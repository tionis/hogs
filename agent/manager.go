package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/internal/capability"
	"github.com/tionis/hogs/query"
	"gopkg.in/yaml.v3"
)

type ManagerConfig struct {
	Nodes []ManagedNode `yaml:"nodes"`
}

type ManagedNode struct {
	Node       string `yaml:"node"`
	Mode       string `yaml:"mode"`
	ControlURL string `yaml:"control_url"`
	PublicURL  string `yaml:"public_url"`
	SecretFile string `yaml:"secret_file"`
	secret     []byte
}

type DirectAccess struct {
	Mode      string    `json:"mode"`
	URL       string    `json:"url"`
	Token     string    `json:"token,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

type NodeSummary struct {
	Mode       string
	ControlURL string
	PublicURL  string
}

func (m *Manager) NodeSummary(node string) (NodeSummary, bool) {
	if m == nil {
		return NodeSummary{}, false
	}
	m.mu.RLock()
	managed, ok := m.nodes[node]
	m.mu.RUnlock()
	if !ok {
		return NodeSummary{}, false
	}
	return NodeSummary{Mode: managed.Mode, ControlURL: managed.ControlURL, PublicURL: managed.PublicURL}, true
}

func (m *Manager) UpdateNodeTransport(node, mode, controlURL, publicURL string) error {
	m.mu.RLock()
	current, ok := m.nodes[node]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("worker %q is not configured", node)
	}
	candidate := current
	candidate.Mode = mode
	candidate.ControlURL = strings.TrimRight(strings.TrimSpace(controlURL), "/")
	candidate.PublicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if err := validateManagedNode(candidate); err != nil {
		return err
	}
	if err := m.store.UpdateAgentTransport(node, candidate.Mode, candidate.ControlURL, candidate.PublicURL); err != nil {
		return fmt.Errorf("save worker transport: %w", err)
	}
	m.mu.Lock()
	m.nodes[node] = candidate
	delete(m.seen, node)
	m.mu.Unlock()
	m.client.CloseIdleConnections()
	return nil
}

type ResourceStatus struct {
	Running         bool      `json:"running"`
	CPUPercent      *float64  `json:"cpuPercent,omitempty"`
	CPULimitPercent *float64  `json:"cpuLimitPercent,omitempty"`
	MemoryCurrent   *uint64   `json:"memoryCurrentBytes,omitempty"`
	MemoryPeak      *uint64   `json:"memoryPeakBytes,omitempty"`
	MemoryHigh      *uint64   `json:"memoryHighBytes,omitempty"`
	MemoryLimit     *uint64   `json:"memoryLimitBytes,omitempty"`
	SampledAt       time.Time `json:"sampledAt"`
}

type Manager struct {
	client    *http.Client
	nodes     map[string]ManagedNode
	mu        sync.RWMutex
	seen      map[string]time.Time
	resources map[string]ResourceStatus
	ctx       context.Context
	cancel    context.CancelFunc
	store     *database.Store
}

func NewManager(configPath string, store *database.Store) (*Manager, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read agent configuration: %w", err)
	}
	var cfg ManagerConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse agent configuration: %w", err)
	}
	nodes := make(map[string]ManagedNode, len(cfg.Nodes))
	for _, node := range cfg.Nodes {
		if node.Mode == "" {
			node.Mode = "direct"
		}
		if err := validateManagedNode(node); err != nil {
			return nil, err
		}
		if _, exists := nodes[node.Node]; exists {
			return nil, fmt.Errorf("duplicate agent node %q", node.Node)
		}
		secret, err := os.ReadFile(node.SecretFile)
		if err != nil {
			return nil, fmt.Errorf("read secret for agent %q: %w", node.Node, err)
		}
		node.secret = bytes.TrimSpace(secret)
		if len(node.secret) < 32 {
			return nil, fmt.Errorf("secret for agent %q must contain at least 32 bytes", node.Node)
		}
		node.ControlURL = strings.TrimRight(node.ControlURL, "/")
		node.PublicURL = strings.TrimRight(node.PublicURL, "/")
		nodes[node.Node] = node
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("at least one agent node is required")
	}

	for nodeName, configured := range nodes {
		_ = store.InitializeAgentTransport(nodeName, configured.Mode, configured.ControlURL, configured.PublicURL)
		persisted, lookupErr := store.GetAgentByNodeName(nodeName)
		if lookupErr != nil || persisted == nil || persisted.ControlURL == "" {
			continue
		}
		candidate := configured
		candidate.Mode = persisted.Mode
		candidate.ControlURL = persisted.ControlURL
		candidate.PublicURL = persisted.PublicURL
		if validateManagedNode(candidate) == nil {
			nodes[nodeName] = candidate
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		client: &http.Client{Transport: &http.Transport{
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   20,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		}},
		nodes: nodes, seen: make(map[string]time.Time),
		resources: make(map[string]ResourceStatus), cancel: cancel,
		ctx: ctx, store: store,
	}
	go manager.healthLoop(ctx)
	return manager, nil
}

func validateManagedNode(node ManagedNode) error {
	if node.Node == "" || node.ControlURL == "" || node.SecretFile == "" {
		return fmt.Errorf("each agent requires node, control_url, and secret_file")
	}
	if node.Mode != "direct" && node.Mode != "tunneled" {
		return fmt.Errorf("agent %q has unsupported mode %q", node.Node, node.Mode)
	}
	control, err := url.Parse(node.ControlURL)
	if err != nil || control.Host == "" || (control.Scheme != "https" && control.Scheme != "http") {
		return fmt.Errorf("agent %q has invalid control_url", node.Node)
	}
	if node.Mode == "direct" {
		public, err := url.Parse(node.PublicURL)
		if control.Scheme != "https" {
			return fmt.Errorf("direct agent %q control_url must use HTTPS", node.Node)
		}
		if err != nil || public.Scheme != "https" || public.Host == "" {
			return fmt.Errorf("direct agent %q requires an HTTPS public_url", node.Node)
		}
	}
	return nil
}

func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.cancel()
	m.client.CloseIdleConnections()
}

// PublicOrigins returns the browser origins used by direct agents. These URLs
// came from the validated deployment configuration.
func (m *Manager) PublicOrigins() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	nodes := make([]ManagedNode, 0, len(m.nodes))
	for _, node := range m.nodes {
		nodes = append(nodes, node)
	}
	m.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, node := range nodes {
		if node.Mode != "direct" {
			continue
		}
		public, err := url.Parse(node.PublicURL)
		if err != nil || public.Scheme != "https" || public.Host == "" {
			continue
		}
		seen[public.Scheme+"://"+public.Host] = struct{}{}
	}
	origins := make([]string, 0, len(seen))
	for origin := range seen {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	return origins
}

func (m *Manager) ConnectedNode(node string) bool {
	m.mu.RLock()
	last := m.seen[node]
	m.mu.RUnlock()
	return !last.IsZero() && time.Since(last) < 30*time.Second
}

func (m *Manager) ServerResources(serverName string) (ResourceStatus, bool) {
	m.mu.RLock()
	resources, found := m.resources[serverName]
	m.mu.RUnlock()
	return resources, found && time.Since(resources.SampledAt) < 30*time.Second
}

func (m *Manager) healthLoop(ctx context.Context) {
	m.pollHealth(ctx)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pollHealth(ctx)
		}
	}
}

func (m *Manager) StartStatusPolling(store *database.Store, cache *query.ServerStatusCache) {
	m.pollStatuses(m.ctx, store, cache)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
				m.pollStatuses(m.ctx, store, cache)
			}
		}
	}()
}

func (m *Manager) pollStatuses(ctx context.Context, store *database.Store, cache *query.ServerStatusCache) {
	servers, err := store.ListServers()
	if err != nil {
		return
	}
	for _, server := range servers {
		link, err := store.GetPterodactylLink(server.ID)
		if err != nil || link == nil || link.Node == "" {
			continue
		}
		server := server
		node := link.Node
		go func() {
			requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			driver := store.ResolveGameDriver(server.GameType)
			driverHint := "generic"
			if driver.Kind == "embedded" {
				driverHint = driver.Slug
			}
			response, err := m.do(requestCtx, node, http.MethodGet,
				fmt.Sprintf("/v1/servers/%s/status?driver=%s",
					url.PathEscape(server.Name), url.QueryEscape(driverHint)), nil)
			if err != nil {
				cache.SetAgentObservation(server.Name, &query.ServerStatus{
					Online: false, LastUpdated: time.Now(), Error: err.Error(),
				})
				return
			}
			defer response.Body.Close()
			var status struct {
				Online       bool            `json:"online"`
				Players      int             `json:"players"`
				MaxPlayers   int             `json:"maxPlayers"`
				PlayersKnown bool            `json:"playersKnown"`
				Version      string          `json:"version"`
				Resources    *ResourceStatus `json:"resources"`
			}
			if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&status) != nil {
				return
			}
			if status.Resources != nil {
				status.Resources.Running = status.Online
				m.mu.Lock()
				m.resources[server.Name] = *status.Resources
				m.mu.Unlock()
				if err := store.CreateServerResourceSample(&database.ServerResourceSample{
					ServerName: server.Name, Timestamp: status.Resources.SampledAt,
					Running: status.Online, CPUPercent: status.Resources.CPUPercent,
					CPULimitPercent:    status.Resources.CPULimitPercent,
					MemoryCurrentBytes: status.Resources.MemoryCurrent,
					MemoryPeakBytes:    status.Resources.MemoryPeak,
					MemoryHighBytes:    status.Resources.MemoryHigh,
					MemoryLimitBytes:   status.Resources.MemoryLimit,
				}); err != nil {
					log.Printf("store resource sample for %s: %v", server.Name, err)
				}
			}
			cache.SetAgentObservation(server.Name, &query.ServerStatus{
				Online: status.Online, Players: status.Players, MaxPlayers: status.MaxPlayers,
				PlayersKnown: status.PlayersKnown, Version: status.Version, LastUpdated: time.Now(),
			})
		}()
	}
}

func (m *Manager) pollHealth(ctx context.Context) {
	m.mu.RLock()
	nodes := make([]string, 0, len(m.nodes))
	for node := range m.nodes {
		nodes = append(nodes, node)
	}
	m.mu.RUnlock()
	for _, node := range nodes {
		node := node
		go func() {
			requestCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			response, err := m.do(requestCtx, node, http.MethodGet, "/v1/health", nil)
			if err != nil {
				_ = m.store.UpdateAgentObservation(node, nil, false)
				return
			}
			defer response.Body.Close()
			var health struct {
				Capabilities []string `json:"capabilities"`
			}
			if response.StatusCode == http.StatusOK && json.NewDecoder(response.Body).Decode(&health) == nil {
				m.mu.Lock()
				m.seen[node] = time.Now()
				m.mu.Unlock()
				_ = m.store.UpdateAgentObservation(node, health.Capabilities, true)
			} else {
				_ = m.store.UpdateAgentObservation(node, nil, false)
			}
		}()
	}
}

func (m *Manager) Connected(agentID int, store interface {
	GetAgent(int) (*database.Agent, error)
}) bool {
	managedAgent, err := store.GetAgent(agentID)
	return err == nil && managedAgent != nil && m.ConnectedNode(managedAgent.NodeName)
}

func (m *Manager) JSON(ctx context.Context, node, method, endpoint string, requestBody interface{}) (*GenericResultData, error) {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	response, err := m.do(ctx, node, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var result GenericResultData
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode agent response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !result.Success {
		if result.Error == "" {
			result.Error = response.Status
		}
		return &result, fmt.Errorf("%s", result.Error)
	}
	return &result, nil
}

func (m *Manager) Stream(ctx context.Context, node, method, endpoint string, body io.Reader) (*http.Response, error) {
	return m.StreamHeaders(ctx, node, method, endpoint, body, nil)
}

func (m *Manager) StreamHeaders(ctx context.Context, node, method, endpoint string, body io.Reader, headers http.Header) (*http.Response, error) {
	response, err := m.doHeaders(ctx, node, method, endpoint, body, headers)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}
	defer response.Body.Close()
	var result GenericResultData
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result)
	if result.Error == "" {
		result.Error = response.Status
	}
	return nil, fmt.Errorf("%s", result.Error)
}

func (m *Manager) DirectAccess(node, subject, method, endpoint, filePath, targetPath string, maxBytes int64) (*DirectAccess, error) {
	m.mu.RLock()
	managedNode, ok := m.nodes[node]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("node %q has no agent configuration", node)
	}
	if managedNode.Mode != "direct" {
		return &DirectAccess{Mode: managedNode.Mode}, nil
	}
	route, err := url.Parse(endpoint)
	if err != nil || !strings.HasPrefix(route.Path, "/v1/") {
		return nil, fmt.Errorf("invalid agent capability endpoint")
	}
	expires := time.Now().UTC().Add(capability.DefaultLifetime)
	claims := capability.NewClaims(node, subject, method, route.Path, filePath, maxBytes, capability.DefaultLifetime)
	claims.TargetPath = targetPath
	token, err := capability.Sign(managedNode.secret, claims)
	if err != nil {
		return nil, err
	}
	return &DirectAccess{
		Mode: "direct", URL: managedNode.PublicURL + endpoint,
		Token: token, ExpiresAt: expires,
	}, nil
}

func (m *Manager) endpoint(node, endpoint string) (ManagedNode, string, error) {
	m.mu.RLock()
	managedNode, ok := m.nodes[node]
	m.mu.RUnlock()
	if !ok {
		return ManagedNode{}, "", fmt.Errorf("node %q has no agent configuration", node)
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	return managedNode, managedNode.ControlURL + endpoint, nil
}

func (m *Manager) do(ctx context.Context, node, method, endpoint string, body io.Reader) (*http.Response, error) {
	return m.doHeaders(ctx, node, method, endpoint, body, nil)
}

func (m *Manager) doHeaders(ctx context.Context, node, method, endpoint string, body io.Reader, headers http.Header) (*http.Response, error) {
	managedNode, target, err := m.endpoint(node, endpoint)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	claims := capability.NewClaims(node, "hogs-control", method, request.URL.Path,
		request.URL.Query().Get("path"), 0, capability.DefaultLifetime)
	claims.TargetPath = request.URL.Query().Get("target")
	token, err := capability.Sign(managedNode.secret, claims)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Hogs-Request-ID", claims.ID)
	return m.client.Do(request)
}
