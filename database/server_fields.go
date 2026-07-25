package database

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	FieldPlacementSummary  = "summary"
	FieldPlacementDetails  = "details"
	FieldPlacementInternal = "internal"

	FieldDisclosurePlain     = "plain"
	FieldDisclosureReveal    = "reveal"
	FieldDisclosureWriteOnly = "write_only"
)

var serverFieldKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// ServerField is a typed presentation or secret value attached to a server.
// Secret Value fields are blank in ordinary reads and are only populated by
// GetServerFieldValue after a separate authorization decision.
type ServerField struct {
	ID         int    `json:"id"`
	ServerID   int    `json:"serverId"`
	Key        string `json:"key"`
	Label      string `json:"label"`
	Value      string `json:"value,omitempty"`
	Placement  string `json:"placement"`
	Disclosure string `json:"disclosure"`
	SortOrder  int    `json:"sortOrder"`
}

type serverFieldCipher struct {
	aead cipher.AEAD
}

func (s *Server) PublicFields() []ServerField {
	var fields []ServerField
	for _, field := range s.Fields {
		if field.Placement == FieldPlacementSummary && field.Disclosure == FieldDisclosurePlain {
			fields = append(fields, field)
		}
	}
	return fields
}

// EditableMetadata excludes every structured field, especially decrypted
// write-only backend credentials, from the generic administrator metadata UI.
func (s *Server) EditableMetadata() map[string]string {
	fieldKeys := make(map[string]bool, len(s.Fields))
	for _, field := range s.Fields {
		fieldKeys[field.Key] = true
	}
	metadata := make(map[string]string)
	for key, value := range s.Metadata {
		if !fieldKeys[key] && key != "api_token" && key != "rcon_password" {
			metadata[key] = value
		}
	}
	return metadata
}

// ConfigureServerFieldEncryption configures encryption at rest and seals any
// plaintext values left by the one-time legacy metadata migration.
func (s *Store) ConfigureServerFieldEncryption(secret string) error {
	if secret == "" {
		return errors.New("server field encryption secret is required")
	}
	key := sha256.Sum256([]byte("hogs/server-fields/v1\x00" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return fmt.Errorf("initialize server field cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("initialize server field cipher: %w", err)
	}
	s.serverFieldCipher = &serverFieldCipher{aead: aead}
	if err := s.sealLegacyServerFields(); err != nil {
		return err
	}
	return s.scrubLegacyInventorySecrets()
}

func (s *Store) sealServerField(serverID int, fieldKey, value string) (string, error) {
	if s.serverFieldCipher == nil {
		return "", errors.New("server field encryption is not configured")
	}
	nonce := make([]byte, s.serverFieldCipher.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate server field nonce: %w", err)
	}
	aad := []byte(fmt.Sprintf("%d\x00%s", serverID, fieldKey))
	sealed := s.serverFieldCipher.aead.Seal(nonce, nonce, []byte(value), aad)
	return "v1:" + base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Store) openServerField(serverID int, fieldKey, value string) (string, error) {
	if s.serverFieldCipher == nil {
		return "", errors.New("server field encryption is not configured")
	}
	if !strings.HasPrefix(value, "v1:") {
		return "", errors.New("server field value is not encrypted")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "v1:"))
	if err != nil {
		return "", errors.New("server field ciphertext is malformed")
	}
	nonceSize := s.serverFieldCipher.aead.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("server field ciphertext is truncated")
	}
	aad := []byte(fmt.Sprintf("%d\x00%s", serverID, fieldKey))
	plain, err := s.serverFieldCipher.aead.Open(nil, raw[:nonceSize], raw[nonceSize:], aad)
	if err != nil {
		return "", errors.New("server field ciphertext authentication failed")
	}
	return string(plain), nil
}

