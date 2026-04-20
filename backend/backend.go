package backend

import "context"

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

func BackendForServer(nodeName string, backends map[string]Backend, defaultBackend Backend) Backend {
	if nodeName == "" {
		return defaultBackend
	}
	if b, ok := backends[nodeName]; ok {
		return b
	}
	return defaultBackend
}
