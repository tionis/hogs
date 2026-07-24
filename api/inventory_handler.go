package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/tionis/hogs/access"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/database"
)

const InventoryAPIVersion = "hogs.tionis.dev/v1alpha1"

type InventoryManifest struct {
	APIVersion    string                  `json:"apiVersion"`
	Generation    string                  `json:"generation"`
	Nodes         []InventoryNode         `json:"nodes"`
	Servers       []InventoryServer       `json:"servers"`
	Constraints   []InventoryConstraint   `json:"constraints"`
	Schedules     []InventorySchedule     `json:"schedules"`
	Templates     []InventoryTemplate     `json:"templates"`
	Webhooks      []InventoryWebhook      `json:"webhooks"`
	Notifications []InventoryNotification `json:"notifications"`
	Settings      map[string]string       `json:"settings"`
}

type InventoryNode struct {
	Name                string            `json:"name"`
	NodeName            string            `json:"nodeName"`
	Labels              map[string]string `json:"labels,omitempty"`
	DesiredCapabilities []string          `json:"desiredCapabilities"`
}

type InventoryServer struct {
	Name         string                 `json:"name"`
	Address      string                 `json:"address"`
	Description  string                 `json:"description"`
	MapURL       string                 `json:"mapUrl"`
	MapLifecycle string                 `json:"mapLifecycle"`
	ModURL       string                 `json:"modUrl"`
	State        string                 `json:"state"`
	GameType     string                 `json:"gameType"`
	ShowMOTD     bool                   `json:"showMotd"`
	Metadata     map[string]string      `json:"metadata"`
	Tags         []string               `json:"tags"`
	Unit         string                 `json:"unit"`
	DataPath     string                 `json:"dataPath"`
	Backend      InventoryBackend       `json:"backend"`
	Policy       InventoryServerPolicy  `json:"policy"`
	Commands     []InventoryCommand     `json:"commands"`
	AccessGrants []InventoryAccessGrant `json:"accessGrants"`
}

type InventoryAccessGrant struct {
	SubjectType  string   `json:"subjectType"`
	Subject      string   `json:"subject"`
	Effect       string   `json:"effect"`
	Capabilities []string `json:"capabilities"`
}

type InventoryBackend struct {
	Type       string `json:"type"`
	Node       string `json:"node,omitempty"`
	ExternalID string `json:"externalId,omitempty"`
	Identifier string `json:"identifier,omitempty"`
}

type InventoryServerPolicy struct {
	ACLRule        string   `json:"aclRule"`
	AllowedActions []string `json:"allowedActions"`
	Operators      []string `json:"operators"`
	Console        bool     `json:"console"`
	RCON           bool     `json:"rcon"`
	Start          bool     `json:"start"`
	Stop           bool     `json:"stop"`
	Backup         bool     `json:"backup"`
	Restore        bool     `json:"restore"`
	WritablePaths  []string `json:"writablePaths"`
}

type InventoryCommand struct {
	Name        string          `json:"name"`
	DisplayName string          `json:"displayName"`
	Template    string          `json:"template"`
	Params      json.RawMessage `json:"params"`
	ACLRule     string          `json:"aclRule"`
	Enabled     bool            `json:"enabled"`
}

type InventoryConstraint struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Condition   string `json:"condition"`
	Strategy    string `json:"strategy"`
	Priority    int    `json:"priority"`
	Enabled     bool   `json:"enabled"`
}

type InventorySchedule struct {
	Name       string          `json:"name"`
	Schedule   string          `json:"schedule"`
	ServerName string          `json:"serverName"`
	Action     string          `json:"action"`
	Params     json.RawMessage `json:"params"`
	ACLRule    string          `json:"aclRule"`
	Enabled    bool            `json:"enabled"`
}

type InventoryTemplate struct {
	Name            string          `json:"name"`
	GameType        string          `json:"gameType"`
	DefaultSettings json.RawMessage `json:"defaultSettings"`
	DefaultCommands json.RawMessage `json:"defaultCommands"`
	DefaultACL      string          `json:"defaultAcl"`
	DefaultTags     json.RawMessage `json:"defaultTags"`
	Description     string          `json:"description"`
}

type InventoryWebhook struct {
	Name    string          `json:"name"`
	URL     string          `json:"url"`
	Secret  string          `json:"secret,omitempty"`
	Events  json.RawMessage `json:"events"`
	Enabled bool            `json:"enabled"`
}

type InventoryNotification struct {
	Name    string          `json:"name"`
	Type    string          `json:"type"`
	URL     string          `json:"url"`
	Events  json.RawMessage `json:"events"`
	Enabled bool            `json:"enabled"`
}

type InventoryChange struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

type InventoryHandler struct {
	Store      *database.Store
	afterApply func([]InventoryChange) error
}

func NewInventoryHandler(store *database.Store) *InventoryHandler {
	return &InventoryHandler{Store: store}
}

// SetAfterApply registers a runtime reload hook. Reconciliation commits before
// this hook runs, so callers can retry an apply safely if a reload fails.
func (h *InventoryHandler) SetAfterApply(hook func([]InventoryChange) error) {
	h.afterApply = hook
}

