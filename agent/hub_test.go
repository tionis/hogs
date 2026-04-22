package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
)

func testHub(t *testing.T) (*Hub, *database.Store) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	store, err := database.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() {
		store.DB.Close()
		os.Remove(dbPath)
	})
	cfg := &config.Config{
		AgentEnabled:      true,
		AgentHeartbeatSec: 30,
	}
	hub := NewHub(store, cfg)
	return hub, store
}

func TestNewHub(t *testing.T) {
	hub, _ := testHub(t)
	if hub == nil {
		t.Fatal("expected non-nil hub")
	}
	if hub.Conns == nil {
		t.Error("expected non-nil Conns map")
	}
	if hub.pending == nil {
		t.Error("expected non-nil pending map")
	}
}

func TestHubGetConnEmpty(t *testing.T) {
	hub, _ := testHub(t)
	conn := hub.GetConn(1)
	if conn != nil {
		t.Error("expected nil for nonexistent connection")
	}
}

func TestHubGetConnByNodeEmpty(t *testing.T) {
	hub, _ := testHub(t)
	conn := hub.GetConnByNode("nonexistent")
	if conn != nil {
		t.Error("expected nil for nonexistent node")
	}
}

func TestHubAllocRequestID(t *testing.T) {
	hub, _ := testHub(t)
	id1 := hub.allocRequestID()
	id2 := hub.allocRequestID()
	if id1 == id2 {
		t.Error("expected unique request IDs")
	}
	if id1 == "" {
		t.Error("expected non-empty request ID")
	}
}

func TestHubRegisterAndResolvePending(t *testing.T) {
	hub, _ := testHub(t)
	reqID := hub.allocRequestID()
	pr := hub.registerPending(reqID, 1, "action", []byte("{}"))

	result := &GenericResultData{Success: true, Data: "test"}

	go hub.resolvePending(reqID, result)

	select {
	case got := <-pr.ch:
		if !got.Success {
			t.Error("expected success")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for pending resolution")
	}
}

func TestHubResolvePendingNotFound(t *testing.T) {
	hub, _ := testHub(t)
	result := &GenericResultData{Success: true}
	hub.resolvePending("nonexistent", result)
}

func TestSendEnvelopeWithResultOffline(t *testing.T) {
	hub, _ := testHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := hub.SendAction(ctx, 999, "start")
	if err == nil {
		t.Error("expected error for offline agent")
	}
}

func TestSendCommandOffline(t *testing.T) {
	hub, _ := testHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := hub.SendCommand(ctx, 999, "test command")
	if err == nil {
		t.Error("expected error for offline agent")
	}
}

func TestSendFileOperationsOffline(t *testing.T) {
	hub, _ := testHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := hub.SendFileList(ctx, 999, "/tmp")
	if err == nil {
		t.Error("expected error for offline agent")
	}

	_, err = hub.SendFileRead(ctx, 999, "/tmp/file")
	if err == nil {
		t.Error("expected error for offline agent")
	}

	_, err = hub.SendFileWrite(ctx, 999, "/tmp/file", "content")
	if err == nil {
		t.Error("expected error for offline agent")
	}

	_, err = hub.SendFileDelete(ctx, 999, "/tmp/file")
	if err == nil {
		t.Error("expected error for offline agent")
	}

	_, err = hub.SendMkdir(ctx, 999, "/tmp/dir")
	if err == nil {
		t.Error("expected error for offline agent")
	}
}

func TestResolveBackendNoLink(t *testing.T) {
	hub, store := testHub(t)

	backendType, agentID := ResolveBackend("nonexistent", store, hub)
	if backendType != "" {
		t.Errorf("expected empty backend type for nonexistent server, got %q", backendType)
	}
	if agentID != 0 {
		t.Errorf("expected 0 agent ID, got %d", agentID)
	}
}

func TestResolveBackendPterodactyl(t *testing.T) {
	hub, store := testHub(t)

	srv := &database.Server{Name: "ptest", Address: "p:25565", GameType: "minecraft", State: "online"}
	store.CreateServer(srv)
	created, _ := store.GetServerByName("ptest")

	link := &database.PterodactylLink{
		ServerID:       created.ID,
		PteroServerID:  "abc-123",
		AllowedActions: `["start","stop"]`,
	}
	store.CreatePterodactylLink(link)

	backendType, agentID := ResolveBackend("ptest", store, hub)
	if backendType != "pterodactyl" {
		t.Errorf("expected pterodactyl backend, got %q", backendType)
	}
	if agentID != 0 {
		t.Errorf("expected 0 agent ID for pterodactyl, got %d", agentID)
	}
}

func TestResolveBackendAgent(t *testing.T) {
	hub, store := testHub(t)

	agent := &database.Agent{Name: "test-agent", Token: "test-token", NodeName: "node1", Capabilities: json.RawMessage(`["start","stop"]`)}
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent error: %v", err)
	}

	found, err := store.GetAgentByNodeName("node1")
	if err != nil {
		t.Fatalf("GetAgentByNodeName error: %v", err)
	}
	if found == nil {
		t.Fatal("GetAgentByNodeName returned nil - agent not found")
	}

	srv := &database.Server{Name: "atest", Address: "a:25565", GameType: "minecraft", State: "online"}
	store.CreateServer(srv)
	server, _ := store.GetServerByName("atest")

	link := &database.PterodactylLink{
		ServerID:       server.ID,
		PteroServerID:  "xyz-789",
		Node:           "node1",
		AllowedActions: `["start","stop"]`,
	}
	store.CreatePterodactylLink(link)

	backendType, agentID := ResolveBackend("atest", store, hub)
	if backendType != "agent" {
		t.Errorf("expected agent backend, got %q", backendType)
	}
	if agentID != agent.ID {
		t.Errorf("expected agent ID %d, got %d", agent.ID, agentID)
	}
}

