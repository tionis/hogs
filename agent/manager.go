package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/internal/wgnet"
	"github.com/tionis/hogs/query"
	"golang.org/x/net/http2"
	"gopkg.in/yaml.v3"
)

type ManagerConfig struct {
	Address        string        `yaml:"address"`
	PrivateKeyFile string        `yaml:"private_key_file"`
	ListenPort     int           `yaml:"listen_port"`
	APIPort        uint16        `yaml:"api_port"`
	Peers          []ManagedPeer `yaml:"peers"`
}

type ManagedPeer struct {
	Node      string `yaml:"node"`
	Address   string `yaml:"address"`
	PublicKey string `yaml:"public_key"`
}

type Manager struct {
	network *wgnet.Network
	client  *http.Client
	apiPort uint16
	peers   map[string]ManagedPeer
	mu      sync.RWMutex
	seen    map[string]time.Time
	ctx     context.Context
	cancel  context.CancelFunc
	store   *database.Store
}

func NewManager(configPath string, store *database.Store) (*Manager, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read agent network config: %w", err)
	}
	var cfg ManagerConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse agent network config: %w", err)
	}
	if cfg.APIPort == 0 {
		return nil, fmt.Errorf("agent network api_port is required")
	}
	privateKey, err := os.ReadFile(cfg.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read agent network private key: %w", err)
	}
	peers := make(map[string]ManagedPeer, len(cfg.Peers))
	wireGuardPeers := make([]wgnet.Peer, 0, len(cfg.Peers))
	for _, peer := range cfg.Peers {
		if peer.Node == "" || peer.Address == "" || peer.PublicKey == "" {
			return nil, fmt.Errorf("each agent network peer requires node, address, and public_key")
		}
		if _, exists := peers[peer.Node]; exists {
			return nil, fmt.Errorf("duplicate agent network peer %q", peer.Node)
		}
		peers[peer.Node] = peer
		wireGuardPeers = append(wireGuardPeers, wgnet.Peer{
			PublicKey: peer.PublicKey,
			AllowedIP: peer.Address + "/128",
		})
	}
	network, err := wgnet.New(wgnet.Config{
		Address: cfg.Address, PrivateKey: strings.TrimSpace(string(privateKey)),
		ListenPort: cfg.ListenPort, Peers: wireGuardPeers,
	}, "hogs: ")
	if err != nil {
		return nil, err
	}
	transport := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, networkType, address string, _ *tls.Config) (net.Conn, error) {
			return network.DialContext(ctx, networkType, address)
		},
		ReadIdleTimeout: 15 * time.Second,
		PingTimeout:     5 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		network: network, client: &http.Client{Transport: transport},
		apiPort: cfg.APIPort, peers: peers, seen: make(map[string]time.Time), cancel: cancel,
		ctx: ctx, store: store,
	}
	go manager.healthLoop(ctx)
	return manager, nil
}

func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.cancel()
	m.client.CloseIdleConnections()
	m.network.Close()
}

func (m *Manager) ConnectedNode(node string) bool {
	m.mu.RLock()
	last := m.seen[node]
	m.mu.RUnlock()
	return !last.IsZero() && time.Since(last) < 30*time.Second
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
			response, err := m.do(requestCtx, node, http.MethodGet,
				fmt.Sprintf("/v1/servers/%s/status", url.PathEscape(server.Name)), nil)
			if err != nil {
				cache.SetAgentObservation(server.Name, &query.ServerStatus{
					Online: false, LastUpdated: time.Now(), Error: err.Error(),
				})
				return
			}
			defer response.Body.Close()
			var status struct {
				Online       bool   `json:"online"`
				Players      int    `json:"players"`
				MaxPlayers   int    `json:"maxPlayers"`
				PlayersKnown bool   `json:"playersKnown"`
				Version      string `json:"version"`
			}
			if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&status) != nil {
				return
			}
			cache.SetAgentObservation(server.Name, &query.ServerStatus{
				Online: status.Online, Players: status.Players, MaxPlayers: status.MaxPlayers,
				PlayersKnown: status.PlayersKnown, Version: status.Version, LastUpdated: time.Now(),
			})
		}()
	}
}

func (m *Manager) pollHealth(ctx context.Context) {
	for node := range m.peers {
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
	agent, err := store.GetAgent(agentID)
	return err == nil && agent != nil && m.ConnectedNode(agent.NodeName)
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

func (m *Manager) endpoint(node, endpoint string) (string, error) {
	peer, ok := m.peers[node]
	if !ok {
		return "", fmt.Errorf("node %q has no private agent peer", node)
	}
	base := (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(peer.Address, fmt.Sprintf("%d", m.apiPort)),
	}).String()
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	return base + endpoint, nil
}

func (m *Manager) do(ctx context.Context, node, method, endpoint string, body io.Reader) (*http.Response, error) {
	return m.doHeaders(ctx, node, method, endpoint, body, nil)
}

func (m *Manager) doHeaders(ctx context.Context, node, method, endpoint string, body io.Reader, headers http.Header) (*http.Response, error) {
	target, err := m.endpoint(node, endpoint)
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
	request.Header.Set("X-Hogs-Request-ID", fmt.Sprintf("%d", time.Now().UnixNano()))
	return m.client.Do(request)
}