func (h *InventoryHandler) GetState(w http.ResponseWriter, r *http.Request) {
	manifest, digest, appliedAt, actor, err := h.loadState()
	if err != nil {
		http.Error(w, "Failed to load inventory state", http.StatusInternalServerError)
		return
	}
	agents, err := h.Store.ListAgents()
	if err != nil {
		http.Error(w, "Failed to load observed agents", http.StatusInternalServerError)
		return
	}
	servers, err := h.Store.ListServers()
	if err != nil {
		http.Error(w, "Failed to load observed servers", http.StatusInternalServerError)
		return
	}
	users, err := h.Store.ListUsers()
	if err != nil {
		http.Error(w, "Failed to load observed users", http.StatusInternalServerError)
		return
	}
	publicServers := make([]*database.PublicServer, 0, len(servers))
	metrics := make(map[string]*database.ServerMetric, len(servers))
	for i := range servers {
		publicServers = append(publicServers, servers[i].ToPublic())
		metric, err := h.Store.GetLatestServerMetric(servers[i].Name)
		if err != nil {
			http.Error(w, "Failed to load observed server metrics", http.StatusInternalServerError)
			return
		}
		metrics[servers[i].Name] = metric
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"manifest":  redactManifest(manifest),
		"digest":    digest,
		"appliedAt": appliedAt,
		"actor":     actor,
		"observed": map[string]interface{}{
			"agents": agents, "servers": publicServers, "metrics": metrics, "users": users,
		},
	})
}

func (h *InventoryHandler) Plan(w http.ResponseWriter, r *http.Request) {
	manifest, ok := decodeInventoryManifest(w, r)
	if !ok {
		return
	}
	current, currentDigest, _, _, err := h.loadState()
	if err != nil {
		http.Error(w, "Failed to load inventory state", http.StatusInternalServerError)
		return
	}
	digest, err := inventoryDigest(manifest)
	if err != nil {
		http.Error(w, "Failed to hash inventory", http.StatusInternalServerError)
		return
	}
	changes := diffInventory(current, manifest)
	legacyDeletes, err := h.firstAdoptionDeletes(currentDigest, manifest)
	if err != nil {
		http.Error(w, "Failed to inspect pre-existing inventory", http.StatusInternalServerError)
		return
	}
	changes = append(changes, legacyDeletes...)
	sortChanges(changes)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"generation":    manifest.Generation,
		"digest":        digest,
		"currentDigest": currentDigest,
		"changes":       changes,
		"destructive":   hasDeletes(changes),
	})
}

func (h *InventoryHandler) Apply(w http.ResponseWriter, r *http.Request) {
	manifest, ok := decodeInventoryManifest(w, r)
	if !ok {
		return
	}
	current, currentDigest, _, _, err := h.loadState()
	if err != nil {
		http.Error(w, "Failed to load inventory state", http.StatusInternalServerError)
		return
	}
	if expected := r.Header.Get("If-Match"); expected != "" && expected != currentDigest {
		http.Error(w, "Inventory state changed; plan again", http.StatusPreconditionFailed)
		return
	}
	changes := diffInventory(current, manifest)
	legacyDeletes, err := h.firstAdoptionDeletes(currentDigest, manifest)
	if err != nil {
		http.Error(w, "Failed to inspect pre-existing inventory", http.StatusInternalServerError)
		return
	}
	changes = append(changes, legacyDeletes...)
	sortChanges(changes)
	if hasDeletes(changes) && !strings.EqualFold(r.Header.Get("X-HOGS-Confirm-Prune"), "true") {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error":   "apply would remove managed resources; set X-HOGS-Confirm-Prune: true",
			"changes": changes,
		})
		return
	}
	digest, err := inventoryDigest(manifest)
	if err != nil {
		http.Error(w, "Failed to hash inventory", http.StatusInternalServerError)
		return
	}
	key := auth.GetAPIKeyFromContext(r)
	actor := "api"
	if key != nil {
		actor = "api-key:" + key.Name
	}
	err = h.applyManifest(manifest, current, digest, actor, changes)
	if err != nil {
		http.Error(w, "Failed to apply inventory: "+err.Error(), http.StatusBadRequest)
		return
	}
	if h.afterApply != nil {
		if err := h.afterApply(changes); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"error":      "inventory was applied, but runtime state could not be reloaded",
				"generation": manifest.Generation,
				"digest":     digest,
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"generation": manifest.Generation,
		"digest":     digest,
		"changes":    changes,
	})
}

