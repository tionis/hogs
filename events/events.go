// Package events publishes HOGS activity as CloudEvents to PicoMQ event
// streams (/events/hogs/...), following the event streams contract that
// PicoMQ serves as /events/_spec.
//
// Events are written to a SQLite outbox in the same transaction that assigns
// their per-stream producer sequence. A background flusher appends them in
// order with PicoMQ's idempotent producer headers, so a retry after a crash
// is acknowledged without a duplicate, and publication never blocks the
// action that produced the event.
package events

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	contentType   = "application/cloudevents+json"
	streamDefined = "dev.tionis.events.stream.defined.v1"
	typeDefined   = "dev.tionis.events.type.defined.v1"
	registryKey   = "_registry"
)

// Config selects the PicoMQ endpoint and this instance's identity.
type Config struct {
	URL      string // stream API base, for example http://picomq:4437
	Token    string // write-only token on /events/<Producer>/
	Producer string // namespace below /events/, default "hogs"
	Source   string // CloudEvents source, default /<Producer>/<instance>
	Instance string // producer id for idempotent appends
}

// Publisher queues and delivers events. A nil *Publisher is valid and drops
// every event, so callers need no enabled check.
type Publisher struct {
	cfg    Config
	db     *sql.DB
	client *http.Client
	wake   chan struct{}
	mu     sync.Mutex
	made   map[string]bool
}

// New prepares the outbox and registers definitions when they changed.
func New(db *sql.DB, cfg Config) (*Publisher, error) {
	if cfg.URL == "" || cfg.Token == "" {
		return nil, nil
	}
	if cfg.Producer == "" {
		cfg.Producer = "hogs"
	}
	if cfg.Instance == "" {
		cfg.Instance = "hogs"
	}
	if cfg.Source == "" {
		cfg.Source = "/" + cfg.Producer + "/" + cfg.Instance
	}
	p := &Publisher{
		cfg:    cfg,
		db:     db,
		client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: keepAuthorization(cfg.Token)},
		wake:   make(chan struct{}, 1),
		made:   map[string]bool{},
	}
	if err := p.queueDefinitions(); err != nil {
		return nil, err
	}
	return p, nil
}

func keepAuthorization(token string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many redirects")
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// Emit queues one event. It never fails the caller: errors are logged.
func (p *Publisher) Emit(eventType, subject string, data interface{}) {
	if p == nil {
		return
	}
	definition, ok := typeByName[eventType]
	if !ok {
		log.Printf("events: undeclared type %s dropped", eventType)
		return
	}
	event := map[string]interface{}{
		"specversion":     "1.0",
		"id":              newID(),
		"source":          p.cfg.Source,
		"type":            eventType,
		"time":            time.Now().UTC().Format(time.RFC3339Nano),
		"datacontenttype": "application/json",
		"dataschema":      fmt.Sprintf("picomq:/events/%s/_registry#%s", p.cfg.Producer, eventType),
		"data":            data,
	}
	if subject != "" {
		event["subject"] = subject
	}
	if err := p.enqueue(definition.Stream, event); err != nil {
		log.Printf("events: queue %s: %v", eventType, err)
		return
	}
	p.notify()
}

func (p *Publisher) notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Publisher) streamPath(name string) string {
	return "/events/" + p.cfg.Producer + "/" + name
}

// enqueue assigns the next producer sequence for the stream in the same
// transaction that stores the event.
func (p *Publisher) enqueue(stream string, event map[string]interface{}) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO event_producers(stream, epoch, next_seq) VALUES(?, ?, 0)
		ON CONFLICT(stream) DO NOTHING`, stream, time.Now().Unix()); err != nil {
		return err
	}
	var seq int64
	if err := tx.QueryRow(`SELECT next_seq FROM event_producers WHERE stream=?`, stream).Scan(&seq); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE event_producers SET next_seq=? WHERE stream=?`, seq+1, stream); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO event_outbox(stream, seq, body) VALUES(?, ?, ?)`, stream, seq, string(body)); err != nil {
		return err
	}
	return tx.Commit()
}

// Run delivers queued events until ctx ends.
func (p *Publisher) Run(ctx context.Context) {
	if p == nil {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := p.Flush(ctx); err != nil {
			log.Printf("events: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.wake:
		}
	}
}

type queued struct {
	id   int64
	seq  int64
	body []byte
}

// Flush appends every queued event, stream by stream in sequence order.
func (p *Publisher) Flush(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	rows, err := p.db.Query(`SELECT DISTINCT stream FROM event_outbox ORDER BY stream`)
	if err != nil {
		return err
	}
	var streams []string
	for rows.Next() {
		var stream string
		if err := rows.Scan(&stream); err != nil {
			rows.Close()
			return err
		}
		streams = append(streams, stream)
	}
	rows.Close()
	var failures []string
	for _, stream := range streams {
		if err := p.flushStream(ctx, stream); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", stream, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("flush: %s", strings.Join(failures, "; "))
	}
	return nil
}

func (p *Publisher) flushStream(ctx context.Context, stream string) error {
	if !p.made[stream] {
		if err := p.createStream(ctx, stream); err != nil {
			return err
		}
		p.made[stream] = true
	}
	var epoch int64
	if err := p.db.QueryRow(`SELECT epoch FROM event_producers WHERE stream=?`, stream).Scan(&epoch); err != nil {
		return err
	}
	rows, err := p.db.Query(`SELECT id, seq, body FROM event_outbox WHERE stream=? ORDER BY seq LIMIT 200`, stream)
	if err != nil {
		return err
	}
	var batch []queued
	for rows.Next() {
		var item queued
		var body string
		if err := rows.Scan(&item.id, &item.seq, &body); err != nil {
			rows.Close()
			return err
		}
		item.body = []byte(body)
		batch = append(batch, item)
	}
	rows.Close()
	for _, item := range batch {
		status, header, body, err := p.do(ctx, http.MethodPost, p.streamPath(stream), item.body, map[string]string{
			"Content-Type":        contentType,
			"Pico-Producer-Id":    p.cfg.Instance,
			"Pico-Producer-Epoch": strconv.FormatInt(epoch, 10),
			"Pico-Producer-Seq":   strconv.FormatInt(item.seq, 10),
		})
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusOK:
		case status == http.StatusConflict && expectedAfter(header, item.seq):
			// The server already holds this sequence from an earlier attempt.
		case status == http.StatusConflict:
			// A stale epoch or a sequence gap (for example a restored
			// database): renumber what is queued under a new epoch.
			if err := p.renumber(stream); err != nil {
				return err
			}
			return fmt.Errorf("producer conflict, renumbered under a new epoch: %s", truncate(body))
		default:
			return fmt.Errorf("append: HTTP %d %s", status, truncate(body))
		}
		if _, err := p.db.Exec(`DELETE FROM event_outbox WHERE id=?`, item.id); err != nil {
			return err
		}
	}
	return nil
}

func expectedAfter(header http.Header, seq int64) bool {
	expected, err := strconv.ParseInt(header.Get("Pico-Expected-Seq"), 10, 64)
	return err == nil && expected > seq
}

func (p *Publisher) renumber(stream string) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM event_outbox WHERE stream=? ORDER BY seq`, stream)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	// Move existing sequences out of the way first: (stream, seq) is unique.
	if _, err := tx.Exec(`UPDATE event_outbox SET seq = -seq - 1 WHERE stream=?`, stream); err != nil {
		return err
	}
	for index, id := range ids {
		if _, err := tx.Exec(`UPDATE event_outbox SET seq=? WHERE id=?`, index, id); err != nil {
			return err
		}
	}
	var epoch int64
	if err := tx.QueryRow(`SELECT epoch FROM event_producers WHERE stream=?`, stream).Scan(&epoch); err != nil {
		return err
	}
	next := time.Now().Unix()
	if next <= epoch {
		next = epoch + 1
	}
	if _, err := tx.Exec(`UPDATE event_producers SET epoch=?, next_seq=? WHERE stream=?`, next, len(ids), stream); err != nil {
		return err
	}
	return tx.Commit()
}

