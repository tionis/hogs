package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	robfigcron "github.com/robfig/cron/v3"
	"github.com/tionis/hogs/access"
	"github.com/tionis/hogs/auth"
	"github.com/tionis/hogs/config"
	hogscron "github.com/tionis/hogs/cron"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
)

type AutomationHandler struct {
	Store               *database.Store
	Config              *config.Config
	Engine              *engine.Engine
	AfterScheduleChange func() error
	Auth                *auth.Authenticator
}

func (h *AutomationHandler) SetAfterScheduleChange(callback func() error) {
	h.AfterScheduleChange = callback
}

func (h *AutomationHandler) SetAuthenticator(authenticator *auth.Authenticator) {
	h.Auth = authenticator
}

func (h *AutomationHandler) automationServer(r *http.Request) (*database.Server, error) {
	server, err := h.Store.GetServerByName(mux.Vars(r)["serverName"])
	if err != nil || server == nil {
		return nil, fmt.Errorf("server not found")
	}
	if h.Auth == nil {
		return nil, fmt.Errorf("authentication is not configured")
	}
	if h.Auth.GetUserRole(r) == "admin" {
		return server, nil
	}
	username := h.Auth.GetUsername(r)
	user, _ := h.Store.GetUserByUsername(username)
	var groups []string
	if user != nil {
		scimGroups, _ := h.Store.GetSCIMGroupsForUser(user.ID)
		for _, group := range scimGroups {
			groups = append(groups, group.DisplayName)
		}
	}
	decision, err := h.Store.EvaluateServerAccess(server.ID, username, groups, access.AutomationManage)
	if err != nil {
		return nil, err
	}
	if !decision.Allowed {
		return nil, fmt.Errorf("automation access denied")
	}
	return server, nil
}

func automationPath(server *database.Server) string {
	return "/servers/" + url.PathEscape(server.Name) + "/automation"
}

// redirectAutomationError sends the operator back to the automation page with a
// short, non-sensitive message. Form errors must never surface as a raw
// plain-text database error.
func (h *AutomationHandler) redirectAutomationError(w http.ResponseWriter, r *http.Request, server *database.Server, message string) {
	target := "/"
	if server != nil {
		target = automationPath(server)
	}
	http.Redirect(w, r, target+"?error="+url.QueryEscape(message), http.StatusSeeOther)
}

// automationSaveError maps a persistence failure to an operator-facing message.
func automationSaveError(err error, name string) string {
	if strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Sprintf("An automation rule named %q already exists on this server.", name)
	}
	return "Could not save the automation rule. Please try again."
}

func redirectCommandManagerError(w http.ResponseWriter, r *http.Request, serverID int, message string) {
	target := "/admin"
	if serverID > 0 {
		target = "/admin/commands/" + strconv.Itoa(serverID)
	}
	http.Redirect(w, r, target+"?error="+url.QueryEscape(message), http.StatusSeeOther)
}

func redirectConstraintError(w http.ResponseWriter, r *http.Request, message string) {
	http.Redirect(w, r, "/admin/constraints?error="+url.QueryEscape(message), http.StatusSeeOther)
}

// saveErrorMessage maps a persistence failure to an operator-facing message.
func saveErrorMessage(err error, subject string) string {
	if strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Sprintf("A %s with that name already exists.", subject)
	}
	return fmt.Sprintf("Could not save the %s. Please try again.", subject)
}

