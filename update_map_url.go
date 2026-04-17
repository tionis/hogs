//go:build ignore
// +build ignore

package main

import (
	"database/sql"
	"log"

	_ "github.com/mattn/go-sqlite3"
)

func main() {
	db, err := sql.Open("sqlite3", "./hogs.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	updateSQL := `UPDATE servers SET map_url = ? WHERE name = ?`
	statement, err := db.Prepare(updateSQL)
	if err != nil {
		log.Fatal(err)
	}
	defer statement.Close()

	_, err = statement.Exec("https://example.com", "Creative")
	if err != nil {
		log.Fatalf("Could not update server: %v", err)
	}

	log.Println("Updated Creative server MapURL to https://example.com")
}