func TestAgentServiceExecuteActionNoBackend(t *testing.T) {
	hub, store := testHub(t)
	service := NewAgentService(store, hub)

	err := service.ExecuteAction("nonexistent", "start")
	if err == nil {
		t.Error("expected error for nonexistent server")
	}
}

func TestAgentServiceSendCommandNoBackend(t *testing.T) {
	hub, store := testHub(t)
	service := NewAgentService(store, hub)

	err := service.SendCommand("nonexistent", "test")
	if err == nil {
		t.Error("expected error for nonexistent server")
	}
}

func TestRemoveConnFailsPendingRequests(t *testing.T) {
	hub, _ := testHub(t)
	reqID := hub.allocRequestID()
	_ = hub.registerPending(reqID, 42, "action", []byte("{}"))

	hub.RemoveConn(42)

	hub.pendingMu.Lock()
	_, exists := hub.pending[reqID]
	hub.pendingMu.Unlock()
	if exists {
		t.Error("expected pending request to be removed after RemoveConn")
	}
}

func TestEnvelopeSerialization(t *testing.T) {
	data, _ := json.Marshal(CommandRequestData{Command: "seed"})
	env := Envelope{Type: "command", RequestID: "42", Data: data}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}

	var parsed Envelope
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("json.Unmarshal error: %v", err)
	}
	if parsed.Type != "command" {
		t.Errorf("Type = %q, want %q", parsed.Type, "command")
	}
	if parsed.RequestID != "42" {
		t.Errorf("RequestID = %q, want %q", parsed.RequestID, "42")
	}

	var cmdData CommandRequestData
	json.Unmarshal(parsed.Data, &cmdData)
	if cmdData.Command != "seed" {
		t.Errorf("Command = %q, want %q", cmdData.Command, "seed")
	}
}

func TestEnvelopeEmptyRequestID(t *testing.T) {
	data, _ := json.Marshal(ActionRequestData{Action: "start"})
	env := Envelope{Type: "action", Data: data}
	b, _ := json.Marshal(env)

	var parsed Envelope
	json.Unmarshal(b, &parsed)
	if parsed.RequestID != "" {
		t.Errorf("expected empty RequestID, got %q", parsed.RequestID)
	}
}

func TestIsResultType(t *testing.T) {
	resultTypes := []string{
		"action_result", "command_result", "file_list_result",
		"file_read_result", "file_write_result", "file_delete_result",
		"mkdir_result", "backup_create_result", "backup_restore_result",
		"backup_list_result",
	}
	for _, rt := range resultTypes {
		if !isResultType(rt) {
			t.Errorf("expected %q to be a result type", rt)
		}
	}

	nonResultTypes := []string{"register", "status", "console", "action", "command"}
	for _, nrt := range nonResultTypes {
		if isResultType(nrt) {
			t.Errorf("expected %q NOT to be a result type", nrt)
		}
	}
}

