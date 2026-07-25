package database

import (
	"database/sql"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratesqlite3 "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

func TestImmutableServerIDMigrationPreservesPopulatedData(t *testing.T) {
	dbPath := t.TempDir() + "/upgrade.db"
	dsn := sqliteDSNWithForeignKeys(dbPath)
	db, err := sql.Open("sqlite3", dsn)
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

	if err := migrations.Migrate(37); err != nil {
		t.Fatalf("migrate to legacy schema: %v", err)
	}
	result, err := db.Exec(`
		INSERT INTO servers (
			name, address, description, map_url, mod_url,
			game_type, metadata, show_motd, state
		) VALUES ('Old Factorio Server', '', '', '', '', 'factorio', '{}', 1, 'online')`)
	if err != nil {
		t.Fatal(err)
	}
	serverID64, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	serverID := int(serverID64)
	if _, err := db.Exec(`
		INSERT INTO pterodactyl_servers (server_id, ptero_server_id)
		VALUES (?, 'agent:factorio')`, serverID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO server_resource_samples (server_name, timestamp, running)
		VALUES ('Old Factorio Server', '2026-01-01T00:00:00Z', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO server_metrics (server_name, timestamp, online)
		VALUES ('Old Factorio Server', '2026-01-01T00:00:00Z', 1)`); err != nil {
		t.Fatal(err)
	}
	cronResult, err := db.Exec(`
		INSERT INTO cron_jobs (name, schedule, server_name, action)
		VALUES ('daily-save', '0 0 * * *', 'Old Factorio Server', 'command')`)
	if err != nil {
		t.Fatal(err)
	}
	cronID, err := cronResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO cron_job_logs (cron_job_id, result, output)
		VALUES (?, 'success', 'saved')`, cronID); err != nil {
		t.Fatal(err)
	}

	if err := migrations.Migrate(38); err != nil {
		t.Fatalf("migrate populated schema to immutable IDs: %v", err)
	}

	var managementID string
	if err := db.QueryRow("SELECT management_id FROM servers WHERE id = ?", serverID).Scan(&managementID); err != nil {
		t.Fatal(err)
	}
	if managementID != "factorio" {
		t.Fatalf("management_id = %q, want factorio", managementID)
	}

	if _, err := db.Exec("UPDATE servers SET name = 'Renamed Factorio' WHERE id = ?", serverID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"SELECT COUNT(*) FROM server_resource_samples WHERE server_id = ?",
		"SELECT COUNT(*) FROM server_metrics WHERE server_id = ?",
		"SELECT COUNT(*) FROM cron_jobs WHERE server_id = ?",
	} {
		var count int
		if err := db.QueryRow(query, serverID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s returned %d rows, want 1", query, count)
		}
	}
	var logCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM cron_job_logs WHERE cron_job_id = ?", cronID).Scan(&logCount); err != nil {
		t.Fatal(err)
	}
	if logCount != 1 {
		t.Fatalf("cron log count = %d, want 1", logCount)
	}
	assertNoForeignKeyViolations(t, db)

	if err := migrations.Steps(-1); err != nil {
		t.Fatalf("roll back immutable ID migration: %v", err)
	}
	for _, table := range []string{"server_resource_samples", "server_metrics", "cron_jobs"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE server_name = 'Renamed Factorio'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s rollback rows = %d, want 1", table, count)
		}
	}
	assertNoForeignKeyViolations(t, db)
}

func assertNoForeignKeyViolations(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID int64
		var parent string
		var fkID int
		if err := rows.Scan(&table, &rowID, &parent, &fkID); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("foreign key violation in %s row %d referencing %s (constraint %d)", table, rowID, parent, fkID)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
