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

type Background struct {
	ID        int    `json:"id"`
	Filename  string `json:"filename"`
	ThemeMode string `json:"themeMode"`
	GameType  string `json:"gameType"`
	Enabled   bool   `json:"enabled"`
}

func (s *Store) ListBackgrounds() ([]Background, error) {
	rows, err := s.DB.Query("SELECT id, filename, theme_mode, game_type, enabled FROM backgrounds ORDER BY uploaded_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bgs []Background
	for rows.Next() {
		var bg Background
		var enabled int
		if err := rows.Scan(&bg.ID, &bg.Filename, &bg.ThemeMode, &bg.GameType, &enabled); err != nil {
			return nil, err
		}
		bg.Enabled = enabled == 1
		bgs = append(bgs, bg)
	}
	return bgs, nil
}

func (s *Store) CreateBackground(bg *Background) error {
	stmt, err := s.DB.Prepare("INSERT INTO backgrounds (filename, theme_mode, game_type, enabled) VALUES (?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	enabled := 1
	if !bg.Enabled {
		enabled = 0
	}
	result, err := stmt.Exec(bg.Filename, bg.ThemeMode, bg.GameType, enabled)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	bg.ID = int(id)
	return nil
}

func (s *Store) DeleteBackground(id int) error {
	stmt, err := s.DB.Prepare("DELETE FROM backgrounds WHERE id=?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	_, err = stmt.Exec(id)
	return err
}

func (s *Store) UpdateBackground(bg *Background) error {
	enabled := 0
	if bg.Enabled {
		enabled = 1
	}
	_, err := s.DB.Exec("UPDATE backgrounds SET theme_mode = ?, game_type = ?, enabled = ? WHERE id = ?", bg.ThemeMode, bg.GameType, enabled, bg.ID)
	return err
}

func (s *Store) GetRandomBackground(theme, gameType string) (*Background, error) {
	query := "SELECT id, filename, theme_mode, game_type, enabled FROM backgrounds WHERE enabled = 1 AND (theme_mode = ? OR theme_mode = 'all')"
	args := []interface{}{theme}

	if gameType != "" && gameType != "all" {
		query += " AND (game_type = ? OR game_type = 'all')"
		args = append(args, gameType)
	}

	query += " ORDER BY RANDOM() LIMIT 1"

	row := s.DB.QueryRow(query, args...)
	var bg Background
	var enabled int
	err := row.Scan(&bg.ID, &bg.Filename, &bg.ThemeMode, &bg.GameType, &enabled)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	bg.Enabled = enabled == 1
	return &bg, nil
}

func (s *Store) GetSetting(key string) (string, error) {
	var value string
	err := s.DB.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return value, nil
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = ?", key, value, value)
	return err
}