func (p *Publisher) createStream(ctx context.Context, stream string) error {
	headers := map[string]string{"Content-Type": contentType}
	if retention := streamRetention(stream); retention > 0 {
		headers["Pico-TTL"] = strconv.FormatInt(retention, 10)
	}
	status, _, body, err := p.do(ctx, http.MethodPut, p.streamPath(stream), nil, headers)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("create %s: HTTP %d %s", p.streamPath(stream), status, truncate(body))
	}
	return nil
}

func (p *Publisher) do(ctx context.Context, method, path string, body []byte, headers map[string]string) (int, http.Header, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.cfg.URL, "/")+path, reader)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.Token)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, resp.Header, payload, nil
}

// queueDefinitions appends the stream and type definitions to the registry
// when they differ from the last registered set.
func (p *Publisher) queueDefinitions() error {
	records := p.definitionRecords()
	canonical, err := json.Marshal(records)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(canonical)
	digest := hex.EncodeToString(sum[:])
	var current string
	err = p.db.QueryRow(`SELECT digest FROM event_registry WHERE producer=?`, p.cfg.Producer).Scan(&current)
	if err == nil && current == digest {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	for _, record := range records {
		event := map[string]interface{}{
			"specversion":     "1.0",
			"id":              newID(),
			"source":          p.cfg.Source,
			"type":            record.Type,
			"time":            time.Now().UTC().Format(time.RFC3339Nano),
			"subject":         record.Subject,
			"datacontenttype": "application/json",
			"data":            record.Data,
		}
		if err := p.enqueue(registryKey, event); err != nil {
			return err
		}
	}
	_, err = p.db.Exec(`INSERT INTO event_registry(producer, digest) VALUES(?, ?)
		ON CONFLICT(producer) DO UPDATE SET digest=excluded.digest`, p.cfg.Producer, digest)
	return err
}

type definitionRecord struct {
	Type    string
	Subject string
	Data    interface{}
}

func (p *Publisher) definitionRecords() []definitionRecord {
	var records []definitionRecord
	for _, stream := range Streams {
		var types []string
		for _, definition := range Types {
			if definition.Stream == stream.Name {
				types = append(types, definition.Type)
			}
		}
		var retention interface{}
		if stream.RetentionSeconds > 0 {
			retention = stream.RetentionSeconds
		}
		records = append(records, definitionRecord{Type: streamDefined, Subject: p.streamPath(stream.Name), Data: map[string]interface{}{
			"stream": p.streamPath(stream.Name), "title": stream.Title, "description": stream.Description,
			"retention_seconds": retention, "types": types,
		}})
	}
	for _, definition := range Types {
		records = append(records, definitionRecord{Type: typeDefined, Subject: definition.Type, Data: map[string]interface{}{
			"type": definition.Type, "stream": p.streamPath(definition.Stream), "title": definition.Title,
			"description": definition.Description, "schema": definition.Schema, "example": definition.Example,
			"deprecated": false, "replaced_by": nil,
		}})
	}
	return records
}

func streamRetention(name string) int64 {
	for _, stream := range Streams {
		if stream.Name == name {
			return stream.RetentionSeconds
		}
	}
	return 0
}

func newID() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:])
}

func truncate(body []byte) string {
	if len(body) > 200 {
		body = body[:200]
	}
	return strings.TrimSpace(string(body))
}
