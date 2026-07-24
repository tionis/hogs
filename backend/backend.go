package backend

import (
	"context"
	"errors"
)

type ActionResult struct {
	Success bool
	Message string
}

type ServerStatus struct {
	Online     bool
	Players    int
	MaxPlayers int
	Version    string
}

type Backend interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	SendCommand(ctx context.Context, command string) error
	Status(ctx context.Context) (*ServerStatus, error)
	Name() string
}

type WhitelistEntry struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

type WhitelistRequest struct {
	Operation        string `json:"operation"`
	Username         string `json:"username,omitempty"`
	PreviousUsername string `json:"previousUsername,omitempty"`
	ExternalID       string `json:"externalId,omitempty"`
}

type WhitelistResult struct {
	Mode    string           `json:"mode"`
	Output  string           `json:"output,omitempty"`
	Entries []WhitelistEntry `json:"entries"`
}

type WhitelistBackend interface {
	Whitelist(context.Context, WhitelistRequest) (*WhitelistResult, error)
}

type WhitelistError struct {
	Code    string
	Message string
}

func (e *WhitelistError) Error() string { return e.Message }

func IsWhitelistError(err error, code string) bool {
	var target *WhitelistError
	return errors.As(err, &target) && target.Code == code
}

func BackendForServer(nodeName string, backends map[string]Backend, defaultBackend Backend) Backend {
	if nodeName == "" {
		return defaultBackend
	}
	if b, ok := backends[nodeName]; ok {
		return b
	}
	return defaultBackend
}
