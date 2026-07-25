package agent

import (
	"bytes"
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
	NodeName string
	ServerID string
	Manager  *Manager
}

func NewAgentBackend(nodeName, serverID string, manager *Manager) *AgentBackend {
	return &AgentBackend{NodeName: nodeName, ServerID: serverID, Manager: manager}
}

func (a *AgentBackend) action(ctx context.Context, action string) error {
	endpoint := fmt.Sprintf("/v1/servers/%s/actions/%s", url.PathEscape(a.ServerID), url.PathEscape(action))
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
		fmt.Sprintf("/v1/servers/%s/command", url.PathEscape(a.ServerID)),
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
	response, err := a.Manager.Stream(ctx, a.NodeName, http.MethodGet,
		fmt.Sprintf("/v1/servers/%s/status", url.PathEscape(a.ServerID)), nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return decodeBackendStatus(response.Body)
}

func (a *AgentBackend) Whitelist(ctx context.Context, request backend.WhitelistRequest) (*backend.WhitelistResult, error) {
	method := http.MethodPost
	var body io.Reader
	if request.Operation == "list" {
		method = http.MethodGet
	} else {
		encoded, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	response, err := a.Manager.do(ctx, a.NodeName, method,
		fmt.Sprintf("/v1/servers/%s/whitelist", url.PathEscape(a.ServerID)), body)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var envelope struct {
		Success bool                    `json:"success"`
		Data    backend.WhitelistResult `json:"data"`
		Error   string                  `json:"error"`
		Code    string                  `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode agent whitelist response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
		if envelope.Error == "" {
			envelope.Error = response.Status
		}
		return nil, &backend.WhitelistError{Code: envelope.Code, Message: envelope.Error}
	}
	if envelope.Data.Entries == nil {
		envelope.Data.Entries = []backend.WhitelistEntry{}
	}
	return &envelope.Data, nil
}

func decodeBackendStatus(reader io.Reader) (*backend.ServerStatus, error) {
	var status backend.ServerStatus
	if err := json.NewDecoder(io.LimitReader(reader, 1<<20)).Decode(&status); err != nil {
		return nil, fmt.Errorf("decode agent status: %w", err)
	}
	return &status, nil
}

func (a *AgentBackend) Name() string { return "agent" }

func ResolveBackend(serverID int, store *database.Store) (string, string) {
	server, err := store.GetServer(serverID)
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
	server, err := s.Store.GetServerByName(serverName)
	if err != nil || server == nil {
		return nil, fmt.Errorf("server %q not found", serverName)
	}
	backendType, node := ResolveBackend(server.ID, s.Store)
	if backendType != "agent" || node == "" {
		return nil, fmt.Errorf("no private agent backend available for server %s", serverName)
	}
	return NewAgentBackend(node, server.ManagementID, s.Manager), nil
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
		fmt.Sprintf("/v1/servers/%s/command", url.PathEscape(agentBackend.ServerID)),
		map[string]string{"command": command})
}

func (s *AgentService) operation(ctx context.Context, serverName, method, suffix string, request interface{}) (*GenericResultData, error) {
	agentBackend, err := s.backend(serverName)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("/v1/servers/%s/%s", url.PathEscape(agentBackend.ServerID), strings.TrimPrefix(suffix, "/"))
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
	endpoint := fmt.Sprintf("/v1/servers/%s/file?path=%s", url.PathEscape(agentBackend.ServerID), url.QueryEscape(filePath))
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

func (s *AgentService) FileOperation(serverName, operation, sourcePath, targetPath string) (*GenericResultData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	query := url.Values{
		"operation": []string{operation},
		"path":      []string{sourcePath},
		"target":    []string{targetPath},
	}
	return s.operation(ctx, serverName, http.MethodPost, "file-operations?"+query.Encode(), nil)
}

func (s *AgentService) BackupCreate(serverName string, paths, tags []string) (*GenericResultData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	return s.operation(ctx, serverName, http.MethodPost, "backups", map[string]interface{}{"paths": paths, "tags": tags})
}

func (s *AgentService) BackupRestore(serverName, snapshot, confirmServerID string) (*GenericResultData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	return s.operation(ctx, serverName, http.MethodPost, "restore", map[string]string{
		"snapshot": snapshot, "confirmServerId": confirmServerID,
	})
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
		fmt.Sprintf("/v1/servers/%s/console", url.PathEscape(agentBackend.ServerID)), nil)
}
