package database

import (
	"strings"
	"testing"
)

func TestServerFieldsEncryptAndSeparateDisclosure(t *testing.T) {
	store := testStore(t)
	if err := store.ConfigureServerFieldEncryption("test-server-field-key-material-000000000000"); err != nil {
		t.Fatal(err)
	}
	server := &Server{Name: "Field Test", GameType: "generic", State: "online", Metadata: map[string]string{}}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	fields := []ServerField{
		{Key: "region", Label: "Region", Value: "Europe", Placement: FieldPlacementSummary, Disclosure: FieldDisclosurePlain},
		{Key: "join_password", Label: "Join password", Value: "correct horse battery staple", Placement: FieldPlacementDetails, Disclosure: FieldDisclosureReveal},
		{Key: "management_password", Label: "Management password", Value: "operator-only", Placement: FieldPlacementInternal, Disclosure: FieldDisclosureWriteOnly},
	}
	if err := store.ReplaceServerFields(server.ID, fields); err != nil {
		t.Fatal(err)
	}

	listed, err := store.ListServerFields(server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 || listed[0].Value != "Europe" || listed[1].Value != "" || listed[2].Value != "" {
		t.Fatalf("sanitized fields = %#v", listed)
	}
	for _, field := range listed[1:] {
		var stored string
		if err := store.DB.QueryRow("SELECT value FROM server_fields WHERE id=?", field.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(stored, "v1:") ||
			strings.Contains(stored, "correct horse") || strings.Contains(stored, "operator-only") {
			t.Fatalf("field %q was not encrypted at rest: %q", field.Key, stored)
		}
	}
	value, err := store.GetServerFieldValue(server.ID, listed[1].ID)
	if err != nil || value != "correct horse battery staple" {
		t.Fatalf("revealed value=%q err=%v", value, err)
	}

	hydrated, err := store.GetServer(server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hydrated.Metadata["management_password"] != "operator-only" {
		t.Fatal("write-only backend field was not made available to the game driver")
	}
	if _, leaked := hydrated.PublicMetadata()["management_password"]; leaked {
		t.Fatal("generic write-only field leaked through public metadata")
	}
	if len(hydrated.PublicFields()) != 1 || hydrated.PublicFields()[0].Key != "region" {
		t.Fatalf("public fields = %#v", hydrated.PublicFields())
	}

	// Blank encrypted values retain the existing ciphertext during ordinary
	// settings edits.
	listed[0].Value = "Germany"
	if err := store.ReplaceServerFields(server.ID, listed); err != nil {
		t.Fatal(err)
	}
	value, err = store.GetServerFieldValue(server.ID, listed[1].ID)
	if err != nil || value != "correct horse battery staple" {
		t.Fatalf("preserved value=%q err=%v", value, err)
	}
}

func TestServerFieldValidationKeepsSecurityModesCoherent(t *testing.T) {
	tests := []ServerField{
		{Key: "password", Label: "Password", Placement: FieldPlacementSummary, Disclosure: FieldDisclosureReveal},
		{Key: "token", Label: "Token", Placement: FieldPlacementDetails, Disclosure: FieldDisclosureWriteOnly},
		{Key: "notes", Label: "Notes", Placement: FieldPlacementInternal, Disclosure: FieldDisclosurePlain},
	}
	for _, field := range tests {
		if err := ValidateServerField(field); err == nil {
			t.Fatalf("field %#v unexpectedly passed validation", field)
		}
	}
}

func TestConfiguringEncryptionSealsMigrationValuesAndScrubsInventoryState(t *testing.T) {
	store := testStore(t)
	server := &Server{Name: "Migrated", GameType: "generic", State: "online"}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(`INSERT INTO server_fields
		(server_id,field_key,label,value,placement,disclosure,sort_order)
		VALUES(?, 'legacy_password', 'Legacy password', 'plaintext-legacy-value', 'internal', 'write_only', 0)`,
		server.ID); err != nil {
		t.Fatal(err)
	}
	manifest := `{"apiVersion":"hogs.tionis.dev/v1alpha2","generation":"old","nodes":[],"servers":[{"metadata":{"join_password":"inventory-plaintext"}}]}`
	if _, err := store.DB.Exec(`INSERT INTO inventory_state(singleton,generation,digest,manifest,actor)
		VALUES(1,'old','sha256:old',?,'test')`, manifest); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfigureServerFieldEncryption("test-server-field-key-material-000000000000"); err != nil {
		t.Fatal(err)
	}
	var fieldValue, storedManifest string
	if err := store.DB.QueryRow("SELECT value FROM server_fields WHERE field_key='legacy_password'").Scan(&fieldValue); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow("SELECT manifest FROM inventory_state WHERE singleton=1").Scan(&storedManifest); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fieldValue, "v1:") || strings.Contains(fieldValue, "plaintext-legacy-value") {
		t.Fatalf("legacy field was not sealed: %q", fieldValue)
	}
	if strings.Contains(storedManifest, "inventory-plaintext") || strings.Contains(storedManifest, "join_password") {
		t.Fatalf("legacy inventory secret remained at rest: %s", storedManifest)
	}
}

func TestSetServerSecretFieldRoundTrip(t *testing.T) {
	store := testStore(t)
	if err := store.ConfigureServerFieldEncryption("test-server-field-key-material-000000000000"); err != nil {
		t.Fatal(err)
	}
	server := &Server{Name: "Secret Test", GameType: "generic", State: "online", Metadata: map[string]string{}}
	if err := store.CreateServer(server); err != nil {
		t.Fatal(err)
	}
	if err := store.SetServerSecretField(server.ID, "api_token", "token-one"); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	fields, err := store.ListServerFields(server.ID)
	if err != nil || len(fields) != 1 {
		t.Fatalf("fields = %#v err=%v", fields, err)
	}
	if fields[0].Placement != FieldPlacementInternal || fields[0].Disclosure != FieldDisclosureWriteOnly {
		t.Fatalf("secret field has wrong shape: %+v", fields[0])
	}
	value, err := store.GetServerFieldValue(server.ID, fields[0].ID)
	if err != nil || value != "token-one" {
		t.Fatalf("round trip value=%q err=%v", value, err)
	}
	fingerprint, err := store.FingerprintServerSecret("scope", "api_token", "token-one")
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	same, err := store.FingerprintServerSecret("scope", "api_token", "token-one")
	if err != nil || same != fingerprint || !strings.HasPrefix(fingerprint, "hmac-sha256:") {
		t.Fatalf("fingerprint not stable: %q err=%v", same, err)
	}
	other, err := store.FingerprintServerSecret("scope", "api_token", "token-two")
	if err != nil || other == fingerprint {
		t.Fatal("fingerprint did not change with value")
	}
	if err := store.SetServerSecretField(server.ID, "game_password", "x"); err == nil {
		t.Fatal("unmanaged key was accepted")
	}
	if err := store.SetServerSecretField(server.ID, "api_token", ""); err != nil {
		t.Fatalf("remove secret: %v", err)
	}
	fields, err = store.ListServerFields(server.ID)
	if err != nil || len(fields) != 0 {
		t.Fatalf("removed field still present: %#v err=%v", fields, err)
	}
}