func (h *InventoryHandler) Events(w http.ResponseWriter, r *http.Request) {
	after, err := strconv.Atoi(r.URL.Query().Get("after"))
	if err != nil && r.URL.Query().Get("after") != "" {
		http.Error(w, "after must be an integer cursor", http.StatusBadRequest)
		return
	}
	rows, err := h.Store.DB.Query(`SELECT id, generation, timestamp, resource_type, resource_key, action, actor, details
		FROM inventory_events WHERE id > ? ORDER BY id LIMIT 1000`, after)
	if err != nil {
		http.Error(w, "Failed to list inventory events", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type event struct {
		ID           int             `json:"id"`
		Generation   string          `json:"generation"`
		Timestamp    string          `json:"timestamp"`
		ResourceType string          `json:"resourceType"`
		ResourceKey  string          `json:"resourceKey"`
		Action       string          `json:"action"`
		Actor        string          `json:"actor"`
		Details      json.RawMessage `json:"details"`
	}
	events := []event{}
	cursor := after
	for rows.Next() {
		var e event
		var details string
		if err := rows.Scan(&e.ID, &e.Generation, &e.Timestamp, &e.ResourceType, &e.ResourceKey, &e.Action, &e.Actor, &details); err != nil {
			http.Error(w, "Failed to read inventory events", http.StatusInternalServerError)
			return
		}
		e.Details = json.RawMessage(details)
		events = append(events, e)
		cursor = e.ID
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": events, "cursor": cursor})
}

func decodeInventoryManifest(w http.ResponseWriter, r *http.Request) (InventoryManifest, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var manifest InventoryManifest
	if err := decoder.Decode(&manifest); err != nil {
		http.Error(w, "Invalid inventory JSON: "+err.Error(), http.StatusBadRequest)
		return manifest, false
	}
	if err := validateManifest(&manifest); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return manifest, false
	}
	normalizeManifest(&manifest)
	return manifest, true
}

func validateManifest(m *InventoryManifest) error {
	if m.APIVersion != InventoryAPIVersion {
		return fmt.Errorf("apiVersion must be %q", InventoryAPIVersion)
	}
	if strings.TrimSpace(m.Generation) == "" {
		return fmt.Errorf("generation is required")
	}
	nodes := make(map[string]bool)
	for _, node := range m.Nodes {
		if err := uniqueName("node", node.Name, nodes); err != nil {
			return err
		}
		if node.NodeName == "" {
			return fmt.Errorf("node %q requires nodeName", node.Name)
		}
	}
	servers := make(map[string]bool)
	for _, server := range m.Servers {
		if err := uniqueName("server", server.Name, servers); err != nil {
			return err
		}
		if server.GameType == "" {
			return fmt.Errorf("server %q requires gameType", server.Name)
		}
		if server.MapLifecycle != "" && server.MapLifecycle != "game" && server.MapLifecycle != "independent" {
			return fmt.Errorf("server %q mapLifecycle must be game or independent", server.Name)
		}
		if _, legacy := server.Metadata["map_lifecycle"]; legacy {
			return fmt.Errorf("server %q must use mapLifecycle instead of metadata.map_lifecycle", server.Name)
		}
		if server.Backend.Type == "agent" && (strings.TrimSpace(server.Unit) == "" || strings.TrimSpace(server.DataPath) == "") {
			return fmt.Errorf("server %q agent backend requires unit and dataPath", server.Name)
		}
		if server.Backend.Type == "agent" && !filepath.IsAbs(server.DataPath) {
			return fmt.Errorf("server %q dataPath must be absolute", server.Name)
		}
		for _, path := range server.Policy.WritablePaths {
			if !strings.HasPrefix(path, "/") {
				return fmt.Errorf("server %q writable path %q must be absolute", server.Name, path)
			}
			if server.DataPath != "" {
				relative, err := filepath.Rel(filepath.Clean(server.DataPath), filepath.Clean(path))
				if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					return fmt.Errorf("server %q writable path %q escapes dataPath", server.Name, path)
				}
			}
		}
		if server.Backend.Type != "agent" && server.Backend.Type != "pterodactyl" && server.Backend.Type != "none" {
			return fmt.Errorf("server %q backend.type must be agent, pterodactyl, or none", server.Name)
		}
		if server.Backend.Type == "agent" && !nodes[server.Backend.Node] {
			return fmt.Errorf("server %q references unknown agent node %q", server.Name, server.Backend.Node)
		}
		if server.Backend.Type == "pterodactyl" && strings.TrimSpace(server.Backend.ExternalID) == "" {
			return fmt.Errorf("server %q pterodactyl backend requires externalId", server.Name)
		}
		commands := make(map[string]bool)
		for _, command := range server.Commands {
			if err := uniqueName("command", server.Name+"/"+command.Name, commands); err != nil {
				return err
			}
			if len(command.Params) > 0 && !json.Valid(command.Params) {
				return fmt.Errorf("server %q command %q params must be JSON", server.Name, command.Name)
			}
		}
		grants := make(map[string]bool)
		for _, grant := range server.AccessGrants {
			if grant.SubjectType != "user" && grant.SubjectType != "group" && grant.SubjectType != "authenticated" && grant.SubjectType != "everyone" {
				return fmt.Errorf("server %q access grant has invalid subjectType %q", server.Name, grant.SubjectType)
			}
			if grant.Effect != "allow" && grant.Effect != "deny" {
				return fmt.Errorf("server %q access grant has invalid effect %q", server.Name, grant.Effect)
			}
			if strings.TrimSpace(grant.Subject) == "" {
				return fmt.Errorf("server %q access grant subject is required", server.Name)
			}
			key := grant.Effect + ":" + grant.SubjectType + ":" + grant.Subject
			if grants[key] {
				return fmt.Errorf("server %q has duplicate access grant %q", server.Name, key)
			}
			grants[key] = true
			if len(grant.Capabilities) == 0 {
				return fmt.Errorf("server %q access grant %q has no capabilities", server.Name, key)
			}
			for _, capability := range grant.Capabilities {
				if !access.Known(capability) {
					return fmt.Errorf("server %q access grant %q has unknown capability %q", server.Name, key, capability)
				}
			}
		}
	}
	for _, schedule := range m.Schedules {
		if !servers[schedule.ServerName] {
			return fmt.Errorf("schedule %q references unknown server %q", schedule.Name, schedule.ServerName)
		}
		if err := validateRawJSON("schedule "+schedule.Name+" params", schedule.Params); err != nil {
			return err
		}
		parser := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(schedule.Schedule); err != nil {
			return fmt.Errorf("schedule %q has invalid six-field cron expression: %w", schedule.Name, err)
		}
	}
	for _, strategy := range m.Constraints {
		if strategy.Strategy != "deny" && strategy.Strategy != "queue" && strategy.Strategy != "stop_oldest" {
			return fmt.Errorf("constraint %q has unsupported strategy %q", strategy.Name, strategy.Strategy)
		}
	}
	for _, template := range m.Templates {
		for label, value := range map[string]json.RawMessage{
			"defaultSettings": template.DefaultSettings,
			"defaultCommands": template.DefaultCommands,
			"defaultTags":     template.DefaultTags,
		} {
			if err := validateRawJSON("template "+template.Name+" "+label, value); err != nil {
				return err
			}
		}
	}
	for _, webhook := range m.Webhooks {
		if err := validateRawJSON("webhook "+webhook.Name+" events", webhook.Events); err != nil {
			return err
		}
	}
	for _, notification := range m.Notifications {
		if err := validateRawJSON("notification "+notification.Name+" events", notification.Events); err != nil {
			return err
		}
	}
	return validateTopLevelNames(m)
}

func validateRawJSON(label string, value json.RawMessage) error {
	if len(value) > 0 && !json.Valid(value) {
		return fmt.Errorf("%s must be JSON", label)
	}
	return nil
}

func validateTopLevelNames(m *InventoryManifest) error {
	groups := []struct {
		name   string
		values []string
	}{
		{"constraint", constraintNames(m.Constraints)}, {"schedule", scheduleNames(m.Schedules)},
		{"template", templateNames(m.Templates)}, {"webhook", webhookNames(m.Webhooks)},
		{"notification", notificationNames(m.Notifications)},
	}
	for _, group := range groups {
		seen := make(map[string]bool)
		for _, value := range group.values {
			if err := uniqueName(group.name, value, seen); err != nil {
				return err
			}
		}
	}
	return nil
}

func uniqueName(kind, value string, seen map[string]bool) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s name is required", kind)
	}
	if seen[value] {
		return fmt.Errorf("duplicate %s %q", kind, value)
	}
	seen[value] = true
	return nil
}

