package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

// consoleReplayLimit bounds how many journal lines one console connection
// replays; it matches the transcript HOGS retains per server.
const consoleReplayLimit = 500

// consoleFallbackBacklog is replayed when a stored cursor no longer resolves,
// for example after journal rotation removed it.
const consoleFallbackBacklog = 200

var journalCursorPattern = regexp.MustCompile(`^[A-Za-z0-9=;_-]{1,512}$`)

// journalCommand is replaced in tests.
var journalCommand = func(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "journalctl", args...)
}

type journalEntry struct {
	Cursor  string
	Message string
	Time    time.Time
}

func journalBaseArgs(unit string) []string {
	return []string{"-u", unit, "--no-pager", "-o", "json", "--output-fields=MESSAGE"}
}

// parseJournalEntry decodes one `journalctl -o json` record. journald encodes
// MESSAGE as a string, or as a byte array when it is not valid UTF-8.
func parseJournalEntry(raw []byte) (journalEntry, bool) {
	var record struct {
		Cursor   string          `json:"__CURSOR"`
		Realtime string          `json:"__REALTIME_TIMESTAMP"`
		Message  json.RawMessage `json:"MESSAGE"`
	}
	if json.Unmarshal(raw, &record) != nil || record.Cursor == "" {
		return journalEntry{}, false
	}
	entry := journalEntry{Cursor: record.Cursor, Time: time.Now().UTC()}
	if usec, err := strconv.ParseInt(record.Realtime, 10, 64); err == nil {
		entry.Time = time.UnixMicro(usec).UTC()
	}
	var text string
	if json.Unmarshal(record.Message, &text) == nil {
		entry.Message = text
		return entry, true
	}
	var data []int
	if json.Unmarshal(record.Message, &data) == nil {
		bytes := make([]byte, 0, len(data))
		for _, value := range data {
			bytes = append(bytes, byte(value))
		}
		entry.Message = string(bytes)
	}
	return entry, true
}

// consoleReplayScanLimit bounds how many journal entries one backlog replay
// reads while looking for lines the console shows; routine lines such as
// status polling can outnumber them by far.
const consoleReplayScanLimit = 20000

// replayJournal returns the entries after afterCursor, or the newest backlog
// entries when no cursor is given, keeping only entries visible accepts and at
// most consoleReplayLimit of them, plus the cursor of the newest entry read.
// Follow mode resumes from that cursor so no line is lost or repeated between
// the two journalctl invocations. Hidden entries never count toward the
// backlog, so a server whose journal is mostly routine lines still shows its
// recent real output.
func replayJournal(ctx context.Context, unit, afterCursor string, backlog int, visible func(journalEntry) bool) ([]journalEntry, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	args := journalBaseArgs(unit)
	reverse := afterCursor == ""
	if reverse {
		args = append(args, "--reverse", "-n", strconv.Itoa(consoleReplayScanLimit))
	} else {
		args = append(args, "--after-cursor="+afterCursor)
	}
	cmd := journalCommand(ctx, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	var entries []journalEntry
	last := ""
	stopped := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		entry, ok := parseJournalEntry(scanner.Bytes())
		if !ok {
			continue
		}
		if !reverse || last == "" {
			last = entry.Cursor
		}
		if !visible(entry) {
			continue
		}
		entries = append(entries, entry)
		if reverse && len(entries) >= backlog {
			stopped = true
			cancel()
			break
		}
		if len(entries) > consoleReplayLimit {
			entries = entries[1:]
		}
	}
	waitErr := cmd.Wait()
	if reverse {
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	}
	if waitErr != nil && !stopped {
		return entries, last, fmt.Errorf("read journal: %w", waitErr)
	}
	return entries, last, scanner.Err()
}
