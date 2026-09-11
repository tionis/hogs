package database

import "database/sql"

// UserAdmin tracks which game identity HOGS granted in-game administrator
// rights for a user on a server. It mirrors user_whitelists so the admin
// reconciler can distinguish HOGS-owned entries from manual ones.
type UserAdmin struct {
	ID           int    `json:"id"`
	UserUsername string `json:"userUsername"`
	ServerID     int    `json:"serverId"`
	Username     string `json:"username"`
}

func (s *Store) ListUserAdmins(serverID int) ([]UserAdmin, error) {
	rows, err := s.DB.Query(`SELECT id,user_username,server_id,username
		FROM user_admins WHERE server_id=? ORDER BY lower(username),lower(user_username)`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []UserAdmin
	for rows.Next() {
		var entry UserAdmin
		if err := rows.Scan(&entry.ID, &entry.UserUsername, &entry.ServerID, &entry.Username); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) GetUserAdmin(username string, serverID int) (*UserAdmin, error) {
	row := s.DB.QueryRow("SELECT id, user_username, server_id, username FROM user_admins WHERE user_username = ? AND server_id = ?", username, serverID)
	var admin UserAdmin
	err := row.Scan(&admin.ID, &admin.UserUsername, &admin.ServerID, &admin.Username)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &admin, nil
}

func (s *Store) SetUserAdminForIdentity(panelUsername string, serverID int, gameUsername string, caseSensitive bool) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	deleteQuery := `DELETE FROM user_admins
		WHERE server_id=? AND lower(username)=lower(?) AND lower(user_username)<>lower(?)`
	if caseSensitive {
		deleteQuery = `DELETE FROM user_admins
			WHERE server_id=? AND username=? AND lower(user_username)<>lower(?)`
	}
	if _, err = tx.Exec(deleteQuery, serverID, gameUsername, panelUsername); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO user_admins (user_username, server_id, username)
		VALUES (?, ?, ?) ON CONFLICT(user_username, server_id) DO UPDATE SET username=excluded.username`,
		panelUsername, serverID, gameUsername); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteUserAdmin(username string, serverID int) error {
	_, err := s.DB.Exec("DELETE FROM user_admins WHERE user_username = ? AND server_id = ?", username, serverID)
	return err
}

func (s *Store) DeleteUserAdminsByIdentity(serverID int, username string, caseSensitive bool) error {
	query := "DELETE FROM user_admins WHERE server_id=? AND lower(username)=lower(?)"
	if caseSensitive {
		query = "DELETE FROM user_admins WHERE server_id=? AND username=?"
	}
	_, err := s.DB.Exec(query, serverID, username)
	return err
}