func normalizeManifest(m *InventoryManifest) {
	if m.Nodes == nil {
		m.Nodes = []InventoryNode{}
	}
	if m.Servers == nil {
		m.Servers = []InventoryServer{}
	}
	for i := range m.Servers {
		if m.Servers[i].MapLifecycle == "" {
			m.Servers[i].MapLifecycle = "game"
		}
	}
	if m.Constraints == nil {
		m.Constraints = []InventoryConstraint{}
	}
	if m.Schedules == nil {
		m.Schedules = []InventorySchedule{}
	}
	if m.Templates == nil {
		m.Templates = []InventoryTemplate{}
	}
	if m.Webhooks == nil {
		m.Webhooks = []InventoryWebhook{}
	}
	if m.Notifications == nil {
		m.Notifications = []InventoryNotification{}
	}
	if m.Settings == nil {
		m.Settings = map[string]string{}
	}
	sort.Slice(m.Nodes, func(i, j int) bool { return m.Nodes[i].Name < m.Nodes[j].Name })
	for i := range m.Nodes {
		sort.Strings(m.Nodes[i].DesiredCapabilities)
	}
	sort.Slice(m.Servers, func(i, j int) bool { return m.Servers[i].Name < m.Servers[j].Name })
	for i := range m.Servers {
		sort.Strings(m.Servers[i].Tags)
		sort.Strings(m.Servers[i].Policy.AllowedActions)
		sort.Strings(m.Servers[i].Policy.Operators)
		sort.Strings(m.Servers[i].Policy.WritablePaths)
		for grant := range m.Servers[i].AccessGrants {
			if m.Servers[i].AccessGrants[grant].SubjectType == "authenticated" || m.Servers[i].AccessGrants[grant].SubjectType == "everyone" {
				m.Servers[i].AccessGrants[grant].Subject = "*"
			}
			sort.Strings(m.Servers[i].AccessGrants[grant].Capabilities)
		}
		sort.Slice(m.Servers[i].AccessGrants, func(a, b int) bool {
			left, right := m.Servers[i].AccessGrants[a], m.Servers[i].AccessGrants[b]
			return left.Effect+":"+left.SubjectType+":"+left.Subject < right.Effect+":"+right.SubjectType+":"+right.Subject
		})
		sort.Slice(m.Servers[i].Commands, func(a, b int) bool { return m.Servers[i].Commands[a].Name < m.Servers[i].Commands[b].Name })
	}
	sort.Slice(m.Constraints, func(i, j int) bool { return m.Constraints[i].Name < m.Constraints[j].Name })
	sort.Slice(m.Schedules, func(i, j int) bool { return m.Schedules[i].Name < m.Schedules[j].Name })
	sort.Slice(m.Templates, func(i, j int) bool { return m.Templates[i].Name < m.Templates[j].Name })
	sort.Slice(m.Webhooks, func(i, j int) bool { return m.Webhooks[i].Name < m.Webhooks[j].Name })
	sort.Slice(m.Notifications, func(i, j int) bool { return m.Notifications[i].Name < m.Notifications[j].Name })
}

