package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/tionis/hogs/database"
)

type AgentBackend struct {
	AgentID  int
	NodeName string
	Hub      *Hub
}

func NewAgentBackend(agentID int, nodeName string, hub *Hub) *AgentBackend {
	return &AgentBackend{AgentID: agentID, NodeName: nodeName, Hub: hub}
}

func (a *AgentBackend) Start(ctx context.Context) error {
	_, err := a.Hub.SendAction(ctx, a.AgentID, "start")
	if err != nil {
		return fmt.Errorf("failed to send start: %w", err)
	}
	return nil
}

func (a *AgentBackend) Stop(ctx context.Context) error {
	_, err := a.Hub.SendAction(ctx, a.AgentID, "stop")
	if err != nil {
		return fmt.Errorf("failed to send stop: %w", err)
	}
	return nil
}

func (a *AgentBackend) Restart(ctx context.Context) error {
	_, err := a.Hub.SendAction(ctx, a.AgentID, "restart")
	if err != nil {
		return fmt.Errorf("failed to send restart: %w", err)
	}
	return nil
}

func (a *AgentBackend) SendCommand(ctx context.Context, command string) error {
	_, err := a.Hub.SendCommand(ctx, a.AgentID, command)
	if err != nil {
		return fmt.Errorf("failed to send command: %w", err)
	}
	return nil
}

func (a *AgentBackend) Status(ctx context.Context) (*GenericResultData, error) {
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

func (s *AgentService) FileList(serverName, path string) (*GenericResultData, error) {
	_, agentID := ResolveBackend(serverName, s.Store, s.Hub)
	if agentID <= 0 {
		return nil, fmt.Errorf("no agent backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.Hub.SendFileList(ctx, agentID, path)
}

func (s *AgentService) FileRead(serverName, path string) (*GenericResultData, error) {
	_, agentID := ResolveBackend(serverName, s.Store, s.Hub)
	if agentID <= 0 {
		return nil, fmt.Errorf("no agent backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.Hub.SendFileRead(ctx, agentID, path)
}

func (s *AgentService) FileWrite(serverName, path, content string) (*GenericResultData, error) {
	_, agentID := ResolveBackend(serverName, s.Store, s.Hub)
	if agentID <= 0 {
		return nil, fmt.Errorf("no agent backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.Hub.SendFileWrite(ctx, agentID, path, content)
}

func (s *AgentService) FileDelete(serverName, path string) (*GenericResultData, error) {
	_, agentID := ResolveBackend(serverName, s.Store, s.Hub)
	if agentID <= 0 {
		return nil, fmt.Errorf("no agent backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.Hub.SendFileDelete(ctx, agentID, path)
}

func (s *AgentService) Mkdir(serverName, path string) (*GenericResultData, error) {
	_, agentID := ResolveBackend(serverName, s.Store, s.Hub)
	if agentID <= 0 {
		return nil, fmt.Errorf("no agent backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.Hub.SendMkdir(ctx, agentID, path)
}

func (s *AgentService) BackupCreate(serverName, repo, password string, paths, tags []string) (*GenericResultData, error) {
	_, agentID := ResolveBackend(serverName, s.Store, s.Hub)
	if agentID <= 0 {
		return nil, fmt.Errorf("no agent backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return s.Hub.SendBackupCreate(ctx, agentID, repo, password, paths, tags)
}

func (s *AgentService) BackupRestore(serverName, repo, password, snapshot, target string) (*GenericResultData, error) {
	_, agentID := ResolveBackend(serverName, s.Store, s.Hub)
	if agentID <= 0 {
		return nil, fmt.Errorf("no agent backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return s.Hub.SendBackupRestore(ctx, agentID, repo, password, snapshot, target)
}

func (s *AgentService) BackupList(serverName, repo, password string) (*GenericResultData, error) {
	_, agentID := ResolveBackend(serverName, s.Store, s.Hub)
	if agentID <= 0 {
		return nil, fmt.Errorf("no agent backend")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.Hub.SendBackupList(ctx, agentID, repo, password)
}