func (h *AutomationHandler) parseAutomationRule(r *http.Request, id, serverID int) (*database.CronJob, error) {
	name := strings.TrimSpace(r.FormValue("name"))
	schedule := strings.TrimSpace(r.FormValue("schedule"))
	action := strings.TrimSpace(r.FormValue("action"))
	condition := strings.TrimSpace(r.FormValue("condition"))
	if condition == "" {
		condition = "true"
	}
	stability, stabilityErr := strconv.Atoi(defaultString(r.FormValue("stability_seconds"), "0"))
	cooldown, cooldownErr := strconv.Atoi(defaultString(r.FormValue("cooldown_seconds"), "0"))
	if name == "" || schedule == "" || serverID <= 0 || action == "" {
		return nil, fmt.Errorf("name, schedule, server, and action are required")
	}
	if action != "start" && action != "stop" && action != "restart" {
		return nil, fmt.Errorf("action must be start, stop, or restart")
	}
	if stabilityErr != nil || cooldownErr != nil || stability < 0 || cooldown < 0 {
		return nil, fmt.Errorf("stability and cooldown must be non-negative seconds")
	}
	parser := robfigcron.NewParser(robfigcron.Second | robfigcron.Minute | robfigcron.Hour | robfigcron.Dom | robfigcron.Month | robfigcron.Dow)
	if _, err := parser.Parse(schedule); err != nil {
		return nil, fmt.Errorf("invalid six-field schedule: %w", err)
	}
	env := map[string]interface{}{
		"server":   hogscron.ServerEnv{},
		"activity": hogscron.ActivityEnv{},
		"time":     engine.TimeEnv{Now: time.Now()},
		"duration": func(string) int { return 0 },
	}
	if _, err := h.Engine.TestExpression(condition, env); err != nil {
		return nil, fmt.Errorf("invalid condition: %w", err)
	}
	params := strings.TrimSpace(r.FormValue("params"))
	if params == "" {
		params = "{}"
	}
	if !json.Valid([]byte(params)) {
		return nil, fmt.Errorf("params must be valid JSON")
	}
	return &database.CronJob{
		ID: id, Name: name, Schedule: schedule, ServerID: serverID, Action: action,
		Params: json.RawMessage(params), ACLRule: r.FormValue("acl_rule"),
		Enabled: r.FormValue("enabled") == "on", Condition: condition,
		StabilitySeconds: stability, CooldownSeconds: cooldown,
	}, nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func NewAutomationHandler(store *database.Store, cfg *config.Config, eng *engine.Engine) *AutomationHandler {
	return &AutomationHandler{Store: store, Config: cfg, Engine: eng}
}

func (h *AutomationHandler) AddCommandSchema(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	serverID, err := strconv.Atoi(r.FormValue("server_id"))
	if err != nil {
		redirectCommandManagerError(w, r, 0, "Invalid server ID.")
		return
	}

	name := r.FormValue("name")
	displayName := r.FormValue("display_name")
	template := r.FormValue("template")
	params := r.FormValue("params")
	aclRule := r.FormValue("acl_rule")
	enabled := r.FormValue("enabled") == "on"

	if name == "" || displayName == "" || template == "" {
		redirectCommandManagerError(w, r, serverID, "Name, display name, and template are required.")
		return
	}

	if params == "" {
		params = "{}"
	}

	cs := &database.CommandSchema{
		ServerID:    serverID,
		Name:        name,
		DisplayName: displayName,
		Template:    template,
		Params:      json.RawMessage(params),
		ACLRule:     aclRule,
		Enabled:     enabled,
	}

	if err := h.Store.CreateCommandSchema(cs); err != nil {
		redirectCommandManagerError(w, r, serverID, saveErrorMessage(err, "command schema"))
		return
	}

	http.Redirect(w, r, "/admin/commands/"+strconv.Itoa(serverID), http.StatusFound)
}

func (h *AutomationHandler) UpdateCommandSchema(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		redirectCommandManagerError(w, r, 0, "Invalid command schema ID.")
		return
	}

	serverID, err := strconv.Atoi(r.FormValue("server_id"))
	if err != nil {
		redirectCommandManagerError(w, r, 0, "Invalid server ID.")
		return
	}

	name := r.FormValue("name")
	displayName := r.FormValue("display_name")
	template := r.FormValue("template")
	params := r.FormValue("params")
	aclRule := r.FormValue("acl_rule")
	enabled := r.FormValue("enabled") == "on"

	if params == "" {
		params = "{}"
	}

	cs := &database.CommandSchema{
		ID:          id,
		ServerID:    serverID,
		Name:        name,
		DisplayName: displayName,
		Template:    template,
		Params:      json.RawMessage(params),
		ACLRule:     aclRule,
		Enabled:     enabled,
	}

	if err := h.Store.UpdateCommandSchema(cs); err != nil {
		redirectCommandManagerError(w, r, serverID, saveErrorMessage(err, "command schema"))
		return
	}

	http.Redirect(w, r, "/admin/commands/"+strconv.Itoa(serverID), http.StatusFound)
}