func inventoryDigest(m InventoryManifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (h *InventoryHandler) loadState() (InventoryManifest, string, string, string, error) {
	var generation, digest, raw, appliedAt, actor string
	err := h.Store.DB.QueryRow("SELECT generation, digest, manifest, applied_at, actor FROM inventory_state WHERE singleton = 1").Scan(&generation, &digest, &raw, &appliedAt, &actor)
	if err == sql.ErrNoRows {
		m := InventoryManifest{APIVersion: InventoryAPIVersion, Nodes: []InventoryNode{}, Servers: []InventoryServer{}, Constraints: []InventoryConstraint{}, Schedules: []InventorySchedule{}, Templates: []InventoryTemplate{}, Webhooks: []InventoryWebhook{}, Notifications: []InventoryNotification{}, Settings: map[string]string{}}
		return m, "", "", "", nil
	}
	if err != nil {
		return InventoryManifest{}, "", "", "", err
	}
	var m InventoryManifest
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return InventoryManifest{}, "", "", "", err
	}
	normalizeManifest(&m)
	return m, digest, appliedAt, actor, nil
}

func diffInventory(old, desired InventoryManifest) []InventoryChange {
	oldResources := inventoryResources(old)
	newResources := inventoryResources(desired)
	changes := []InventoryChange{}
	for key, value := range newResources {
		oldValue, exists := oldResources[key]
		if !exists {
			changes = append(changes, InventoryChange{Resource: key, Action: "create"})
		} else if oldValue != value {
			changes = append(changes, InventoryChange{Resource: key, Action: "update"})
		}
	}
	for key := range oldResources {
		if _, exists := newResources[key]; !exists {
			changes = append(changes, InventoryChange{Resource: key, Action: "delete"})
		}
	}
	sortChanges(changes)
	return changes
}

// firstAdoptionDeletes makes the first declarative plan account for resources
// created by older interactive HOGS versions. applyManifest is authoritative
// over these tables, so omitting this discovery would make the apply remove
// rows that were invisible in the plan.
func (h *InventoryHandler) firstAdoptionDeletes(currentDigest string, desired InventoryManifest) ([]InventoryChange, error) {
	if currentDigest != "" {
		return nil, nil
	}
	desiredResources := inventoryResources(desired)
	tables := []struct {
		resource string
		table    string
	}{
		{"nodes", "agents"},
		{"servers", "servers"},
		{"constraints", "constraints"},
		{"schedules", "cron_jobs"},
		{"templates", "server_templates"},
		{"webhooks", "webhooks"},
		{"notifications", "notification_channels"},
	}
	changes := []InventoryChange{}
	for _, item := range tables {
		rows, err := h.Store.DB.Query("SELECT name FROM " + item.table)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return nil, err
			}
			resource := item.resource + "/" + name
			if _, retained := desiredResources[resource]; !retained {
				changes = append(changes, InventoryChange{Resource: resource, Action: "delete"})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return changes, nil
}

func inventoryResources(m InventoryManifest) map[string]string {
	result := make(map[string]string)
	add := func(key string, value interface{}) { b, _ := json.Marshal(value); result[key] = string(b) }
	for _, v := range m.Nodes {
		add("nodes/"+v.Name, v)
	}
	for _, v := range m.Servers {
		add("servers/"+v.Name, v)
	}
	for _, v := range m.Constraints {
		add("constraints/"+v.Name, v)
	}
	for _, v := range m.Schedules {
		add("schedules/"+v.Name, v)
	}
	for _, v := range m.Templates {
		add("templates/"+v.Name, v)
	}
	for _, v := range m.Webhooks {
		add("webhooks/"+v.Name, v)
	}
	for _, v := range m.Notifications {
		add("notifications/"+v.Name, v)
	}
	for key, value := range m.Settings {
		add("settings/"+key, value)
	}
	return result
}

func sortChanges(changes []InventoryChange) {
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Resource == changes[j].Resource {
			return changes[i].Action < changes[j].Action
		}
		return changes[i].Resource < changes[j].Resource
	})
}
func hasDeletes(changes []InventoryChange) bool {
	for _, change := range changes {
		if change.Action == "delete" {
			return true
		}
	}
	return false
}

