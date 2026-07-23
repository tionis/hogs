package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tionis/hogs/backend"
	"github.com/tionis/hogs/database"
)

const lifecycleActionTimeout = 3 * time.Minute

type AgentBackend struct {
	NodeName   string
	ServerName string
	Manager    *Manager
}

func NewAgentBackend(nodeName, serverName string, manager *Manager) *AgentBackend {
	return &AgentBackend{NodeName: nodeName, ServerName: serverName, Manager: manager}
}

func (a *AgentBackend) action(ctx context.Context, action string) error {
	endpoint := fmt.Sprintf("/v1/servers/%s/actions/%s", url.PathEscape(a.ServerName), url.PathEscape(action))
	_, err := a.Manager.JSON(ctx, a.NodeName, http.MethodPost, endpoint, map[string]interface{}{})
	return err
}

func (a *AgentBackend) Start(ctx context.Context) error   { return a.action(ctx, "start") }
func (a *AgentBackend) Stop(ctx context.Context) error    { return a.action(ctx, "stop") }
func (a *AgentBackend) Restart(ctx context.Context) error { return a.action(ctx, "restart") }

func (a *AgentBackend) SendCommand(ctx context.Context, command string) error {
	_, err := a.SendCommandOutput(ctx, command)
	return err
}

func (a *AgentBackend) SendCommandOutput(ctx context.Context, command string) (string, error) {
	result, err := a.Manager.JSON(ctx, a.NodeName, http.MethodPost,
		fmt.Sprintf("/v1/servers/%s/command", url.PathEscape(a.ServerName)),
		map[string]string{"command": command})
	if err != nil {
		return "", err
	}
	if data, ok := result.Data.(map[string]interface{}); ok {
		if output, ok := data["output"].(string); ok {
			return output, nil
		}
	}
	return "", nil
}

func (a *AgentBackend) Status(ctx context.Context) (*backend.ServerStatus, error) {
	return nil, fmt.Errorf("agent status is exposed through the status cache")
}

func (a *AgentBackend) Name() string { return "agent" }

func ResolveBackend(serverName string, store *database.Store) (string, string) {
	server, err := store.GetServerByName(serverName)
	if err != nil || server == nil {
		return "", ""
	}
	link, err := store.GetPterodactylLink(server.ID)
	if err != nil || link == nil {
		return "", ""
	}
	if link.Node == "" {
		return "pterodactyl", ""
	}
	managedAgent, err := store.GetAgentByNodeName(link.Node)
	if err != nil || managedAgent == nil {
		return "pterodactyl", ""
	}
	return "agent", managedAgent.NodeName
}

type AgentService struct {
	Store   *database.Store
	Manager *Manager
}

func NewAgentService(store *database.Store, manager *Manager) *AgentService {
	return &AgentService{Store: store, Manager: manager}
}

func (s *AgentService) backend(serverName string) (*AgentBackend, error) {
	backendType, node := ResolveBackend(serverName, s.Store)
	if backendType != "agent" || node == "" {
		return nil, fmt.Errorf("no private agent backend available for server %s", serverName)
	}
	return NewAgentBackend(node, serverName, s.Manager), nil
}

func (s *AgentService) ExecuteAction(serverName, action string) error {
	agentBackend, err := s.backend(serverName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), lifecycleActionTimeout)
	defer cancel()
	switch action {
	case "start":
		return agentBackend.Start(ctx)
	case "stop":
		return agentBackend.Stop(ctx)
	case "restart":
		return agentBackend.Restart(ctx)
	default:
		return fmt.Errorf("unknown action: %s", action)
	}
}

func (s *AgentService) SendCommand(serverName, command string) error {
	_, err := s.SendCommandResult(serverName, command)
	return err
}

func (s *AgentService) SendCommandResult(serverName, command string) (*GenericResultData, error) {
	agentBackend, err := s.backend(serverName)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.Manager.JSON(ctx, agentBackend.NodeName, http.MethodPost,
		fmt.Sprintf("/v1/servers/%s/command", url.PathEscape(serverName)),
		map[string]string{"command": command})
}

func (s *AgentService) operation(ctx context.Context, serverName, method, suffix string, request interface{}) (*GenericResultData, error) {
	agentBackend, err := s.backend(serverName)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("/v1/servers/%s/%s", url.PathEscape(serverName), strings.TrimPrefix(suffix, "/"))
	return s.Manager.JSON(ctx, agentBackend.NodeName, method, endpoint, request)
}

func (s *AgentService) FileList(serverName, filePath string) (*GenericResultData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.operation(ctx, serverName, http.MethodGet, "files?path="+url.QueryEscape(filePath), nil)
}

func (s *AgentService) FileRead(serverName, filePath string) (*GenericResultData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	response, err := s.FileStream(ctx, serverName, http.MethodGet, filePath, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	return &GenericResultData{Success: true, Data: map[string]interface{}{
		"path": filePath, "size": len(content), "contentBase64": base64.StdEncoding.EncodeToString(content),
	}}, nil
}

func (s *AgentService) FileWrite(serverName, filePath, contentBase64 string) (*GenericResultData, error) {
	content, err := base64.StdEncoding.DecodeString(contentBase64)
	if err != nil {
		return nil, fmt.Errorf("invalid base64 content: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	response, err := s.FileStream(ctx, serverName, http.MethodPut, filePath, strings.NewReader(string(content)))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var result GenericResultData
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *AgentService) FileStream(ctx context.Context, serverName, method, filePath string, body io.Reader) (*http.Response, error) {
	return s.FileStreamHeaders(ctx, serverName, method, filePath, body, nil)
}

func (s *AgentService) FileStreamHeaders(ctx context.Context, serverName, method, filePath string, body io.Reader, headers http.Header) (*http.Response, error) {
	agentBackend, err := s.backend(serverName)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("/v1/servers/%s/file?path=%s", url.PathEscape(serverName), url.QueryEscape(filePath))
	return s.Manager.StreamHeaders(ctx, agentBackend.NodeName, method, endpoint, body, headers)
}

func (s *AgentService) FileDelete(serverName, filePath string) (*GenericResultData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.operation(ctx, serverName, http.MethodDelete, "file?path="+url.QueryEscape(filePath), nil)
}

func (s *AgentService) Mkdir(serverName, filePath string) (*GenericResultData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.operation(ctx, serverName, http.MethodPost, "directories", map[string]string{"path": filePath})
}

func (s *AgentService) BackupCreate(serverName string, paths, tags []string) (*GenericResultData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	return s.operation(ctx, serverName, http.MethodPost, "backups", map[string]interface{}{"paths": paths, "tags": tags})
}

func (s *AgentService) BackupRestore(serverName, snapshot, target string) (*GenericResultData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	return s.operation(ctx, serverName, http.MethodPost, "restore", map[string]string{"snapshot": snapshot, "target": target})
}

func (s *AgentService) BackupList(serverName string) (*GenericResultData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.operation(ctx, serverName, http.MethodGet, "backups", nil)
}

func (s *AgentService) Console(ctx context.Context, serverName string) (*http.Response, error) {
	agentBackend, err := s.backend(serverName)
	if err != nil {
		return nil, err
	}
	return s.Manager.Stream(ctx, agentBackend.NodeName, http.MethodGet,
		fmt.Sprintf("/v1/servers/%s/console", url.PathEscape(serverName)), nil)
}
