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
	"time"

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
	ID          int    `json:"id"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	FirstSeen   string `json:"firstSeen"`
	LastLogin   string `json:"lastLogin"`
	ExternalID  string `json:"externalId"`
	DisplayName string `json:"displayName"`
	Active      bool   `json:"active"`
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
	row := s.DB.QueryRow("SELECT id, email, role, first_seen, last_login, external_id, display_name, active FROM users WHERE email = ?", email)
	var u User
	var active int
	err := row.Scan(&u.ID, &u.Email, &u.Role, &u.FirstSeen, &u.LastLogin, &u.ExternalID, &u.DisplayName, &active)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	u.Active = active == 1
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
	rows, err := s.DB.Query("SELECT id, email, role, first_seen, last_login, external_id, display_name, active FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var active int
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.FirstSeen, &u.LastLogin, &u.ExternalID, &u.DisplayName, &active); err != nil {
			return nil, err
		}
		u.Active = active == 1
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
	ACLRule         string `json:"aclRule"`
	Node            string `json:"node"`
}

type PterodactylCommand struct {
	ID          int    `json:"id"`
	ServerID    int    `json:"serverId"`
	Command     string `json:"command"`
	DisplayName string `json:"displayName"`
}

func (s *Store) GetPterodactylLink(serverID int) (*PterodactylLink, error) {
	row := s.DB.QueryRow("SELECT id, server_id, ptero_server_id, ptero_identifier, allowed_actions, acl_rule, node FROM pterodactyl_servers WHERE server_id = ?", serverID)
	var link PterodactylLink
	err := row.Scan(&link.ID, &link.ServerID, &link.PteroServerID, &link.PteroIdentifier, &link.AllowedActions, &link.ACLRule, &link.Node)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &link, nil
}

func (s *Store) CreatePterodactylLink(link *PterodactylLink) error {
	result, err := s.DB.Exec("INSERT INTO pterodactyl_servers (server_id, ptero_server_id, ptero_identifier, allowed_actions, acl_rule, node) VALUES (?, ?, ?, ?, ?, ?)",
		link.ServerID, link.PteroServerID, link.PteroIdentifier, link.AllowedActions, link.ACLRule, link.Node)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	link.ID = int(id)
	return nil
}

func (s *Store) UpdatePterodactylLink(link *PterodactylLink) error {
	_, err := s.DB.Exec("UPDATE pterodactyl_servers SET ptero_server_id = ?, ptero_identifier = ?, allowed_actions = ?, acl_rule = ?, node = ? WHERE server_id = ?",
		link.PteroServerID, link.PteroIdentifier, link.AllowedActions, link.ACLRule, link.Node, link.ServerID)
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

type CommandSchema struct {
	ID          int             `json:"id"`
	ServerID    int             `json:"serverId"`
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName"`
	Template    string          `json:"template"`
	Params      json.RawMessage `json:"params"`
	ACLRule     string          `json:"aclRule"`
	Enabled     bool            `json:"enabled"`
}

