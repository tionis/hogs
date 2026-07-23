package api

import (
	"encoding/json"
	"net/http"

	"github.com/tionis/hogs/agent"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
)

type DashboardHandler struct {
	Store        *database.Store
	Config       *config.Config
	Engine       *engine.Engine
	AgentManager *agent.Manager
}

func NewDashboardHandler(store *database.Store, cfg *config.Config, eng *engine.Engine, manager *agent.Manager) *DashboardHandler {
	return &DashboardHandler{Store: store, Config: cfg, Engine: eng, AgentManager: manager}
}

func (h *DashboardHandler) Overview(w http.ResponseWriter, r *http.Request) {
	servers, err := h.Store.ListServers()
	if err != nil {
		http.Error(w, "Failed to list servers", http.StatusInternalServerError)
		return
	}

	totalServers := len(servers)
	onlineServers := 0
	offlineServers := 0
	maintenanceServers := 0
	plannedServers := 0

	gameTypes := make(map[string]int)
	for _, s := range servers {
		switch s.State {
		case "online":
			onlineServers++
		case "offline":
			offlineServers++
		case "maintenance":
			maintenanceServers++
		case "planned":
			plannedServers++
		}
		gameTypes[s.GameType]++
	}

	agentOverview := map[string]interface{}{
		"enabled":      h.Config.AgentEnabled,
		"connected":    0,
		"disconnected": 0,
	}
	if h.AgentManager != nil {
		agents, _ := h.Store.ListAgents()
		if agents == nil {
			agents = []database.Agent{}
		}
		connected := 0
		for _, a := range agents {
			if h.AgentManager.ConnectedNode(a.NodeName) {
				connected++
			}
		}
		agentOverview["total"] = len(agents)
		agentOverview["connected"] = connected
		agentOverview["disconnected"] = len(agents) - connected
	}

	recentAudit, err := h.Store.ListAuditLog(10, 0)
	if err != nil {
		recentAudit = []database.AuditLogEntry{}
	}

	cronEnabled := h.Config.CronEnabled

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"servers": map[string]interface{}{
			"total":       totalServers,
			"online":      onlineServers,
			"offline":     offlineServers,
			"maintenance": maintenanceServers,
			"planned":     plannedServers,
			"byGameType":  gameTypes,
		},
		"agents":      agentOverview,
		"cron":        map[string]interface{}{"enabled": cronEnabled},
		"recentAudit": recentAudit,
	})
}

func (h *DashboardHandler) AgentList(w http.ResponseWriter, r *http.Request) {
	agents, err := h.Store.ListAgents()
	if err != nil {
		http.Error(w, "Failed to list agents", http.StatusInternalServerError)
		return
	}
	if agents == nil {
		agents = []database.Agent{}
	}

	type agentStatus struct {
		database.Agent
		Connected bool `json:"connected"`
	}

	var result []agentStatus
	for _, a := range agents {
		connected := false
		if h.AgentManager != nil {
			connected = h.AgentManager.ConnectedNode(a.NodeName)
		}
		result = append(result, agentStatus{Agent: a, Connected: connected})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
