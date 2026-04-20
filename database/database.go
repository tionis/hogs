package database

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

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

type UserWhitelist struct {
	ID        int    `json:"id"`
	UserEmail string `json:"userEmail"`
	ServerID  int    `json:"serverId"`
	Username  string `json:"username"`
}

func (s *Store) GetUserWhitelist(email string, serverID int) (*UserWhitelist, error) {
	row := s.DB.QueryRow("SELECT id, user_email, server_id, username FROM user_whitelists WHERE user_email = ? AND server_id = ?", email, serverID)
	var uw UserWhitelist
	err := row.Scan(&uw.ID, &uw.UserEmail, &uw.ServerID, &uw.Username)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &uw, nil
}

func (s *Store) SetUserWhitelist(email string, serverID int, username string) error {
	_, err := s.DB.Exec("INSERT INTO user_whitelists (user_email, server_id, username) VALUES (?, ?, ?) ON CONFLICT(user_email, server_id) DO UPDATE SET username = ?", email, serverID, username, username)
	return err
}

func (s *Store) DeleteUserWhitelist(email string, serverID int) error {
	_, err := s.DB.Exec("DELETE FROM user_whitelists WHERE user_email = ? AND server_id = ?", email, serverID)
	return err
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

func (s *Store) GetServer(id int) (*Server, error) {
	row := s.DB.QueryRow("SELECT "+serverColumns+" FROM servers WHERE id = ?", id)

	srv, err := scanServer(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	return srv, nil
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
	ID          int      `json:"id"`
	Filename    string   `json:"filename"`
	ContentHash string   `json:"contentHash"`
	Enabled     bool     `json:"enabled"`
	Tags        []string `json:"tags"`
}

func (s *Store) ListBackgrounds() ([]Background, error) {
	rows, err := s.DB.Query("SELECT id, filename, content_hash, enabled FROM backgrounds ORDER BY uploaded_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var bgs []Background
	for rows.Next() {
		var bg Background
		var enabled int
		if err := rows.Scan(&bg.ID, &bg.Filename, &bg.ContentHash, &enabled); err != nil {
			return nil, err
		}
		bg.Enabled = enabled == 1
		bg.Tags, _ = s.GetBackgroundTags(bg.ID)
		bgs = append(bgs, bg)
	}
	return bgs, nil
}

func (s *Store) CreateBackground(bg *Background) error {
	stmt, err := s.DB.Prepare("INSERT INTO backgrounds (filename, content_hash, enabled) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	enabled := 1
	if !bg.Enabled {
		enabled = 0
	}
	result, err := stmt.Exec(bg.Filename, bg.ContentHash, enabled)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	bg.ID = int(id)
	if err := s.SetBackgroundTags(bg.ID, bg.Tags); err != nil {
		return err
	}
	return nil
}

func (s *Store) GetBackgroundTags(backgroundID int) ([]string, error) {
	rows, err := s.DB.Query("SELECT tag FROM background_tags WHERE background_id = ? ORDER BY tag", backgroundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

func (s *Store) SetBackgroundTags(backgroundID int, tags []string) error {
	_, err := s.DB.Exec("DELETE FROM background_tags WHERE background_id = ?", backgroundID)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		return nil
	}
	stmt, err := s.DB.Prepare("INSERT INTO background_tags (background_id, tag) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, tag := range tags {
		if _, err := stmt.Exec(backgroundID, tag); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteBackground(id int) error {
	_, err := s.DB.Exec("DELETE FROM background_tags WHERE background_id = ?", id)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("DELETE FROM backgrounds WHERE id = ?", id)
	return err
}

func (s *Store) UpdateBackground(bg *Background) error {
	enabled := 0
	if bg.Enabled {
		enabled = 1
	}
	_, err := s.DB.Exec("UPDATE backgrounds SET enabled = ? WHERE id = ?", enabled, bg.ID)
	if err != nil {
		return err
	}
	return s.SetBackgroundTags(bg.ID, bg.Tags)
}

func (s *Store) GetRandomBackground(tags []string) (*Background, error) {
	if len(tags) == 0 {
		return nil, nil
	}

	query := `SELECT b.id, b.filename, b.content_hash, b.enabled
		FROM backgrounds b
		JOIN background_tags bt ON b.id = bt.background_id
		WHERE b.enabled = 1 AND bt.tag IN (` + placeholders(len(tags)) + `)
		GROUP BY b.id
		HAVING COUNT(DISTINCT bt.tag) = ?
		ORDER BY RANDOM() LIMIT 1`

	args := make([]interface{}, len(tags)+1)
	for i, t := range tags {
		args[i] = t
	}
	args[len(tags)] = len(tags)

	row := s.DB.QueryRow(query, args...)
	var bg Background
	var enabled int
	err := row.Scan(&bg.ID, &bg.Filename, &bg.ContentHash, &enabled)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	bg.Enabled = enabled == 1
	bg.Tags, _ = s.GetBackgroundTags(bg.ID)
	return &bg, nil
}

func placeholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
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

type User struct {
	ID        int    `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	FirstSeen string `json:"firstSeen"`
	LastLogin string `json:"lastLogin"`
}

func (s *Store) CreateUser(email, role string) (*User, error) {
	if role == "" {
		role = "user"
	}
	result, err := s.DB.Exec("INSERT INTO users (email, role) VALUES (?, ?)", email, role)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return &User{ID: int(id), Email: email, Role: role}, nil
}

func (s *Store) GetUserByEmail(email string) (*User, error) {
	row := s.DB.QueryRow("SELECT id, email, role, first_seen, last_login FROM users WHERE email = ?", email)
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Role, &u.FirstSeen, &u.LastLogin)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func (s *Store) UpdateUserRole(id int, role string) error {
	_, err := s.DB.Exec("UPDATE users SET role = ? WHERE id = ?", role, id)
	return err
}

func (s *Store) TouchUserLastLogin(id int) error {
	_, err := s.DB.Exec("UPDATE users SET last_login = CURRENT_TIMESTAMP WHERE id = ?", id)
	return err
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.DB.Query("SELECT id, email, role, first_seen, last_login FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.FirstSeen, &u.LastLogin); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

type PterodactylLink struct {
	ID              int    `json:"id"`
	ServerID        int    `json:"serverId"`
	PteroServerID   string `json:"pteroServerId"`
	PteroIdentifier string `json:"pteroIdentifier"`
	AllowedActions  string `json:"allowedActions"`
}

type PterodactylCommand struct {
	ID          int    `json:"id"`
	ServerID    int    `json:"serverId"`
	Command     string `json:"command"`
	DisplayName string `json:"displayName"`
}

func (s *Store) GetPterodactylLink(serverID int) (*PterodactylLink, error) {
	row := s.DB.QueryRow("SELECT id, server_id, ptero_server_id, ptero_identifier, allowed_actions FROM pterodactyl_servers WHERE server_id = ?", serverID)
	var link PterodactylLink
	err := row.Scan(&link.ID, &link.ServerID, &link.PteroServerID, &link.PteroIdentifier, &link.AllowedActions)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &link, nil
}

func (s *Store) CreatePterodactylLink(link *PterodactylLink) error {
	result, err := s.DB.Exec("INSERT INTO pterodactyl_servers (server_id, ptero_server_id, ptero_identifier, allowed_actions) VALUES (?, ?, ?, ?)",
		link.ServerID, link.PteroServerID, link.PteroIdentifier, link.AllowedActions)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	link.ID = int(id)
	return nil
}

func (s *Store) UpdatePterodactylLink(link *PterodactylLink) error {
	_, err := s.DB.Exec("UPDATE pterodactyl_servers SET ptero_server_id = ?, ptero_identifier = ?, allowed_actions = ? WHERE server_id = ?",
		link.PteroServerID, link.PteroIdentifier, link.AllowedActions, link.ServerID)
	return err
}

func (s *Store) DeletePterodactylLink(serverID int) error {
	_, err := s.DB.Exec("DELETE FROM pterodactyl_servers WHERE server_id = ?", serverID)
	return err
}

func (s *Store) ListPterodactylCommands(serverID int) ([]PterodactylCommand, error) {
	rows, err := s.DB.Query("SELECT id, server_id, command, display_name FROM pterodactyl_commands WHERE server_id = ?", serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var commands []PterodactylCommand
	for rows.Next() {
		var cmd PterodactylCommand
		if err := rows.Scan(&cmd.ID, &cmd.ServerID, &cmd.Command, &cmd.DisplayName); err != nil {
			return nil, err
		}
		commands = append(commands, cmd)
	}
	return commands, nil
}

func (s *Store) CreatePterodactylCommand(cmd *PterodactylCommand) error {
	result, err := s.DB.Exec("INSERT INTO pterodactyl_commands (server_id, command, display_name) VALUES (?, ?, ?)",
		cmd.ServerID, cmd.Command, cmd.DisplayName)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	cmd.ID = int(id)
	return nil
}

func (s *Store) DeletePterodactylCommand(id int) error {
	_, err := s.DB.Exec("DELETE FROM pterodactyl_commands WHERE id = ?", id)
	return err
}

func (bg *Background) URL() string {
	if bg.ContentHash != "" {
		return "/backgrounds/" + bg.ContentHash + "/" + bg.Filename
	}
	return "/backgrounds/" + bg.Filename
}

func (s *Store) ComputeMissingHashes(bgDir string) error {
	rows, err := s.DB.Query("SELECT id, filename, content_hash FROM backgrounds WHERE content_hash = ''")
	if err != nil {
		return err
	}
	defer rows.Close()

	type update struct {
		id          int
		contentHash string
	}
	var updates []update

	for rows.Next() {
		var id int
		var filename, hash string
		if err := rows.Scan(&id, &filename, &hash); err != nil {
			return err
		}
		data, err := os.ReadFile(filepath.Join(bgDir, filename))
		if err != nil {
			log.Printf("Warning: could not read background file %s: %v", filename, err)
			continue
		}
		h := sha256.Sum256(data)
		contentHash := hex.EncodeToString(h[:])[:16]
		updates = append(updates, update{id: id, contentHash: contentHash})
	}

	for _, u := range updates {
		if _, err := s.DB.Exec("UPDATE backgrounds SET content_hash = ? WHERE id = ?", u.contentHash, u.id); err != nil {
			return err
		}
	}
	return nil
}
