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

	servers := []struct {
		Name        string
		Address     string
		Description string
		MapURL      string
		ModURL      string
		GameType    string
	}{
		{
			Name:        "Creative",
			Address:     "creative.example.com:25565",
			Description: "A server for creative building.",
			MapURL:      "https://maps.example.com/creative",
			ModURL:      "/files/creative/mods/creative-pack-v1.zip",
			GameType:    "minecraft",
		},
		{
			Name:        "Survival",
			Address:     "survival.example.com:25565",
			Description: "A challenging survival server.",
			MapURL:      "https://maps.example.com/survival",
			ModURL:      "/files/survival/mods/survival-pack-v2.zip",
			GameType:    "minecraft",
		},
		{
			Name:        "Satisfactory",
			Address:     "satisfactory.example.com:15777",
			Description: "Our Satisfactory server.",
			GameType:    "satisfactory",
		},
		{
			Name:        "Factorio",
			Address:     "factorio.example.com:27015",
			Description: "Our Factorio server.",
			GameType:    "factorio",
		},
	}

	insertSQL := `INSERT INTO servers (name, address, description, map_url, mod_url, state, game_type, show_motd, metadata) VALUES (?, ?, ?, ?, ?, 'online', ?, 1, '{}')`
	statement, err := db.Prepare(insertSQL)
	if err != nil {
		log.Fatal(err)
	}
	defer statement.Close()

	for _, server := range servers {
		_, err := statement.Exec(server.Name, server.Address, server.Description, server.MapURL, server.ModURL, server.GameType)
		if err != nil {
			log.Printf("Could not insert server %s: %v", server.Name, err)
		} else {
			log.Printf("Inserted server: %s (%s)", server.Name, server.GameType)
		}
	}
	log.Println("Dummy data insertion complete.")
}