func redactManifest(m InventoryManifest) InventoryManifest {
	if m.Settings != nil {
		settings := make(map[string]string, len(m.Settings))
		for key, value := range m.Settings {
			settings[key] = value
		}
		m.Settings = settings
	}
	for i := range m.Servers {
		if m.Servers[i].Metadata == nil {
			continue
		}
		metadata := make(map[string]string, len(m.Servers[i].Metadata))
		for key, value := range m.Servers[i].Metadata {
			if isSensitiveName(key) && value != "" {
				metadata[key] = "***"
			} else {
				metadata[key] = value
			}
		}
		m.Servers[i].Metadata = metadata
	}
	for i := range m.Webhooks {
		if m.Webhooks[i].Secret != "" {
			m.Webhooks[i].Secret = "***"
		}
	}
	for i := range m.Notifications {
		if m.Notifications[i].URL != "" {
			m.Notifications[i].URL = "***"
		}
	}
	for key, value := range m.Settings {
		if isSensitiveName(key) {
			if value != "" {
				m.Settings[key] = "***"
			}
		}
	}
	return m
}

func isSensitiveName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "secret") || strings.Contains(lower, "token") ||
		strings.Contains(lower, "password") || strings.Contains(lower, "key")
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func constraintNames(v []InventoryConstraint) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = v[i].Name
	}
	return out
}
func scheduleNames(v []InventorySchedule) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = v[i].Name
	}
	return out
}
func templateNames(v []InventoryTemplate) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = v[i].Name
	}
	return out
}
func webhookNames(v []InventoryWebhook) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = v[i].Name
	}
	return out
}
func notificationNames(v []InventoryNotification) []string {
	out := make([]string, len(v))
	for i := range v {
		out[i] = v[i].Name
	}
	return out
}

