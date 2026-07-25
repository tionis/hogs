package database

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite3 "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

func TestServerFieldMigrationMovesAndSealsLegacyMetadata(t *testing.T) {
	dbPath := t.TempDir() + "/server-fields-upgrade.db"
	db, err := sql.Open("sqlite3", sqliteDSNWithForeignKeys(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	driver, err := migratesqlite3.WithInstance(db, &migratesqlite3.Config{})
	if err != nil {
		t.Fatal(err)
	}
	migrations, err := migrate.NewWithInstance("iofs", source, "sqlite3", driver)
	if err != nil {
		t.Fatal(err)
	}
	defer migrations.Close()
	if err := migrations.Migrate(39); err != nil {
		t.Fatalf("migrate to pre-field schema: %v", err)
	}
	result, err := db.Exec(`INSERT INTO servers
		(management_id,name,address,description,map_url,mod_url,state,game_type,show_motd,metadata)
		VALUES('legacy','Legacy','','','','','online','factorio',0,
		'{"edition":"stable","directAddress":"node.example.test:1","api_token":"old-token","rcon_password":"old-rcon"}')`)
	if err != nil {
		t.Fatal(err)
	}
	serverID, _ := result.LastInsertId()
	if err := migrations.Migrate(40); err != nil {
		t.Fatalf("migrate to structured fields: %v", err)
	}

	store := &Store{DB: db}
	if err := store.ConfigureServerFieldEncryption("test-server-field-key-material-000000000000"); err != nil {
		t.Fatal(err)
	}
	fields, err := store.ListServerFields(int(serverID))
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 {
		t.Fatalf("migrated fields=%#v", fields)
	}
	var metadata string
	if err := db.QueryRow("SELECT metadata FROM servers WHERE id=?", serverID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(metadata, "old-token") || strings.Contains(metadata, "old-rcon") ||
		!strings.Contains(metadata, `"edition":"stable"`) || !strings.Contains(metadata, "directAddress") {
		t.Fatalf("migrated metadata=%s", metadata)
	}
	server, err := store.GetServer(int(serverID))
	if err != nil {
		t.Fatal(err)
	}
	if server.Metadata["api_token"] != "old-token" || server.Metadata["rcon_password"] != "old-rcon" {
		t.Fatalf("driver metadata=%#v", server.Metadata)
	}
	for _, field := range fields {
		if field.Disclosure == FieldDisclosurePlain {
			continue
		}
		var value string
		if err := db.QueryRow("SELECT value FROM server_fields WHERE id=?", field.ID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(value, "v1:") || strings.Contains(value, "old-") {
			t.Fatalf("migrated secret %q not sealed: %q", field.Key, value)
		}
	}
}