func (s *Store) sealLegacyServerFields() error {
	rows, err := s.DB.Query(`SELECT id,server_id,field_key,value FROM server_fields
		WHERE disclosure IN ('reveal','write_only') AND value <> '' AND value NOT LIKE 'v1:%'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type legacyField struct {
		id, serverID int
		key, value   string
	}
	var fields []legacyField
	for rows.Next() {
		var field legacyField
		if err := rows.Scan(&field.id, &field.serverID, &field.key, &field.value); err != nil {
			return err
		}
		fields = append(fields, field)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, field := range fields {
		sealed, err := s.sealServerField(field.serverID, field.key, field.value)
		if err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE server_fields SET value=?,updated_at=CURRENT_TIMESTAMP WHERE id=?", sealed, field.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) scrubLegacyInventorySecrets() error {
	var raw string
	err := s.DB.QueryRow("SELECT manifest FROM inventory_state WHERE singleton=1").Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var manifest map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		return fmt.Errorf("decode stored inventory while scrubbing secrets: %w", err)
	}
	changed := false
	servers, _ := manifest["servers"].([]interface{})
	for _, item := range servers {
		server, _ := item.(map[string]interface{})
		metadata, _ := server["metadata"].(map[string]interface{})
		for key := range metadata {
			if serverFieldSensitiveName(key) {
				delete(metadata, key)
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	sanitized, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(sanitized)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	_, err = s.DB.Exec("UPDATE inventory_state SET manifest=?,digest=? WHERE singleton=1", string(sanitized), digest)
	return err
}

func serverFieldSensitiveName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "secret") || strings.Contains(lower, "token") ||
		strings.Contains(lower, "password") || strings.Contains(lower, "key")
}

func ValidateServerField(field ServerField) error {
	field.Key = strings.TrimSpace(field.Key)
	field.Label = strings.TrimSpace(field.Label)
	if !serverFieldKeyPattern.MatchString(field.Key) {
		return errors.New("field key must use 1-64 letters, numbers, dots, dashes, or underscores")
	}
	if field.Label == "" || len([]rune(field.Label)) > 120 || strings.ContainsAny(field.Label, "\r\n\x00") {
		return errors.New("field label must contain 1-120 printable characters")
	}
	switch field.Placement {
	case FieldPlacementSummary, FieldPlacementDetails, FieldPlacementInternal:
	default:
		return errors.New("invalid field placement")
	}
	switch field.Disclosure {
	case FieldDisclosurePlain, FieldDisclosureReveal, FieldDisclosureWriteOnly:
	default:
		return errors.New("invalid field disclosure")
	}
	if field.Disclosure == FieldDisclosureReveal && field.Placement != FieldPlacementDetails {
		return errors.New("revealed fields must use details placement")
	}
	if field.Disclosure == FieldDisclosureWriteOnly && field.Placement != FieldPlacementInternal {
		return errors.New("write-only fields must use internal placement")
	}
	if field.Placement == FieldPlacementInternal && field.Disclosure != FieldDisclosureWriteOnly {
		return errors.New("internal fields must be write-only")
	}
	return nil
}

func (s *Store) ListServerFields(serverID int) ([]ServerField, error) {
	rows, err := s.DB.Query(`SELECT id,server_id,field_key,label,value,placement,disclosure,sort_order
		FROM server_fields WHERE server_id=? ORDER BY sort_order,id`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fields []ServerField
	for rows.Next() {
		var field ServerField
		var storedValue string
		if err := rows.Scan(&field.ID, &field.ServerID, &field.Key, &field.Label, &storedValue,
			&field.Placement, &field.Disclosure, &field.SortOrder); err != nil {
			return nil, err
		}
		if field.Disclosure == FieldDisclosurePlain {
			field.Value = storedValue
		}
		fields = append(fields, field)
	}
	return fields, rows.Err()
}

func (s *Store) hydrateServerFields(server *Server) error {
	fields, err := s.ListServerFields(server.ID)
	if err != nil {
		return err
	}
	server.Fields = fields
	if server.Metadata == nil {
		server.Metadata = make(map[string]string)
	}
	fieldKeys := make(map[string]bool, len(fields))
	for _, field := range fields {
		fieldKeys[field.Key] = true
		if field.Disclosure != FieldDisclosureWriteOnly {
			continue
		}
		value, err := s.GetServerFieldValue(server.ID, field.ID)
		if err != nil {
			return fmt.Errorf("open internal server field %q: %w", field.Key, err)
		}
		server.Metadata[field.Key] = value
	}
	// Keep ordinary metadata created by older API clients or inventory manifests
	// visible until an administrator saves it as a structured field.
	for key, value := range server.Metadata {
		if fieldKeys[key] || key == "directAddress" || key == "map_lifecycle" ||
			key == "api_token" || key == "rcon_password" {
			continue
		}
		fields = append(fields, ServerField{
			ServerID: server.ID, Key: key, Label: key, Value: value,
			Placement: FieldPlacementSummary, Disclosure: FieldDisclosurePlain,
			SortOrder: len(fields),
		})
	}
	server.Fields = fields
	return nil
}

func (s *Store) GetServerField(serverID, fieldID int) (*ServerField, error) {
	var field ServerField
	var storedValue string
	err := s.DB.QueryRow(`SELECT id,server_id,field_key,label,value,placement,disclosure,sort_order
		FROM server_fields WHERE server_id=? AND id=?`, serverID, fieldID).
		Scan(&field.ID, &field.ServerID, &field.Key, &field.Label, &storedValue,
			&field.Placement, &field.Disclosure, &field.SortOrder)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if field.Disclosure == FieldDisclosurePlain {
		field.Value = storedValue
	}
	return &field, nil
}

func (s *Store) GetServerFieldValue(serverID, fieldID int) (string, error) {
	var key, disclosure, value string
	err := s.DB.QueryRow(`SELECT field_key,disclosure,value FROM server_fields
		WHERE server_id=? AND id=?`, serverID, fieldID).Scan(&key, &disclosure, &value)
	if err != nil {
		return "", err
	}
	if disclosure == FieldDisclosurePlain {
		return value, nil
	}
	return s.openServerField(serverID, key, value)
}

// ReplaceServerFields applies the complete administrator-edited field list.
// A blank value preserves an existing encrypted value; new encrypted fields
// require a value.
func (s *Store) ReplaceServerFields(serverID int, fields []ServerField) error {
	existingRows, err := s.DB.Query(`SELECT id,field_key,value,disclosure FROM server_fields WHERE server_id=?`, serverID)
	if err != nil {
		return err
	}
	type existingField struct {
		key, value, disclosure string
	}
	existing := make(map[int]existingField)
	for existingRows.Next() {
		var id int
		var field existingField
		if err := existingRows.Scan(&id, &field.key, &field.value, &field.disclosure); err != nil {
			existingRows.Close()
			return err
		}
		existing[id] = field
	}
	if err := existingRows.Close(); err != nil {
		return err
	}

	seenKeys := make(map[string]bool)
	for i := range fields {
		fields[i].ServerID = serverID
		fields[i].Key = strings.TrimSpace(fields[i].Key)
		fields[i].Label = strings.TrimSpace(fields[i].Label)
		fields[i].SortOrder = i
		if err := ValidateServerField(fields[i]); err != nil {
			return fmt.Errorf("field %q: %w", fields[i].Key, err)
		}
		if seenKeys[fields[i].Key] {
			return fmt.Errorf("field key %q is duplicated", fields[i].Key)
		}
		seenKeys[fields[i].Key] = true
	}

	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keep := make([]int, 0, len(fields))
	for _, field := range fields {
		storedValue := field.Value
		if field.Disclosure != FieldDisclosurePlain {
			if storedValue == "" {
				old, ok := existing[field.ID]
				if !ok || old.value == "" || old.key != field.Key || old.disclosure == FieldDisclosurePlain {
					return fmt.Errorf("field %q requires a secret value", field.Key)
				}
				storedValue = old.value
			} else {
				storedValue, err = s.sealServerField(serverID, field.Key, storedValue)
				if err != nil {
					return err
				}
			}
		}
		if field.ID > 0 {
			result, err := tx.Exec(`UPDATE server_fields
				SET field_key=?,label=?,value=?,placement=?,disclosure=?,sort_order=?,updated_at=CURRENT_TIMESTAMP
				WHERE id=? AND server_id=?`,
				field.Key, field.Label, storedValue, field.Placement, field.Disclosure,
				field.SortOrder, field.ID, serverID)
			if err != nil {
				return err
			}
			updated, _ := result.RowsAffected()
			if updated != 1 {
				return fmt.Errorf("field %q no longer exists", field.Key)
			}
			keep = append(keep, field.ID)
		} else {
			result, err := tx.Exec(`INSERT INTO server_fields
				(server_id,field_key,label,value,placement,disclosure,sort_order)
				VALUES(?,?,?,?,?,?,?)`, serverID, field.Key, field.Label, storedValue,
				field.Placement, field.Disclosure, field.SortOrder)
			if err != nil {
				return err
			}
			id, _ := result.LastInsertId()
			keep = append(keep, int(id))
		}
	}
	if len(keep) == 0 {
		if _, err := tx.Exec("DELETE FROM server_fields WHERE server_id=?", serverID); err != nil {
			return err
		}
	} else {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(keep)), ",")
		args := make([]interface{}, 0, len(keep)+1)
		args = append(args, serverID)
		for _, id := range keep {
			args = append(args, id)
		}
		if _, err := tx.Exec("DELETE FROM server_fields WHERE server_id=? AND id NOT IN ("+placeholders+")", args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
