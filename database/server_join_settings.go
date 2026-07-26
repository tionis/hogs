package database

import (
	"database/sql"
	"fmt"
	"strings"
)

const (
	JoinEnforcementAuto      = "auto"
	JoinEnforcementWhitelist = "whitelist"
	JoinEnforcementPassword  = "password"
)

func NormalizeJoinEnforcementMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = JoinEnforcementAuto
	}
	switch mode {
	case JoinEnforcementAuto, JoinEnforcementWhitelist, JoinEnforcementPassword:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid join enforcement mode %q", mode)
	}
}

func (s *Store) GetServerJoinEnforcementMode(serverID int) (string, error) {
	var mode string
	err := s.DB.QueryRow(
		"SELECT enforcement_mode FROM server_join_settings WHERE server_id=?",
		serverID,
	).Scan(&mode)
	if err != nil {
		if err == sql.ErrNoRows {
			return JoinEnforcementAuto, nil
		}
		return "", err
	}
	return NormalizeJoinEnforcementMode(mode)
}

func (s *Store) SetServerJoinEnforcementMode(serverID int, mode string) error {
	normalized, err := NormalizeJoinEnforcementMode(mode)
	if err != nil {
		return err
	}
	if normalized == JoinEnforcementAuto {
		_, err = s.DB.Exec("DELETE FROM server_join_settings WHERE server_id=?", serverID)
		return err
	}
	_, err = s.DB.Exec(`INSERT INTO server_join_settings(server_id,enforcement_mode)
		VALUES(?,?) ON CONFLICT(server_id) DO UPDATE SET enforcement_mode=excluded.enforcement_mode`,
		serverID, normalized)
	return err
}

func JoinWhitelistEnabled(mode string, driverSupportsWhitelist bool) bool {
	switch mode {
	case JoinEnforcementWhitelist:
		return driverSupportsWhitelist
	case JoinEnforcementPassword:
		return false
	default:
		return driverSupportsWhitelist
	}
}
