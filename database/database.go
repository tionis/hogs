package database

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Server struct {
	ID          int               `json:"id"`
	Name        string            `json:"name"`
	Address     string            `json:"address"`
	Description string            `json:"description"`
	MapURL      string            `json:"mapUrl"`
	ModURL      string            `json:"modUrl"`
	State       string            `json:"state"`
	GameType    string            `json:"gameType"`
	ShowMOTD    bool              `json:"showMotd"`
	Metadata    map[string]string `json:"metadata"`
}

var sensitiveMetadataKeys = map[string]bool{
	"api_token":     true,
	"rcon_password": true,
}

func (s *Server) PublicMetadata() map[string]string {
	if s.Metadata == nil {
		return nil
	}
	public := make(map[string]string, len(s.Metadata))
	for k, v := range s.Metadata {
		if !sensitiveMetadataKeys[k] {
			public[k] = v
		}
	}
	return public
}

type PublicServer struct {
	ID          int               `json:"id"`
	Name        string            `json:"name"`
	Address     string            `json:"address"`
	Description string            `json:"description"`
	MapURL      string            `json:"mapUrl"`
	ModURL      string            `json:"modUrl"`
	State       string            `json:"state"`
	GameType    string            `json:"gameType"`
	ShowMOTD    bool              `json:"showMotd"`
	Metadata    map[string]string `json:"metadata"`
}

func (s *Server) ToPublic() *PublicServer {
	return &PublicServer{
		ID:          s.ID,
		Name:        s.Name,
		Address:     s.Address,
		Description: s.Description,
		MapURL:      s.MapURL,
		ModURL:      s.ModURL,
		State:       s.State,
		GameType:    s.GameType,
		ShowMOTD:    s.ShowMOTD,
		Metadata:    s.PublicMetadata(),
	}
}

type Store struct {
	DB *sql.DB
}

func NewStore(dataSourceName string) (*Store, error) {
	if err := runMigrations(dataSourceName); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	db, err := sql.Open("sqlite3", dataSourceName)
	if err != nil {
		return nil, err
	}

	if err = db.Ping(); err != nil {
		return nil, err
	}

	log.Println("Database connection established.")
	store := &Store{DB: db}

	return store, nil
}

func runMigrations(dataSourceName string) error {
	driver, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlite3", dataSourceName)
	if err != nil {
		return err
	}
	defer db.Close()

	dbDriver, err := sqlite3.WithInstance(db, &sqlite3.Config{})
	if err != nil {
		return err
	}

	m, err := migrate.NewWithInstance(
		"iofs", driver,
		"sqlite3", dbDriver,
	)
	if err != nil {
		return err
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}

	log.Println("Database migrations ran successfully.")
	return nil
}

const serverColumns = "id, name, address, description, map_url, mod_url, state, game_type, show_motd, metadata"

func scanServer(scanner interface{ Scan(...interface{}) error }) (*Server, error) {
	var srv Server
	var showMotd int
	var metadataJSON string

	err := scanner.Scan(&srv.ID, &srv.Name, &srv.Address, &srv.Description, &srv.MapURL, &srv.ModURL, &srv.State, &srv.GameType, &showMotd, &metadataJSON)
	if err != nil {
		return nil, err
	}

	srv.ShowMOTD = showMotd == 1

	if metadataJSON != "" {
		if err := json.Unmarshal([]byte(metadataJSON), &srv.Metadata); err != nil {
			log.Printf("Warning: failed to unmarshal metadata for server %s: %v", srv.Name, err)
			srv.Metadata = make(map[string]string)
		}
	} else {
		srv.Metadata = make(map[string]string)
	}

	return &srv, nil
}

func (s *Store) ListServers() ([]Server, error) {
	rows, err := s.DB.Query("SELECT " + serverColumns + " FROM servers ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var servers []Server
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		servers = append(servers, *srv)
	}

	return servers, nil
}

func (s *Store) GetServerByName(name string) (*Server, error) {
	row := s.DB.QueryRow("SELECT "+serverColumns+" FROM servers WHERE name = ?", name)

	srv, err := scanServer(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return srv, nil
}

func (s *Store) CreateServer(srv *Server) error {
	stmt, err := s.DB.Prepare("INSERT INTO servers (name, address, description, map_url, mod_url, state, game_type, show_motd, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	showMotd := 0
	if srv.ShowMOTD {
		showMotd = 1
	}

	metadataJSON, err := json.Marshal(srv.Metadata)
	if err != nil {
		return err
	}

	gameType := srv.GameType
	if gameType == "" {
		gameType = "minecraft"
	}

	_, err = stmt.Exec(srv.Name, srv.Address, srv.Description, srv.MapURL, srv.ModURL, srv.State, gameType, showMotd, string(metadataJSON))
	return err
}

func (s *Store) UpdateServer(srv *Server) error {
	stmt, err := s.DB.Prepare("UPDATE servers SET name=?, address=?, description=?, map_url=?, mod_url=?, state=?, game_type=?, show_motd=?, metadata=? WHERE id=?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	showMotd := 0
	if srv.ShowMOTD {
		showMotd = 1
	}

	metadataJSON, err := json.Marshal(srv.Metadata)
	if err != nil {
		return err
	}

	gameType := srv.GameType
	if gameType == "" {
		gameType = "minecraft"
	}

	_, err = stmt.Exec(srv.Name, srv.Address, srv.Description, srv.MapURL, srv.ModURL, srv.State, gameType, showMotd, string(metadataJSON), srv.ID)
	return err
}

func (s *Store) DeleteServer(id int) error {
	stmt, err := s.DB.Prepare("DELETE FROM servers WHERE id=?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(id)
	return err
}
