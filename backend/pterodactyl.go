package backend

import (
	"context"
	"fmt"

	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/pterodactyl"
)

type PterodactylBackend struct {
	Client        *pterodactyl.Client
	Identifier    string
	PteroServerID string
}

func NewPterodactylBackend(cfg *config.Config, pteroServerID, identifier string) *PterodactylBackend {
	client := pterodactyl.NewClient(cfg.PterodactylURL, cfg.PterodactylAppKey)
	client.ClientKey = cfg.PterodactylClientKey
	return &PterodactylBackend{
		Client:        client,
		Identifier:    identifier,
		PteroServerID: pteroServerID,
	}
}

func (p *PterodactylBackend) Start(ctx context.Context) error {
	if p.Client.ClientKey == "" {
		return fmt.Errorf("pterodactyl client key not configured")
	}
	identifier := p.Identifier
	if identifier == "" {
		srv, err := p.Client.GetServer(p.PteroServerID)
		if err != nil {
			return fmt.Errorf("failed to resolve identifier: %w", err)
		}
		identifier = srv.Identifier
	}
	return p.Client.StartServer(identifier)
}

func (p *PterodactylBackend) Stop(ctx context.Context) error {
	if p.Client.ClientKey == "" {
		return fmt.Errorf("pterodactyl client key not configured")
	}
	identifier := p.Identifier
	if identifier == "" {
		srv, err := p.Client.GetServer(p.PteroServerID)
		if err != nil {
			return fmt.Errorf("failed to resolve identifier: %w", err)
		}
		identifier = srv.Identifier
	}
	return p.Client.StopServer(identifier)
}

func (p *PterodactylBackend) Restart(ctx context.Context) error {
	if p.Client.ClientKey == "" {
		return fmt.Errorf("pterodactyl client key not configured")
	}
	identifier := p.Identifier
	if identifier == "" {
		srv, err := p.Client.GetServer(p.PteroServerID)
		if err != nil {
			return fmt.Errorf("failed to resolve identifier: %w", err)
		}
		identifier = srv.Identifier
	}
	return p.Client.RestartServer(identifier)
}

func (p *PterodactylBackend) SendCommand(ctx context.Context, command string) error {
	if p.Client.ClientKey == "" {
		return fmt.Errorf("pterodactyl client key not configured")
	}
	identifier := p.Identifier
	if identifier == "" {
		srv, err := p.Client.GetServer(p.PteroServerID)
		if err != nil {
			return fmt.Errorf("failed to resolve identifier: %w", err)
		}
		identifier = srv.Identifier
	}
	return p.Client.SendCommand(identifier, command)
}

func (p *PterodactylBackend) Status(ctx context.Context) (*ServerStatus, error) {
	return nil, fmt.Errorf("status not supported via pterodactyl backend")
}

func (p *PterodactylBackend) Name() string {
	return "pterodactyl"
}
