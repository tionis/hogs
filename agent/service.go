package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/tionis/hogs/database"
)

type BackendServerStatus struct {
	Online     bool
	Players    int
	MaxPlayers int
	Version    string
}

type AgentBackend struct {
	AgentID  int
	NodeName string
	Hub      *Hub
}

func NewAgentBackend(agentID int, nodeName string, hub *Hub) *AgentBackend {
	return &AgentBackend{AgentID: agentID, NodeName: nodeName, Hub: hub}
}

func (a *AgentBackend) Start(ctx context.Context) error {
	ok, msg := a.Hub.SendAction(a.AgentID, "start")
	if !ok {
		return fmt.Errorf("failed to send start: %s", msg)
	}
	return nil
}

func (a *AgentBackend) Stop(ctx context.Context) error {
	ok, msg := a.Hub.SendAction(a.AgentID, "stop")
	if !ok {
		return fmt.Errorf("failed to send stop: %s", msg)
	}
	return nil
}

func (a *AgentBackend) Restart(ctx context.Context) error {
	ok, msg := a.Hub.SendAction(a.AgentID, "restart")
	if !ok {
		return fmt.Errorf("failed to send restart: %s", msg)
	}
	return nil
}

func (a *AgentBackend) SendCommand(ctx context.Context, command string) error {
	ok, msg := a.Hub.SendCommand(a.AgentID, command)
	if !ok {
		return fmt.Errorf("failed to send command: %s", msg)
	}
	return nil
}

func (a *AgentBackend) Status(ctx context.Context) (*BackendServerStatus, error) {
	return nil, fmt.Errorf("agent status not yet implemented")
}

func (a *AgentBackend) Name() string {
	return "agent"
}

func ResolveBackend(serverName string, store *database.Store, hub *Hub) (string, int) {
	server, err := store.GetServerByName(serverName)
	if err != nil || server == nil {
		return "", 0
	}

	link, err := store.GetPterodactylLink(server.ID)
	if err != nil || link == nil {
		return "", 0
	}

	if link.Node == "" {
		return "pterodactyl", 0
	}

	agent, err := store.GetAgentByNodeName(link.Node)
	if err != nil || agent == nil {
		return "pterodactyl", 0
	}

	return "agent", agent.ID
}

type AgentService struct {
	Store *database.Store
	Hub   *Hub
}

func NewAgentService(store *database.Store, hub *Hub) *AgentService {
	return &AgentService{Store: store, Hub: hub}
}

func (s *AgentService) ExecuteAction(serverName, action string) error {
	backendType, agentID := ResolveBackend(serverName, s.Store, s.Hub)

	if backendType == "agent" && agentID > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		ab := NewAgentBackend(agentID, "", s.Hub)
		switch action {
		case "start":
			return ab.Start(ctx)
		case "stop":
			return ab.Stop(ctx)
		case "restart":
			return ab.Restart(ctx)
		default:
			return fmt.Errorf("unknown action: %s", action)
		}
	}

	return fmt.Errorf("no agent backend available for server %s", serverName)
}

func (s *AgentService) SendCommand(serverName, command string) error {
	backendType, agentID := ResolveBackend(serverName, s.Store, s.Hub)

	if backendType == "agent" && agentID > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		ab := NewAgentBackend(agentID, "", s.Hub)
		return ab.SendCommand(ctx, command)
	}

	return fmt.Errorf("no agent backend available for server %s", serverName)
}