func TestServeWSMissingToken(t *testing.T) {
	hub, _ := testHub(t)
	req := httptest.NewRequest(http.MethodGet, "/agent/ws", nil)
	w := httptest.NewRecorder()
	hub.ServeWS(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestServeWSInvalidToken(t *testing.T) {
	hub, _ := testHub(t)
	req := httptest.NewRequest(http.MethodGet, "/agent/ws?token=invalid", nil)
	w := httptest.NewRecorder()
	hub.ServeWS(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestRemoveConnNonexistent(t *testing.T) {
	hub, _ := testHub(t)
	hub.RemoveConn(999)
}

func TestGenericResultDataSerialization(t *testing.T) {
	result := GenericResultData{Success: true, Data: map[string]interface{}{"entries": 5}}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal error: %v", err)
	}
	if !strings.Contains(string(b), `"success":true`) {
		t.Errorf("expected success:true in %q", string(b))
	}
}

func TestConsoleBufferAndBroadcast(t *testing.T) {
	hub, _ := testHub(t)

	// Simulate adding console lines
	hub.broadcastConsole("test-server", consoleLine{Line: "line1", Timestamp: "2024-01-01T00:00:00Z"})
	hub.broadcastConsole("test-server", consoleLine{Line: "line2", Timestamp: "2024-01-01T00:00:01Z"})

	// Verify buffer
	hub.consoleBuffersMu.RLock()
	buf := hub.consoleBuffers["test-server"]
	hub.consoleBuffersMu.RUnlock()
	if len(buf) != 2 {
		t.Errorf("expected 2 lines in buffer, got %d", len(buf))
	}
	if buf[0].Line != "line1" {
		t.Errorf("expected line1, got %s", buf[0].Line)
	}
}

func TestConsoleBufferLimit(t *testing.T) {
	hub, _ := testHub(t)

	// Add more than consoleBufferSize lines
	for i := 0; i < consoleBufferSize+10; i++ {
		hub.broadcastConsole("test-server", consoleLine{Line: string(rune('a' + i%26)), Timestamp: "2024-01-01T00:00:00Z"})
	}

	hub.consoleBuffersMu.RLock()
	buf := hub.consoleBuffers["test-server"]
	hub.consoleBuffersMu.RUnlock()
	if len(buf) != consoleBufferSize {
		t.Errorf("expected buffer to be limited to %d, got %d", consoleBufferSize, len(buf))
	}
}

func TestConsoleClientManagement(t *testing.T) {
	hub, _ := testHub(t)

	// Create a mock websocket connection (we can't easily create a real one without a server)
	// Just test that add/remove don't panic
	// In a real scenario we'd use a websocket test server

	// Test that adding/removing clients works with nil conn doesn't panic
	hub.AddConsoleClient("srv1", nil)
	hub.RemoveConsoleClient("srv1", nil)

	// Verify client map is clean
	hub.consoleClientsMu.RLock()
	clients := hub.consoleClients["srv1"]
	hub.consoleClientsMu.RUnlock()
	if clients != nil && len(clients) > 0 {
		t.Error("expected no clients after removal")
	}
}

func TestGetConnByServerName(t *testing.T) {
	hub, store := testHub(t)

	// Create an agent
	agent := &database.Agent{Name: "test-agent", NodeName: "node1", TokenHash: "hash", TokenPrefix: "pre"}
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Manually add a connection with ServerName
	ac := &AgentConn{AgentID: agent.ID, ServerName: "MyServer", NodeName: "node1", Hub: hub, Send: make(chan []byte, 10)}
	hub.mu.Lock()
	hub.Conns[agent.ID] = ac
	hub.mu.Unlock()

	got := hub.GetConnByServerName("MyServer")
	if got == nil {
		t.Fatal("expected to find connection by server name")
	}
	if got.ServerName != "MyServer" {
		t.Errorf("expected ServerName MyServer, got %s", got.ServerName)
	}

	// Nonexistent server
	if hub.GetConnByServerName("Nonexistent") != nil {
		t.Error("expected nil for nonexistent server")
	}
}

func TestContextCancellation(t *testing.T) {
	hub, _ := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := hub.SendAction(ctx, 1, "start")
	if err == nil {
		t.Error("expected error with cancelled context")
	}
}

func TestAgentBackendName(t *testing.T) {
	hub, _ := testHub(t)
	ab := NewAgentBackend(1, "node1", hub)
	if ab.Name() != "agent" {
		t.Errorf("Name() = %q, want %q", ab.Name(), "agent")
	}
}

func TestAgentBackendStatusNotImplemented(t *testing.T) {
	hub, _ := testHub(t)
	ab := NewAgentBackend(1, "node1", hub)
	ctx := context.Background()
	_, err := ab.Status(ctx)
	if err == nil {
		t.Error("expected error for unimplemented status")
	}
}

func TestHubLoadAndRecoverPendingOps(t *testing.T) {
	hub, store := testHub(t)

	// Create a stale pending op in DB
	op := &database.AgentPendingOp{
		RequestID: "stale-req",
		AgentID:   1,
		OpType:    "action",
		Payload:   `{"action":"start"}`,
		CreatedAt: "2024-01-01T00:00:00Z",
		ExpiresAt: "2099-01-01T00:05:00Z",
		Resolved:  false,
	}
	if err := store.CreateAgentPendingOp(op); err != nil {
		t.Fatalf("CreateAgentPendingOp failed: %v", err)
	}

	// Load and recover should mark it resolved
	hub.LoadAndRecoverPendingOps()

	resolved, _ := store.GetAgentPendingOp("stale-req")
	if resolved == nil || !resolved.Resolved {
		t.Error("expected stale pending op to be resolved after recovery")
	}
}

func TestHubPendingOpPersistence(t *testing.T) {
	hub, store := testHub(t)

	reqID := hub.allocRequestID()
	pr := hub.registerPending(reqID, 1, "command", []byte(`{"command":"help"}`))
	if pr == nil {
		t.Fatal("expected pending request")
	}

	// Verify it was persisted
	op, err := store.GetAgentPendingOp(reqID)
	if err != nil {
		t.Fatalf("GetAgentPendingOp failed: %v", err)
	}
	if op == nil {
		t.Fatal("expected op to be persisted")
	}
	if op.OpType != "command" {
		t.Errorf("OpType = %q, want command", op.OpType)
	}

	// Resolve it
	result := &GenericResultData{Success: true}
	hub.resolvePending(reqID, result)

	// Verify it was marked resolved
	resolved, _ := store.GetAgentPendingOp(reqID)
	if resolved == nil || !resolved.Resolved {
		t.Error("expected op to be marked resolved")
	}
}