func (h *AutomationHandler) DeleteCommandSchema(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		redirectCommandManagerError(w, r, 0, "Invalid command schema ID.")
		return
	}
	serverID, _ := strconv.Atoi(r.FormValue("server_id"))

	if err := h.Store.DeleteCommandSchema(id); err != nil {
		redirectCommandManagerError(w, r, serverID, "Could not delete the command schema. Please try again.")
		return
	}

	if serverID > 0 {
		http.Redirect(w, r, "/admin/commands/"+strconv.Itoa(serverID), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusFound)
}

func (h *AutomationHandler) AddConstraint(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")
	condition := r.FormValue("condition")
	mode := r.FormValue("mode")
	strategy := r.FormValue("strategy")
	priority, _ := strconv.Atoi(r.FormValue("priority"))
	enabled := r.FormValue("enabled") == "on"

	if name == "" || condition == "" {
		redirectConstraintError(w, r, "Name and condition are required.")
		return
	}
	if strategy == "" {
		strategy = "deny"
	}
	if mode == "" {
		mode = "require"
	}
	if mode != "require" && mode != "exempt" {
		redirectConstraintError(w, r, "Mode must be require or exempt.")
		return
	}

	c := &database.Constraint{
		Name:        name,
		Description: description,
		Condition:   condition,
		Mode:        mode,
		Strategy:    strategy,
		Priority:    priority,
		Enabled:     enabled,
	}

	if err := h.Store.CreateConstraint(c); err != nil {
		redirectConstraintError(w, r, saveErrorMessage(err, "constraint"))
		return
	}

	http.Redirect(w, r, "/admin/constraints", http.StatusFound)
}

func (h *AutomationHandler) UpdateConstraint(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		redirectConstraintError(w, r, "Invalid constraint ID.")
		return
	}
	existing, err := h.Store.GetConstraint(id)
	if err != nil || existing == nil || existing.ServerID != nil {
		redirectConstraintError(w, r, "Instance constraint not found.")
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")
	condition := r.FormValue("condition")
	mode := r.FormValue("mode")
	strategy := r.FormValue("strategy")
	priority, _ := strconv.Atoi(r.FormValue("priority"))
	enabled := r.FormValue("enabled") == "on"

	if strategy == "" {
		strategy = "deny"
	}
	if mode == "" {
		mode = "require"
	}
	if mode != "require" && mode != "exempt" {
		redirectConstraintError(w, r, "Mode must be require or exempt.")
		return
	}

	c := &database.Constraint{
		ID:          id,
		Name:        name,
		Description: description,
		Condition:   condition,
		Mode:        mode,
		Strategy:    strategy,
		Priority:    priority,
		Enabled:     enabled,
	}

	if err := h.Store.UpdateConstraint(c); err != nil {
		redirectConstraintError(w, r, saveErrorMessage(err, "constraint"))
		return
	}

	http.Redirect(w, r, "/admin/constraints", http.StatusFound)
}

func (h *AutomationHandler) DeleteConstraint(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		redirectConstraintError(w, r, "Invalid constraint ID.")
		return
	}
	existing, err := h.Store.GetConstraint(id)
	if err != nil || existing == nil || existing.ServerID != nil {
		redirectConstraintError(w, r, "Instance constraint not found.")
		return
	}

	if err := h.Store.DeleteConstraint(id); err != nil {
		redirectConstraintError(w, r, "Could not delete the constraint. Please try again.")
		return
	}

	http.Redirect(w, r, "/admin/constraints", http.StatusFound)
}