func (s *Store) ListCommandSchemas(serverID int) ([]CommandSchema, error) {
	rows, err := s.DB.Query("SELECT id, server_id, name, display_name, template, params, acl_rule, enabled FROM command_schemas WHERE server_id = ?", serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schemas []CommandSchema
	for rows.Next() {
		var cs CommandSchema
		var enabled int
		if err := rows.Scan(&cs.ID, &cs.ServerID, &cs.Name, &cs.DisplayName, &cs.Template, &cs.Params, &cs.ACLRule, &enabled); err != nil {
			return nil, err
		}
		cs.Enabled = enabled == 1
		schemas = append(schemas, cs)
	}
	return schemas, nil
}

func (s *Store) GetCommandSchema(id int) (*CommandSchema, error) {
	row := s.DB.QueryRow("SELECT id, server_id, name, display_name, template, params, acl_rule, enabled FROM command_schemas WHERE id = ?", id)
	var cs CommandSchema
	var enabled int
	err := row.Scan(&cs.ID, &cs.ServerID, &cs.Name, &cs.DisplayName, &cs.Template, &cs.Params, &cs.ACLRule, &enabled)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	cs.Enabled = enabled == 1
	return &cs, nil
}

func (s *Store) GetCommandSchemaByName(serverID int, name string) (*CommandSchema, error) {
	row := s.DB.QueryRow("SELECT id, server_id, name, display_name, template, params, acl_rule, enabled FROM command_schemas WHERE server_id = ? AND name = ?", serverID, name)
	var cs CommandSchema
	var enabled int
	err := row.Scan(&cs.ID, &cs.ServerID, &cs.Name, &cs.DisplayName, &cs.Template, &cs.Params, &cs.ACLRule, &enabled)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	cs.Enabled = enabled == 1
	return &cs, nil
}

func (s *Store) CreateCommandSchema(cs *CommandSchema) error {
	enabled := 0
	if cs.Enabled {
		enabled = 1
	}
	if cs.Params == nil {
		cs.Params = json.RawMessage("{}")
	}
	result, err := s.DB.Exec("INSERT INTO command_schemas (server_id, name, display_name, template, params, acl_rule, enabled) VALUES (?, ?, ?, ?, ?, ?, ?)",
		cs.ServerID, cs.Name, cs.DisplayName, cs.Template, string(cs.Params), cs.ACLRule, enabled)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	cs.ID = int(id)
	return nil
}

func (s *Store) UpdateCommandSchema(cs *CommandSchema) error {
	enabled := 0
	if cs.Enabled {
		enabled = 1
	}
	if cs.Params == nil {
		cs.Params = json.RawMessage("{}")
	}
	_, err := s.DB.Exec("UPDATE command_schemas SET name = ?, display_name = ?, template = ?, params = ?, acl_rule = ?, enabled = ? WHERE id = ?",
		cs.Name, cs.DisplayName, cs.Template, string(cs.Params), cs.ACLRule, enabled, cs.ID)
	return err
}

func (s *Store) DeleteCommandSchema(id int) error {
	_, err := s.DB.Exec("DELETE FROM command_schemas WHERE id = ?", id)
	return err
}

type ServerTag struct {
	ServerID int    `json:"serverId"`
	Tag      string `json:"tag"`
}

func (s *Store) GetServerTags(serverID int) ([]string, error) {
	rows, err := s.DB.Query("SELECT tag FROM server_tags WHERE server_id = ? ORDER BY tag", serverID)
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

func (s *Store) SetServerTags(serverID int, tags []string) error {
	_, err := s.DB.Exec("DELETE FROM server_tags WHERE server_id = ?", serverID)
	if err != nil {
		return err
	}
	if len(tags) == 0 {
		return nil
	}
	stmt, err := s.DB.Prepare("INSERT INTO server_tags (server_id, tag) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, tag := range tags {
		if _, err := stmt.Exec(serverID, tag); err != nil {
			return err
		}
	}
	return nil
}

type Constraint struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Condition   string `json:"condition"`
	Strategy    string `json:"strategy"`
	Priority    int    `json:"priority"`
	Enabled     bool   `json:"enabled"`
}

func (s *Store) ListConstraints() ([]Constraint, error) {
	rows, err := s.DB.Query("SELECT id, name, description, condition, strategy, priority, enabled FROM constraints ORDER BY priority DESC, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var constraints []Constraint
	for rows.Next() {
		var c Constraint
		var enabled int
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.Condition, &c.Strategy, &c.Priority, &enabled); err != nil {
			return nil, err
		}
		c.Enabled = enabled == 1
		constraints = append(constraints, c)
	}
	return constraints, nil
}

func (s *Store) ListEnabledConstraints() ([]Constraint, error) {
	rows, err := s.DB.Query("SELECT id, name, description, condition, strategy, priority, enabled FROM constraints WHERE enabled = 1 ORDER BY priority DESC, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var constraints []Constraint
	for rows.Next() {
		var c Constraint
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.Condition, &c.Strategy, &c.Priority, &c.Enabled); err != nil {
			return nil, err
		}
		constraints = append(constraints, c)
	}
	return constraints, nil
}

func (s *Store) GetConstraint(id int) (*Constraint, error) {
	row := s.DB.QueryRow("SELECT id, name, description, condition, strategy, priority, enabled FROM constraints WHERE id = ?", id)
	var c Constraint
	var enabled int
	err := row.Scan(&c.ID, &c.Name, &c.Description, &c.Condition, &c.Strategy, &c.Priority, &enabled)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	c.Enabled = enabled == 1
	return &c, nil
}

func (s *Store) CreateConstraint(c *Constraint) error {
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	result, err := s.DB.Exec("INSERT INTO constraints (name, description, condition, strategy, priority, enabled) VALUES (?, ?, ?, ?, ?, ?)",
		c.Name, c.Description, c.Condition, c.Strategy, c.Priority, enabled)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	c.ID = int(id)
	return nil
}

func (s *Store) UpdateConstraint(c *Constraint) error {
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	_, err := s.DB.Exec("UPDATE constraints SET name = ?, description = ?, condition = ?, strategy = ?, priority = ?, enabled = ? WHERE id = ?",
		c.Name, c.Description, c.Condition, c.Strategy, c.Priority, enabled, c.ID)
	return err
}

func (s *Store) DeleteConstraint(id int) error {
	_, err := s.DB.Exec("DELETE FROM constraints WHERE id = ?", id)
	return err
}

type CronJob struct {
	ID         int             `json:"id"`
	Name       string          `json:"name"`
	Schedule   string          `json:"schedule"`
	ServerName string          `json:"serverName"`
	Action     string          `json:"action"`
	Params     json.RawMessage `json:"params"`
	ACLRule    string          `json:"aclRule"`
	Enabled    bool            `json:"enabled"`
	LastRun    *string         `json:"lastRun"`
	NextRun    *string         `json:"nextRun"`
	LastResult string          `json:"lastResult"`
	LastOutput string          `json:"lastOutput"`
}

type CronJobLog struct {
	ID         int    `json:"id"`
	CronJobID  int    `json:"cronJobId"`
	Timestamp  string `json:"timestamp"`
	Result     string `json:"result"`
	Output     string `json:"output"`
	DurationMs int    `json:"durationMs"`
}

func (s *Store) ListCronJobs() ([]CronJob, error) {
	rows, err := s.DB.Query("SELECT id, name, schedule, server_name, action, params, acl_rule, enabled, last_run, next_run, last_result, last_output FROM cron_jobs ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []CronJob
	for rows.Next() {
		var j CronJob
		var enabled int
		if err := rows.Scan(&j.ID, &j.Name, &j.Schedule, &j.ServerName, &j.Action, &j.Params, &j.ACLRule, &enabled, &j.LastRun, &j.NextRun, &j.LastResult, &j.LastOutput); err != nil {
			return nil, err
		}
		j.Enabled = enabled == 1
		jobs = append(jobs, j)
	}
	return jobs, nil
}

func (s *Store) ListEnabledCronJobs() ([]CronJob, error) {
	rows, err := s.DB.Query("SELECT id, name, schedule, server_name, action, params, acl_rule, enabled, last_run, next_run, last_result, last_output FROM cron_jobs WHERE enabled = 1 ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []CronJob
	for rows.Next() {
		var j CronJob
		var enabled int
		if err := rows.Scan(&j.ID, &j.Name, &j.Schedule, &j.ServerName, &j.Action, &j.Params, &j.ACLRule, &enabled, &j.LastRun, &j.NextRun, &j.LastResult, &j.LastOutput); err != nil {
			return nil, err
		}
		j.Enabled = enabled == 1
		jobs = append(jobs, j)
	}
	return jobs, nil
}

func (s *Store) GetCronJob(id int) (*CronJob, error) {
	row := s.DB.QueryRow("SELECT id, name, schedule, server_name, action, params, acl_rule, enabled, last_run, next_run, last_result, last_output FROM cron_jobs WHERE id = ?", id)
	var j CronJob
	var enabled int
	err := row.Scan(&j.ID, &j.Name, &j.Schedule, &j.ServerName, &j.Action, &j.Params, &j.ACLRule, &enabled, &j.LastRun, &j.NextRun, &j.LastResult, &j.LastOutput)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	j.Enabled = enabled == 1
	return &j, nil
}

func (s *Store) CreateCronJob(j *CronJob) error {
	enabled := 0
	if j.Enabled {
		enabled = 1
	}
	if j.Params == nil {
		j.Params = json.RawMessage("{}")
	}
	result, err := s.DB.Exec("INSERT INTO cron_jobs (name, schedule, server_name, action, params, acl_rule, enabled, last_run, next_run) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		j.Name, j.Schedule, j.ServerName, j.Action, string(j.Params), j.ACLRule, enabled, j.LastRun, j.NextRun)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	j.ID = int(id)
	return nil
}

func (s *Store) UpdateCronJob(j *CronJob) error {
	enabled := 0
	if j.Enabled {
		enabled = 1
	}
	if j.Params == nil {
		j.Params = json.RawMessage("{}")
	}
	_, err := s.DB.Exec("UPDATE cron_jobs SET name = ?, schedule = ?, server_name = ?, action = ?, params = ?, acl_rule = ?, enabled = ?, last_run = ?, next_run = ? WHERE id = ?",
		j.Name, j.Schedule, j.ServerName, j.Action, string(j.Params), j.ACLRule, enabled, j.LastRun, j.NextRun, j.ID)
	return err
}

func (s *Store) DeleteCronJob(id int) error {
	_, err := s.DB.Exec("DELETE FROM cron_jobs WHERE id = ?", id)
	return err
}

func (s *Store) UpdateCronJobTimestamps(id int, lastRun, nextRun string) error {
	_, err := s.DB.Exec("UPDATE cron_jobs SET last_run = ?, next_run = ? WHERE id = ?", lastRun, nextRun, id)
	return err
}

func (s *Store) UpdateCronJobResult(id int, lastResult, lastOutput string) error {
	_, err := s.DB.Exec("UPDATE cron_jobs SET last_result = ?, last_output = ? WHERE id = ?", lastResult, lastOutput, id)
	return err
}

func (s *Store) CreateCronJobLog(log *CronJobLog) error {
	result, err := s.DB.Exec("INSERT INTO cron_job_logs (cron_job_id, timestamp, result, output, duration_ms) VALUES (?, ?, ?, ?, ?)",
		log.CronJobID, log.Timestamp, log.Result, log.Output, log.DurationMs)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	log.ID = int(id)
	return nil
}

func (s *Store) ListCronJobLogs(cronJobID, limit int) ([]CronJobLog, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.DB.Query("SELECT id, cron_job_id, timestamp, result, output, duration_ms FROM cron_job_logs WHERE cron_job_id = ? ORDER BY id DESC LIMIT ?", cronJobID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []CronJobLog
	for rows.Next() {
		var l CronJobLog
		if err := rows.Scan(&l.ID, &l.CronJobID, &l.Timestamp, &l.Result, &l.Output, &l.DurationMs); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, nil
}

type AuditLogEntry struct {
	ID         int             `json:"id"`
	Timestamp  string          `json:"timestamp"`
	UserEmail  string          `json:"userEmail"`
	ServerName string          `json:"serverName"`
	Action     string          `json:"action"`
	Params     json.RawMessage `json:"params"`
	Result     string          `json:"result"`
	Reason     string          `json:"reason"`
	Source     string          `json:"source"`
}

func (s *Store) CreateAuditLog(entry *AuditLogEntry) error {
	if entry.Params == nil {
		entry.Params = json.RawMessage("{}")
	}
	result, err := s.DB.Exec("INSERT INTO audit_log (timestamp, user_email, server_name, action, params, result, reason, source) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		entry.Timestamp, entry.UserEmail, entry.ServerName, entry.Action, string(entry.Params), entry.Result, entry.Reason, entry.Source)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	entry.ID = int(id)
	return nil
}

func (s *Store) ListAuditLog(limit, offset int) ([]AuditLogEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.DB.Query("SELECT id, timestamp, user_email, server_name, action, params, result, reason, source FROM audit_log ORDER BY id DESC LIMIT ? OFFSET ?", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []AuditLogEntry
	for rows.Next() {
		var e AuditLogEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.UserEmail, &e.ServerName, &e.Action, &e.Params, &e.Result, &e.Reason, &e.Source); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (s *Store) CleanupAuditLog(retentionDays int) error {
	if retentionDays <= 0 {
		return nil
	}
	_, err := s.DB.Exec("DELETE FROM audit_log WHERE timestamp < datetime('now', ?||' days')", fmt.Sprintf("-%d", retentionDays))
	return err
}

type ServerMetric struct {
	ID          int     `json:"id"`
	ServerName  string  `json:"serverName"`
	AgentID     int     `json:"agentId"`
	Timestamp   string  `json:"timestamp"`
	Online      bool    `json:"online"`
	Players     int     `json:"players"`
	MaxPlayers  int     `json:"maxPlayers"`
	Version     string  `json:"version"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryUsed  int64   `json:"memoryUsed"`
	MemoryTotal int64   `json:"memoryTotal"`
	DiskUsed    int64   `json:"diskUsed"`
	DiskTotal   int64   `json:"diskTotal"`
}

func (s *Store) CreateServerMetric(m *ServerMetric) error {
	online := 0
	if m.Online {
		online = 1
	}
	result, err := s.DB.Exec(
		"INSERT INTO server_metrics (server_name, agent_id, timestamp, online, players, max_players, version, cpu_percent, memory_used, memory_total, disk_used, disk_total) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		m.ServerName, m.AgentID, m.Timestamp, online, m.Players, m.MaxPlayers, m.Version, m.CPUPercent, m.MemoryUsed, m.MemoryTotal, m.DiskUsed, m.DiskTotal,
	)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	m.ID = int(id)
	return nil
}

func (s *Store) GetLatestServerMetric(serverName string) (*ServerMetric, error) {
	row := s.DB.QueryRow("SELECT id, server_name, agent_id, timestamp, online, players, max_players, version, cpu_percent, memory_used, memory_total, disk_used, disk_total FROM server_metrics WHERE server_name = ? ORDER BY timestamp DESC LIMIT 1", serverName)
	var m ServerMetric
	var online int
	err := row.Scan(&m.ID, &m.ServerName, &m.AgentID, &m.Timestamp, &online, &m.Players, &m.MaxPlayers, &m.Version, &m.CPUPercent, &m.MemoryUsed, &m.MemoryTotal, &m.DiskUsed, &m.DiskTotal)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	m.Online = online == 1
	return &m, nil
}

func (s *Store) ListServerMetrics(serverName string, limit int) ([]ServerMetric, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.DB.Query("SELECT id, server_name, agent_id, timestamp, online, players, max_players, version, cpu_percent, memory_used, memory_total, disk_used, disk_total FROM server_metrics WHERE server_name = ? ORDER BY timestamp DESC LIMIT ?", serverName, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var metrics []ServerMetric
	for rows.Next() {
		var m ServerMetric
		var online int
		if err := rows.Scan(&m.ID, &m.ServerName, &m.AgentID, &m.Timestamp, &online, &m.Players, &m.MaxPlayers, &m.Version, &m.CPUPercent, &m.MemoryUsed, &m.MemoryTotal, &m.DiskUsed, &m.DiskTotal); err != nil {
			return nil, err
		}
		m.Online = online == 1
		metrics = append(metrics, m)
	}
	return metrics, nil
}

func (s *Store) CleanupServerMetrics(retentionDays int) error {
	if retentionDays <= 0 {
		return nil
	}
	_, err := s.DB.Exec("DELETE FROM server_metrics WHERE timestamp < datetime('now', ?||' days')", fmt.Sprintf("-%d", retentionDays))
	return err
}

type APIKey struct {
	ID        int     `json:"id"`
	Name      string  `json:"name"`
	KeyHash   string  `json:"-"`
	KeyPrefix string  `json:"keyPrefix"`
	Role      string  `json:"role"`
	CreatedAt string  `json:"createdAt"`
	LastUsed  *string `json:"lastUsed"`
	ExpiresAt *string `json:"expiresAt"`
}

func (s *Store) CreateAPIKey(key *APIKey) error {
	result, err := s.DB.Exec("INSERT INTO api_keys (name, key_hash, key_prefix, role, created_at, last_used, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		key.Name, key.KeyHash, key.KeyPrefix, key.Role, key.CreatedAt, key.LastUsed, key.ExpiresAt)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	key.ID = int(id)
	return nil
}

func (s *Store) GetAPIKeyByHash(keyHash string) (*APIKey, error) {
	row := s.DB.QueryRow("SELECT id, name, key_hash, key_prefix, role, created_at, last_used, expires_at FROM api_keys WHERE key_hash = ?", keyHash)
	var k APIKey
	err := row.Scan(&k.ID, &k.Name, &k.KeyHash, &k.KeyPrefix, &k.Role, &k.CreatedAt, &k.LastUsed, &k.ExpiresAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &k, nil
}

func (s *Store) ListAPIKeys() ([]APIKey, error) {
	rows, err := s.DB.Query("SELECT id, name, key_hash, key_prefix, role, created_at, last_used, expires_at FROM api_keys ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []APIKey
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.KeyHash, &k.KeyPrefix, &k.Role, &k.CreatedAt, &k.LastUsed, &k.ExpiresAt); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

func (s *Store) DeleteAPIKey(id int) error {
	_, err := s.DB.Exec("DELETE FROM api_keys WHERE id = ?", id)
	return err
}

func (s *Store) UpdateAPIKeyLastUsed(id int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.DB.Exec("UPDATE api_keys SET last_used = ? WHERE id = ?", now, id)
	return err
}

func HashAPIKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

type ServerTemplate struct {
	ID              int             `json:"id"`
	Name            string          `json:"name"`
	GameType        string          `json:"gameType"`
	DefaultSettings json.RawMessage `json:"defaultSettings"`
	DefaultCommands json.RawMessage `json:"defaultCommands"`
	DefaultACL      string          `json:"defaultAcl"`
	DefaultTags     json.RawMessage `json:"defaultTags"`
	Description     string          `json:"description"`
}

func (s *Store) ListServerTemplates() ([]ServerTemplate, error) {
	rows, err := s.DB.Query("SELECT id, name, game_type, default_settings, default_commands, default_acl, default_tags, description FROM server_templates ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var templates []ServerTemplate
	for rows.Next() {
		var t ServerTemplate
		var settings, commands, tags []byte
		if err := rows.Scan(&t.ID, &t.Name, &t.GameType, &settings, &commands, &t.DefaultACL, &tags, &t.Description); err != nil {
			return nil, err
		}
		t.DefaultSettings = json.RawMessage(settings)
		t.DefaultCommands = json.RawMessage(commands)
		t.DefaultTags = json.RawMessage(tags)
		templates = append(templates, t)
	}
	return templates, nil
}

func (s *Store) GetServerTemplate(id int) (*ServerTemplate, error) {
	row := s.DB.QueryRow("SELECT id, name, game_type, default_settings, default_commands, default_acl, default_tags, description FROM server_templates WHERE id = ?", id)
	var t ServerTemplate
	var settings, commands, tags []byte
	err := row.Scan(&t.ID, &t.Name, &t.GameType, &settings, &commands, &t.DefaultACL, &tags, &t.Description)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	t.DefaultSettings = json.RawMessage(settings)
	t.DefaultCommands = json.RawMessage(commands)
	t.DefaultTags = json.RawMessage(tags)
	return &t, nil
}

func (s *Store) GetServerTemplateByName(name string) (*ServerTemplate, error) {
	row := s.DB.QueryRow("SELECT id, name, game_type, default_settings, default_commands, default_acl, default_tags, description FROM server_templates WHERE name = ?", name)
	var t ServerTemplate
	var settings, commands, tags []byte
	err := row.Scan(&t.ID, &t.Name, &t.GameType, &settings, &commands, &t.DefaultACL, &t.DefaultTags, &t.Description)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	t.DefaultSettings = json.RawMessage(settings)
	t.DefaultCommands = json.RawMessage(commands)
	t.DefaultTags = json.RawMessage(tags)
	return &t, nil
}

func (s *Store) CreateServerTemplate(t *ServerTemplate) error {
	if t.DefaultSettings == nil {
		t.DefaultSettings = json.RawMessage("{}")
	}
	if t.DefaultCommands == nil {
		t.DefaultCommands = json.RawMessage("[]")
	}
	if t.DefaultTags == nil {
		t.DefaultTags = json.RawMessage("[]")
	}
	result, err := s.DB.Exec("INSERT INTO server_templates (name, game_type, default_settings, default_commands, default_acl, default_tags, description) VALUES (?, ?, ?, ?, ?, ?, ?)",
		t.Name, t.GameType, string(t.DefaultSettings), string(t.DefaultCommands), t.DefaultACL, string(t.DefaultTags), t.Description)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	t.ID = int(id)
	return nil
}

func (s *Store) DeleteServerTemplate(id int) error {
	_, err := s.DB.Exec("DELETE FROM server_templates WHERE id = ?", id)
	return err
}

type Webhook struct {
	ID        int             `json:"id"`
	Name      string          `json:"name"`
	URL       string          `json:"url"`
	Secret    string          `json:"-"`
	Events    json.RawMessage `json:"events"`
	Enabled   bool            `json:"enabled"`
	CreatedAt string          `json:"createdAt"`
}

func (s *Store) ListWebhooks() ([]Webhook, error) {
	rows, err := s.DB.Query("SELECT id, name, url, secret, events, enabled, created_at FROM webhooks ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var webhooks []Webhook
	for rows.Next() {
		var w Webhook
		var enabled int
		var events []byte
		if err := rows.Scan(&w.ID, &w.Name, &w.URL, &w.Secret, &events, &enabled, &w.CreatedAt); err != nil {
			return nil, err
		}
		w.Events = json.RawMessage(events)
		w.Enabled = enabled == 1
		webhooks = append(webhooks, w)
	}
	return webhooks, nil
}

func (s *Store) GetWebhook(id int) (*Webhook, error) {
	row := s.DB.QueryRow("SELECT id, name, url, secret, events, enabled, created_at FROM webhooks WHERE id = ?", id)
	var w Webhook
	var enabled int
	var events []byte
	err := row.Scan(&w.ID, &w.Name, &w.URL, &w.Secret, &events, &enabled, &w.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	w.Events = json.RawMessage(events)
	w.Enabled = enabled == 1
	return &w, nil
}

func (s *Store) CreateWebhook(w *Webhook) error {
	if w.Events == nil {
		w.Events = json.RawMessage("[]")
	}
	enabled := 0
	if w.Enabled {
		enabled = 1
	}
	result, err := s.DB.Exec("INSERT INTO webhooks (name, url, secret, events, enabled) VALUES (?, ?, ?, ?, ?)",
		w.Name, w.URL, w.Secret, string(w.Events), enabled)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	w.ID = int(id)
	return nil
}

func (s *Store) DeleteWebhook(id int) error {
	_, err := s.DB.Exec("DELETE FROM webhooks WHERE id = ?", id)
	return err
}

type NotificationChannel struct {
	ID        int             `json:"id"`
	Name      string          `json:"name"`
	Type      string          `json:"type"`
	URL       string          `json:"url"`
	Events    json.RawMessage `json:"events"`
	Enabled   bool            `json:"enabled"`
	CreatedAt string          `json:"createdAt"`
}

func (s *Store) ListNotificationChannels() ([]NotificationChannel, error) {
	rows, err := s.DB.Query("SELECT id, name, type, url, events, enabled, created_at FROM notification_channels ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []NotificationChannel
	for rows.Next() {
		var ch NotificationChannel
		var enabled int
		var events []byte
		if err := rows.Scan(&ch.ID, &ch.Name, &ch.Type, &ch.URL, &events, &enabled, &ch.CreatedAt); err != nil {
			return nil, err
		}
		ch.Events = json.RawMessage(events)
		ch.Enabled = enabled == 1
		channels = append(channels, ch)
	}
	return channels, nil
}

func (s *Store) CreateNotificationChannel(ch *NotificationChannel) error {
	if ch.Events == nil {
		ch.Events = json.RawMessage("[]")
	}
	enabled := 0
	if ch.Enabled {
		enabled = 1
	}
	result, err := s.DB.Exec("INSERT INTO notification_channels (name, type, url, events, enabled) VALUES (?, ?, ?, ?, ?)",
		ch.Name, ch.Type, ch.URL, string(ch.Events), enabled)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	ch.ID = int(id)
	return nil
}

func (s *Store) DeleteNotificationChannel(id int) error {
	_, err := s.DB.Exec("DELETE FROM notification_channels WHERE id = ?", id)
	return err
}

func (bg *Background) URL() string {
	if bg.ContentHash != "" {
		return "/backgrounds/" + bg.ContentHash + "/" + bg.Filename
	}
	return "/backgrounds/" + bg.Filename
}

type Session struct {
	ID        int    `json:"id"`
	SessionID string `json:"sessionId"`
	UserSub   string `json:"userSub"`
	UserEmail string `json:"userEmail"`
	UserRole  string `json:"userRole"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
}

func (s *Store) CreateSession(session *Session) error {
	result, err := s.DB.Exec("INSERT INTO sessions (session_id, user_sub, user_email, user_role, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)",
		session.SessionID, session.UserSub, session.UserEmail, session.UserRole, session.CreatedAt, session.ExpiresAt)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	session.ID = int(id)
	return nil
}

func (s *Store) GetSession(sessionID string) (*Session, error) {
	row := s.DB.QueryRow("SELECT id, session_id, user_sub, user_email, user_role, created_at, expires_at FROM sessions WHERE session_id = ?", sessionID)
	var session Session
	err := row.Scan(&session.ID, &session.SessionID, &session.UserSub, &session.UserEmail, &session.UserRole, &session.CreatedAt, &session.ExpiresAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &session, nil
}

func (s *Store) DeleteSession(sessionID string) error {
	_, err := s.DB.Exec("DELETE FROM sessions WHERE session_id = ?", sessionID)
	return err
}

func (s *Store) DeleteSessionsBySub(userSub string) error {
	_, err := s.DB.Exec("DELETE FROM sessions WHERE user_sub = ?", userSub)
	return err
}

func (s *Store) CleanupExpiredSessions() error {
	_, err := s.DB.Exec("DELETE FROM sessions WHERE expires_at < datetime('now')")
	return err
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

func (s *Store) GetUserByID(id int) (*User, error) {
	row := s.DB.QueryRow("SELECT id, email, role, first_seen, last_login, external_id, display_name, active FROM users WHERE id = ?", id)
	var u User
	var active int
	err := row.Scan(&u.ID, &u.Email, &u.Role, &u.FirstSeen, &u.LastLogin, &u.ExternalID, &u.DisplayName, &active)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	u.Active = active == 1
	return &u, nil
}

func (s *Store) GetUserByExternalID(externalID string) (*User, error) {
	row := s.DB.QueryRow("SELECT id, email, role, first_seen, last_login, external_id, display_name, active FROM users WHERE external_id = ?", externalID)
	var u User
	var active int
	err := row.Scan(&u.ID, &u.Email, &u.Role, &u.FirstSeen, &u.LastLogin, &u.ExternalID, &u.DisplayName, &active)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	u.Active = active == 1
	return &u, nil
}

func (s *Store) UpdateUserSCIM(id int, externalID, displayName string, active bool) error {
	activeInt := 0
	if active {
		activeInt = 1
	}
	_, err := s.DB.Exec("UPDATE users SET external_id = ?, display_name = ?, active = ? WHERE id = ?", externalID, displayName, activeInt, id)
	return err
}

func (s *Store) SetUserActive(id int, active bool) error {
	activeInt := 0
	if active {
		activeInt = 1
	}
	_, err := s.DB.Exec("UPDATE users SET active = ? WHERE id = ?", activeInt, id)
	return err
}

type SCIMGroup struct {
	ID          int    `json:"id"`
	ExternalID  string `json:"externalId"`
	DisplayName string `json:"displayName"`
	CreatedAt   string `json:"createdAt"`
}

func (s *Store) CreateSCIMGroup(g *SCIMGroup) error {
	result, err := s.DB.Exec("INSERT INTO scim_groups (external_id, display_name) VALUES (?, ?)", g.ExternalID, g.DisplayName)
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	g.ID = int(id)
	return nil
}

func (s *Store) GetSCIMGroup(id int) (*SCIMGroup, error) {
	row := s.DB.QueryRow("SELECT id, external_id, display_name, created_at FROM scim_groups WHERE id = ?", id)
	var g SCIMGroup
	err := row.Scan(&g.ID, &g.ExternalID, &g.DisplayName, &g.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &g, nil
}

func (s *Store) GetSCIMGroupByName(displayName string) (*SCIMGroup, error) {
	row := s.DB.QueryRow("SELECT id, external_id, display_name, created_at FROM scim_groups WHERE display_name = ?", displayName)
	var g SCIMGroup
	err := row.Scan(&g.ID, &g.ExternalID, &g.DisplayName, &g.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &g, nil
}

func (s *Store) ListSCIMGroups() ([]SCIMGroup, error) {
	rows, err := s.DB.Query("SELECT id, external_id, display_name, created_at FROM scim_groups ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []SCIMGroup
	for rows.Next() {
		var g SCIMGroup
		if err := rows.Scan(&g.ID, &g.ExternalID, &g.DisplayName, &g.CreatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

func (s *Store) UpdateSCIMGroup(id int, externalID, displayName string) error {
	_, err := s.DB.Exec("UPDATE scim_groups SET external_id = ?, display_name = ? WHERE id = ?", externalID, displayName, id)
	return err
}

func (s *Store) DeleteSCIMGroup(id int) error {
	_, err := s.DB.Exec("DELETE FROM scim_groups WHERE id = ?", id)
	return err
}

func (s *Store) SetSCIMGroupMembers(groupID int, userIDs []int) error {
	_, err := s.DB.Exec("DELETE FROM scim_group_members WHERE group_id = ?", groupID)
	if err != nil {
		return err
	}
	if len(userIDs) == 0 {
		return nil
	}
	stmt, err := s.DB.Prepare("INSERT INTO scim_group_members (group_id, user_id) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, uid := range userIDs {
		if _, err := stmt.Exec(groupID, uid); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AddSCIMGroupMember(groupID, userID int) error {
	_, err := s.DB.Exec("INSERT OR IGNORE INTO scim_group_members (group_id, user_id) VALUES (?, ?)", groupID, userID)
	return err
}

func (s *Store) RemoveSCIMGroupMember(groupID, userID int) error {
	_, err := s.DB.Exec("DELETE FROM scim_group_members WHERE group_id = ? AND user_id = ?", groupID, userID)
	return err
}

func (s *Store) GetSCIMGroupMembers(groupID int) ([]User, error) {
	rows, err := s.DB.Query(`SELECT u.id, u.email, u.role, u.first_seen, u.last_login, u.external_id, u.display_name, u.active
		FROM users u JOIN scim_group_members gm ON u.id = gm.user_id WHERE gm.group_id = ? ORDER BY u.id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var active int
		if err := rows.Scan(&u.ID, &u.Email, &u.Role, &u.FirstSeen, &u.LastLogin, &u.ExternalID, &u.DisplayName, &active); err != nil {
			return nil, err
		}
		u.Active = active == 1
		users = append(users, u)
	}
	return users, nil
}

func (s *Store) GetSCIMGroupsForUser(userID int) ([]SCIMGroup, error) {
	rows, err := s.DB.Query(`SELECT g.id, g.external_id, g.display_name, g.created_at
		FROM scim_groups g JOIN scim_group_members gm ON g.id = gm.group_id WHERE gm.user_id = ? ORDER BY g.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var groups []SCIMGroup
	for rows.Next() {
		var g SCIMGroup
		if err := rows.Scan(&g.ID, &g.ExternalID, &g.DisplayName, &g.CreatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, nil
}

type Agent struct {
	ID           int             `json:"id"`
	Name         string          `json:"name"`
	Token        string          `json:"token,omitempty"`
	TokenHash    string          `json:"-"`
	TokenPrefix  string          `json:"keyPrefix"`
	NodeName     string          `json:"nodeName"`
	Capabilities json.RawMessage `json:"capabilities"`
	CreatedAt    string          `json:"createdAt"`
	LastSeen     *string         `json:"lastSeen"`
	Online       bool            `json:"online"`
}

func (s *Store) CreateAgent(a *Agent) error {
	if a.Capabilities == nil {
		a.Capabilities = json.RawMessage("[]")
	}
	if a.TokenHash == "" && a.Token != "" {
		a.TokenHash = HashAPIKey(a.Token)
		a.TokenPrefix = a.Token[:8]
	}
	result, err := s.DB.Exec("INSERT INTO agents (name, token, token_hash, token_prefix, node_name, capabilities) VALUES (?, ?, ?, ?, ?, ?)",
		a.Name, a.Token, a.TokenHash, a.TokenPrefix, a.NodeName, string(a.Capabilities))
	if err != nil {
		return err
	}
	id, _ := result.LastInsertId()
	a.ID = int(id)
	return nil
}

func (s *Store) GetAgent(id int) (*Agent, error) {
	row := s.DB.QueryRow("SELECT id, name, token_prefix, node_name, capabilities, created_at, last_seen, online FROM agents WHERE id = ?", id)
	return scanAgent(row)
}

func (s *Store) GetAgentByToken(token string) (*Agent, error) {
	tokenHash := HashAPIKey(token)
	row := s.DB.QueryRow("SELECT id, name, token_prefix, node_name, capabilities, created_at, last_seen, online FROM agents WHERE token_hash = ?", tokenHash)
	return scanAgent(row)
}

func (s *Store) GetAgentByNodeName(nodeName string) (*Agent, error) {
	row := s.DB.QueryRow("SELECT id, name, token_prefix, node_name, capabilities, created_at, last_seen, online FROM agents WHERE node_name = ?", nodeName)
	return scanAgent(row)
}

func scanAgent(row *sql.Row) (*Agent, error) {
	var a Agent
	var online int
	var caps []byte
	var prefix sql.NullString
	err := row.Scan(&a.ID, &a.Name, &prefix, &a.NodeName, &caps, &a.CreatedAt, &a.LastSeen, &online)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	a.Capabilities = json.RawMessage(caps)
	a.TokenPrefix = prefix.String
	a.Online = online == 1
	return &a, nil
}

func (s *Store) ListAgents() ([]Agent, error) {
	rows, err := s.DB.Query("SELECT id, name, token_prefix, node_name, capabilities, created_at, last_seen, online FROM agents ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []Agent
	for rows.Next() {
		var a Agent
		var online int
		var caps []byte
		var prefix sql.NullString
		if err := rows.Scan(&a.ID, &a.Name, &prefix, &a.NodeName, &caps, &a.CreatedAt, &a.LastSeen, &online); err != nil {
			return nil, err
		}
		a.TokenPrefix = prefix.String
		a.Capabilities = json.RawMessage(caps)
		a.Online = online == 1
		agents = append(agents, a)
	}
	return agents, nil
}

func (s *Store) UpdateAgentOnline(id int, online bool) error {
	onlineInt := 0
	if online {
		onlineInt = 1
	}
	_, err := s.DB.Exec("UPDATE agents SET online = ?, last_seen = CURRENT_TIMESTAMP WHERE id = ?", onlineInt, id)
	return err
}

func (s *Store) UpdateAgentCapabilities(id int, capabilities json.RawMessage) error {
	_, err := s.DB.Exec("UPDATE agents SET capabilities = ? WHERE id = ?", string(capabilities), id)
	return err
}

func (s *Store) UpdateAgent(a *Agent) error {
	if a.Capabilities == nil {
		a.Capabilities = json.RawMessage("[]")
	}
	_, err := s.DB.Exec("UPDATE agents SET name = ?, node_name = ?, capabilities = ? WHERE id = ?",
		a.Name, a.NodeName, string(a.Capabilities), a.ID)
	return err
}

func (s *Store) DeleteAgent(id int) error {
	_, err := s.DB.Exec("DELETE FROM agents WHERE id = ?", id)
	return err
}