func (h *InventoryHandler) applyManifest(manifest, previous InventoryManifest, digest, actor string, changes []InventoryChange) error {
	tx, err := h.Store.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := applyNodes(tx, manifest.Nodes); err != nil {
		return err
	}
	if err := applyServers(tx, manifest.Servers); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM background_tags
		WHERE tag NOT IN ('dark', 'light', 'home')
		  AND tag NOT IN (SELECT DISTINCT game_type FROM servers WHERE game_type <> '')`); err != nil {
		return err
	}
	if err := applyConstraints(tx, manifest.Constraints); err != nil {
		return err
	}
	if err := applySchedules(tx, manifest.Schedules); err != nil {
		return err
	}
	if err := applyTemplates(tx, manifest.Templates); err != nil {
		return err
	}
	if err := applyWebhooks(tx, manifest.Webhooks); err != nil {
		return err
	}
	if err := applyNotifications(tx, manifest.Notifications); err != nil {
		return err
	}
	if err := applySettings(tx, manifest.Settings, previous.Settings); err != nil {
		return err
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO inventory_state(singleton,generation,digest,manifest,applied_at,actor) VALUES(1,?,?,?,?,?)
		ON CONFLICT(singleton) DO UPDATE SET generation=excluded.generation,digest=excluded.digest,manifest=excluded.manifest,applied_at=excluded.applied_at,actor=excluded.actor`,
		manifest.Generation, digest, string(raw), time.Now().UTC().Format(time.RFC3339), actor); err != nil {
		return err
	}
	for _, change := range changes {
		parts := strings.SplitN(change.Resource, "/", 2)
		resourceType, resourceKey := parts[0], ""
		if len(parts) == 2 {
			resourceKey = parts[1]
		}
		if _, err := tx.Exec("INSERT INTO inventory_events(generation,resource_type,resource_key,action,actor,details) VALUES(?,?,?,?,?,?)", manifest.Generation, resourceType, resourceKey, change.Action, actor, "{}"); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func applyNodes(tx *sql.Tx, nodes []InventoryNode) error {
	keep := make([]string, 0, len(nodes))
	for _, node := range nodes {
		keep = append(keep, node.Name)
		var id int
		err := tx.QueryRow("SELECT id FROM agents WHERE name = ?", node.Name).Scan(&id)
		if err == sql.ErrNoRows {
			result, err := tx.Exec("INSERT INTO agents(name,token,token_hash,token_prefix,node_name,capabilities) VALUES(?,?,?,?,?,?)", node.Name, "", "", "", node.NodeName, "[]")
			if err != nil {
				return err
			}
			inserted, _ := result.LastInsertId()
			id = int(inserted)
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE agents SET node_name = ? WHERE id = ?", node.NodeName, id); err != nil {
			return err
		}
	}
	return deleteMissing(tx, "agents", "name", keep)
}

func applyServers(tx *sql.Tx, servers []InventoryServer) error {
	keep := make([]string, 0, len(servers))
	for _, server := range servers {
		keep = append(keep, server.Name)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO game_types(slug,display_name,player_noun,icon,accent_color,builtin)
			VALUES(?,?,'Players','','#666666',0)`, server.GameType, server.GameType); err != nil {
			return err
		}
		metadataValues := make(map[string]string, len(server.Metadata)+1)
		for key, value := range server.Metadata {
			metadataValues[key] = value
		}
		metadataValues["map_lifecycle"] = server.MapLifecycle
		metadata, _ := json.Marshal(metadataValues)
		show := 0
		if server.ShowMOTD {
			show = 1
		}
		state := server.State
		if state == "" {
			state = "online"
		}
		_, err := tx.Exec(`INSERT INTO servers(name,address,description,map_url,mod_url,state,game_type,show_motd,metadata) VALUES(?,?,?,?,?,?,?,?,?)
			ON CONFLICT(name) DO UPDATE SET address=excluded.address,description=excluded.description,map_url=excluded.map_url,mod_url=excluded.mod_url,state=excluded.state,game_type=excluded.game_type,show_motd=excluded.show_motd,metadata=excluded.metadata`,
			server.Name, server.Address, server.Description, server.MapURL, server.ModURL, state, server.GameType, show, string(metadata))
		if err != nil {
			return err
		}
		var serverID int
		if err := tx.QueryRow("SELECT id FROM servers WHERE name=?", server.Name).Scan(&serverID); err != nil {
			return err
		}
		allowed, _ := json.Marshal(server.Policy.AllowedActions)
		if server.Backend.Type == "none" {
			if _, err := tx.Exec("DELETE FROM pterodactyl_servers WHERE server_id=?", serverID); err != nil {
				return err
			}
		} else {
			externalID := server.Backend.ExternalID
			if server.Backend.Type == "agent" {
				externalID = "agent:" + server.Name
			}
			_, err := tx.Exec(`INSERT INTO pterodactyl_servers(server_id,ptero_server_id,ptero_identifier,allowed_actions,acl_rule,node) VALUES(?,?,?,?,?,?)
				ON CONFLICT(server_id) DO UPDATE SET ptero_server_id=excluded.ptero_server_id,ptero_identifier=excluded.ptero_identifier,allowed_actions=excluded.allowed_actions,acl_rule=excluded.acl_rule,node=excluded.node`,
				serverID, externalID, server.Backend.Identifier, string(allowed), server.Policy.ACLRule, server.Backend.Node)
			if err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DELETE FROM server_tags WHERE server_id=?", serverID); err != nil {
			return err
		}
		for _, tag := range server.Tags {
			if _, err := tx.Exec("INSERT INTO server_tags(server_id,tag) VALUES(?,?)", serverID, tag); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DELETE FROM command_schemas WHERE server_id=?", serverID); err != nil {
			return err
		}
		for _, command := range server.Commands {
			params := command.Params
			if len(params) == 0 {
				params = json.RawMessage("{}")
			}
			enabled := 0
			if command.Enabled {
				enabled = 1
			}
			if _, err := tx.Exec("INSERT INTO command_schemas(server_id,name,display_name,template,params,acl_rule,enabled) VALUES(?,?,?,?,?,?,?)", serverID, command.Name, command.DisplayName, command.Template, string(params), command.ACLRule, enabled); err != nil {
				return err
			}
		}
		operators, _ := json.Marshal(server.Policy.Operators)
		writablePaths, _ := json.Marshal(server.Policy.WritablePaths)
		_, err = tx.Exec(`INSERT INTO server_management(server_id,unit_name,data_path,operators,console_enabled,rcon_enabled,start_enabled,stop_enabled,backup_enabled,restore_enabled,writable_paths)
			VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET unit_name=excluded.unit_name,data_path=excluded.data_path,operators=excluded.operators,console_enabled=excluded.console_enabled,rcon_enabled=excluded.rcon_enabled,start_enabled=excluded.start_enabled,stop_enabled=excluded.stop_enabled,backup_enabled=excluded.backup_enabled,restore_enabled=excluded.restore_enabled,writable_paths=excluded.writable_paths`,
			serverID, server.Unit, server.DataPath, string(operators), boolInt(server.Policy.Console), boolInt(server.Policy.RCON), boolInt(server.Policy.Start), boolInt(server.Policy.Stop), boolInt(server.Policy.Backup), boolInt(server.Policy.Restore), string(writablePaths))
		if err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM server_access_grants WHERE server_id=?", serverID); err != nil {
			return err
		}
		for _, grant := range server.AccessGrants {
			capabilities, _ := json.Marshal(grant.Capabilities)
			if _, err := tx.Exec(`INSERT INTO server_access_grants(server_id,subject_type,subject,effect,capabilities)
				VALUES(?,?,?,?,?)`, serverID, grant.SubjectType, grant.Subject, grant.Effect, string(capabilities)); err != nil {
				return err
			}
		}
	}
	if err := deleteMissing(tx, "servers", "name", keep); err != nil {
		return err
	}
	_, err := tx.Exec("DELETE FROM server_management WHERE server_id NOT IN (SELECT id FROM servers)")
	return err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func applyConstraints(tx *sql.Tx, values []InventoryConstraint) error {
	keep := []string{}
	for _, v := range values {
		keep = append(keep, v.Name)
		enabled := 0
		if v.Enabled {
			enabled = 1
		}
		_, err := tx.Exec(`INSERT INTO constraints(name,description,condition,strategy,priority,enabled) VALUES(?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET description=excluded.description,condition=excluded.condition,strategy=excluded.strategy,priority=excluded.priority,enabled=excluded.enabled`, v.Name, v.Description, v.Condition, v.Strategy, v.Priority, enabled)
		if err != nil {
			return err
		}
	}
	return deleteMissing(tx, "constraints", "name", keep)
}
func applySchedules(tx *sql.Tx, values []InventorySchedule) error {
	keep := []string{}
	for _, v := range values {
		keep = append(keep, v.Name)
		enabled := 0
		if v.Enabled {
			enabled = 1
		}
		params := v.Params
		if len(params) == 0 {
			params = json.RawMessage("{}")
		}
		_, err := tx.Exec(`INSERT INTO cron_jobs(name,schedule,server_name,action,params,acl_rule,enabled) VALUES(?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET schedule=excluded.schedule,server_name=excluded.server_name,action=excluded.action,params=excluded.params,acl_rule=excluded.acl_rule,enabled=excluded.enabled`, v.Name, v.Schedule, v.ServerName, v.Action, string(params), v.ACLRule, enabled)
		if err != nil {
			return err
		}
	}
	return deleteMissing(tx, "cron_jobs", "name", keep)
}
func applyTemplates(tx *sql.Tx, values []InventoryTemplate) error {
	keep := []string{}
	for _, v := range values {
		keep = append(keep, v.Name)
		settings := defaultJSON(v.DefaultSettings, "{}")
		commands := defaultJSON(v.DefaultCommands, "[]")
		tags := defaultJSON(v.DefaultTags, "[]")
		_, err := tx.Exec(`INSERT INTO server_templates(name,game_type,default_settings,default_commands,default_acl,default_tags,description) VALUES(?,?,?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET game_type=excluded.game_type,default_settings=excluded.default_settings,default_commands=excluded.default_commands,default_acl=excluded.default_acl,default_tags=excluded.default_tags,description=excluded.description`, v.Name, v.GameType, settings, commands, v.DefaultACL, tags, v.Description)
		if err != nil {
			return err
		}
	}
	return deleteMissing(tx, "server_templates", "name", keep)
}
func applyWebhooks(tx *sql.Tx, values []InventoryWebhook) error {
	keep := []string{}
	for _, v := range values {
		keep = append(keep, v.Name)
		enabled := 0
		if v.Enabled {
			enabled = 1
		}
		events := defaultJSON(v.Events, "[]")
		_, err := tx.Exec(`INSERT INTO webhooks(name,url,secret,events,enabled) VALUES(?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET url=excluded.url,secret=excluded.secret,events=excluded.events,enabled=excluded.enabled`, v.Name, v.URL, v.Secret, events, enabled)
		if err != nil {
			return err
		}
	}
	return deleteMissing(tx, "webhooks", "name", keep)
}
func applyNotifications(tx *sql.Tx, values []InventoryNotification) error {
	keep := []string{}
	for _, v := range values {
		keep = append(keep, v.Name)
		enabled := 0
		if v.Enabled {
			enabled = 1
		}
		events := defaultJSON(v.Events, "[]")
		_, err := tx.Exec(`INSERT INTO notification_channels(name,type,url,events,enabled) VALUES(?,?,?,?,?) ON CONFLICT(name) DO UPDATE SET type=excluded.type,url=excluded.url,events=excluded.events,enabled=excluded.enabled`, v.Name, v.Type, v.URL, events, enabled)
		if err != nil {
			return err
		}
	}
	return deleteMissing(tx, "notification_channels", "name", keep)
}
func applySettings(tx *sql.Tx, desired, previous map[string]string) error {
	for key, value := range desired {
		if _, err := tx.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); err != nil {
			return err
		}
	}
	for key := range previous {
		if _, ok := desired[key]; !ok {
			if _, err := tx.Exec("DELETE FROM settings WHERE key=?", key); err != nil {
				return err
			}
		}
	}
	return nil
}

func defaultJSON(value json.RawMessage, fallback string) string {
	if len(value) == 0 {
		return fallback
	}
	return string(value)
}
func deleteMissing(tx *sql.Tx, table, column string, keep []string) error {
	if len(keep) == 0 {
		_, err := tx.Exec("DELETE FROM " + table)
		return err
	}
	args := make([]interface{}, len(keep))
	marks := make([]string, len(keep))
	for i, value := range keep {
		args[i] = value
		marks[i] = "?"
	}
	_, err := tx.Exec("DELETE FROM "+table+" WHERE "+column+" NOT IN ("+strings.Join(marks, ",")+")", args...)
	return err
}