func (h *AutomationHandler) AddCronJob(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	server, err := h.automationServer(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	j, err := h.parseAutomationRule(r, 0, server.ID)
	if err != nil {
		h.redirectAutomationError(w, r, server, err.Error())
		return
	}

	if err := h.Store.CreateCronJob(j); err != nil {
		h.redirectAutomationError(w, r, server, automationSaveError(err, j.Name))
		return
	}
	if h.AfterScheduleChange != nil {
		if err := h.AfterScheduleChange(); err != nil {
			h.redirectAutomationError(w, r, server, "The rule was saved, but reloading the scheduler failed. Check the logs.")
			return
		}
	}

	http.Redirect(w, r, automationPath(server), http.StatusFound)
}

func (h *AutomationHandler) UpdateCronJob(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid cron job ID", http.StatusBadRequest)
		return
	}

	server, err := h.automationServer(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	existing, getErr := h.Store.GetCronJob(id)
	if getErr != nil || existing == nil || existing.ServerID != server.ID {
		http.Error(w, "Automation rule not found", http.StatusNotFound)
		return
	}
	j, err := h.parseAutomationRule(r, id, server.ID)
	if err != nil {
		h.redirectAutomationError(w, r, server, err.Error())
		return
	}
	j.LastActionAt = existing.LastActionAt
	j.LastRun = existing.LastRun
	j.NextRun = existing.NextRun

	if err := h.Store.UpdateCronJob(j); err != nil {
		h.redirectAutomationError(w, r, server, automationSaveError(err, j.Name))
		return
	}
	if h.AfterScheduleChange != nil {
		if err := h.AfterScheduleChange(); err != nil {
			h.redirectAutomationError(w, r, server, "The rule was saved, but reloading the scheduler failed. Check the logs.")
			return
		}
	}

	http.Redirect(w, r, automationPath(server), http.StatusFound)
}

func (h *AutomationHandler) DeleteCronJob(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id, err := strconv.Atoi(r.FormValue("id"))
	if err != nil {
		http.Error(w, "Invalid cron job ID", http.StatusBadRequest)
		return
	}

	server, err := h.automationServer(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	existing, getErr := h.Store.GetCronJob(id)
	if getErr != nil || existing == nil || existing.ServerID != server.ID {
		http.Error(w, "Automation rule not found", http.StatusNotFound)
		return
	}
	if err := h.Store.DeleteCronJob(id); err != nil {
		h.redirectAutomationError(w, r, server, "Could not delete the automation rule. Please try again.")
		return
	}
	if h.AfterScheduleChange != nil {
		if err := h.AfterScheduleChange(); err != nil {
			h.redirectAutomationError(w, r, server, "The rule was deleted, but reloading the scheduler failed. Check the logs.")
			return
		}
	}

	http.Redirect(w, r, automationPath(server), http.StatusFound)
}

func (h *AutomationHandler) UpdateServerTags(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverID, err := strconv.Atoi(vars["serverId"])
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	tags := r.Form["tags"]
	if err := h.Store.SetServerTags(serverID, tags); err != nil {
		http.Error(w, "Failed to update server tags", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *AutomationHandler) UpdateACLRule(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	serverID, err := strconv.Atoi(vars["serverId"])
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	aclRule := r.FormValue("acl_rule")

	link, err := h.Store.GetPterodactylLink(serverID)
	if err != nil || link == nil {
		http.Error(w, "Server not linked to Pterodactyl", http.StatusNotFound)
		return
	}

	link.ACLRule = aclRule
	if err := h.Store.UpdatePterodactylLink(link); err != nil {
		http.Error(w, "Failed to update ACL rule", http.StatusInternalServerError)
		return
	}

	server, _ := h.Store.GetServer(serverID)
	if server == nil {
		http.Redirect(w, r, "/admin", http.StatusFound)
		return
	}
	http.Redirect(w, r, serverSettingsPath(server), http.StatusFound)
}

func (h *AutomationHandler) GetAuditLog(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 100
	offset := 0
	if v, err := strconv.Atoi(limitStr); err == nil && v > 0 {
		limit = v
	}
	if v, err := strconv.Atoi(offsetStr); err == nil && v >= 0 {
		offset = v
	}

	entries, err := h.Store.ListAuditLog(limit, offset)
	if err != nil {
		log.Printf("Failed to fetch audit log: %v", err)
		http.Error(w, "Failed to fetch audit log", http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []database.AuditLogEntry{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"entries": entries,
		"limit":   limit,
		"offset":  offset,
	})
}

func (h *AutomationHandler) ExportAuditLog(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}

	// Limit export to prevent memory exhaustion
	const maxExportLimit = 5000
	entries, err := h.Store.ListAuditLog(maxExportLimit, 0)
	if err != nil {
		log.Printf("Failed to export audit log: %v", err)
		http.Error(w, "Failed to fetch audit log", http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []database.AuditLogEntry{}
	}

	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=audit_log.csv")
		w.Write([]byte("timestamp,user_username,server_name,action,params,result,reason,source,client_ip,country_code\n"))
		for _, e := range entries {
			line := fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n",
				escapeCSV(e.Timestamp), escapeCSV(e.UserUsername), escapeCSV(e.ServerName), escapeCSV(e.Action),
				escapeCSV(string(e.Params)), escapeCSV(e.Result), escapeCSV(e.Reason), escapeCSV(e.Source),
				escapeCSV(e.ClientIP), escapeCSV(e.CountryCode))
			w.Write([]byte(line))
		}
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=audit_log.json")
		json.NewEncoder(w).Encode(entries)
	}
}

func escapeCSV(field string) string {
	// Prevent CSV injection by prefixing formula-triggering characters
	if len(field) > 0 {
		switch field[0] {
		case '=', '+', '-', '@', '\t', '\r':
			field = "'" + field
		}
	}
	// Quote fields containing commas, quotes, or newlines
	if strings.ContainsAny(field, ",\"\n\r") {
		field = strings.ReplaceAll(field, "\"", "\"\"")
		field = "\"" + field + "\""
	}
	return field
}

func (h *AutomationHandler) TestConstraint(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit

	var req struct {
		Condition string           `json:"condition"`
		Server    engine.ServerEnv `json:"server"`
		User      engine.UserEnv   `json:"user"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	env := map[string]interface{}{
		"server": req.Server,
		"user":   req.User,
		"time": engine.TimeEnv{
			Now:     time.Now(),
			Hour:    time.Now().Hour(),
			Weekday: time.Now().Weekday(),
		},
		"hasTag":       engine.HasTag,
		"countRunning": engine.CountRunning,
		"filterByTag":  engine.FilterByTag,
		"weekday":      engine.ParseWeekday,
		"ipInCIDR":     engine.IPInCIDR,
		"ipInAnyCIDR":  engine.IPInAnyCIDR,
	}

	result, err := h.Engine.TestExpression(req.Condition, env)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"result": result})
}

func (h *AutomationHandler) CleanupAuditLog(w http.ResponseWriter, r *http.Request) {
	if err := h.Store.CleanupAuditLog(h.Config.AuditLogRetentionDays); err != nil {
		log.Printf("Warning: audit log cleanup failed: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *AutomationHandler) BulkTags(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Servers []string `json:"servers"`
		Tags    []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	var updated int
	for _, name := range req.Servers {
		server, err := h.Store.GetServerByName(name)
		if err != nil || server == nil {
			continue
		}
		if err := h.Store.SetServerTags(server.ID, req.Tags); err != nil {
			log.Printf("BulkTags: failed to set tags for %s: %v", name, err)
			continue
		}
		updated++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": fmt.Sprintf("Updated tags for %d/%d servers", updated, len(req.Servers)),
		"updated": updated,
	})
}

func (h *AutomationHandler) BulkACL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Servers []string `json:"servers"`
		ACLRule string   `json:"acl_rule"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	var updated int
	for _, name := range req.Servers {
		server, err := h.Store.GetServerByName(name)
		if err != nil || server == nil {
			continue
		}
		link, err := h.Store.GetPterodactylLink(server.ID)
		if err != nil || link == nil {
			continue
		}
		link.ACLRule = req.ACLRule
		if err := h.Store.UpdatePterodactylLink(link); err != nil {
			log.Printf("BulkACL: failed to update ACL for %s: %v", name, err)
			continue
		}
		updated++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": fmt.Sprintf("Updated ACL for %d/%d servers", updated, len(req.Servers)),
		"updated": updated,
	})
}
