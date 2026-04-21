package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
)

type AutomationHandler struct {
	Store  *database.Store
	Config *config.Config
	Engine *engine.Engine
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
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	displayName := r.FormValue("display_name")
	template := r.FormValue("template")
	params := r.FormValue("params")
	aclRule := r.FormValue("acl_rule")
	enabled := r.FormValue("enabled") == "on"

	if name == "" || displayName == "" || template == "" {
		http.Error(w, "name, display_name, and template are required", http.StatusBadRequest)
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
		http.Error(w, "Failed to create command schema: "+err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "Invalid command schema ID", http.StatusBadRequest)
		return
	}

	serverID, err := strconv.Atoi(r.FormValue("server_id"))
	if err != nil {
		http.Error(w, "Invalid server ID", http.StatusBadRequest)
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
		http.Error(w, "Failed to update command schema: "+err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "Invalid command schema ID", http.StatusBadRequest)
		return
	}

	if err := h.Store.DeleteCommandSchema(id); err != nil {
		http.Error(w, "Failed to delete command schema", http.StatusInternalServerError)
		return
	}

	serverIDStr := r.FormValue("server_id")
	if serverIDStr != "" {
		http.Redirect(w, r, "/admin/commands/"+serverIDStr, http.StatusFound)
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
	strategy := r.FormValue("strategy")
	priority, _ := strconv.Atoi(r.FormValue("priority"))
	enabled := r.FormValue("enabled") == "on"

	if name == "" || condition == "" {
		http.Error(w, "name and condition are required", http.StatusBadRequest)
		return
	}
	if strategy == "" {
		strategy = "deny"
	}

	c := &database.Constraint{
		Name:        name,
		Description: description,
		Condition:   condition,
		Strategy:    strategy,
		Priority:    priority,
		Enabled:     enabled,
	}

	if err := h.Store.CreateConstraint(c); err != nil {
		http.Error(w, "Failed to create constraint: "+err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "Invalid constraint ID", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")
	condition := r.FormValue("condition")
	strategy := r.FormValue("strategy")
	priority, _ := strconv.Atoi(r.FormValue("priority"))
	enabled := r.FormValue("enabled") == "on"

	if strategy == "" {
		strategy = "deny"
	}

	c := &database.Constraint{
		ID:          id,
		Name:        name,
		Description: description,
		Condition:   condition,
		Strategy:    strategy,
		Priority:    priority,
		Enabled:     enabled,
	}

	if err := h.Store.UpdateConstraint(c); err != nil {
		http.Error(w, "Failed to update constraint: "+err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "Invalid constraint ID", http.StatusBadRequest)
		return
	}

	if err := h.Store.DeleteConstraint(id); err != nil {
		http.Error(w, "Failed to delete constraint", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/constraints", http.StatusFound)
}

func (h *AutomationHandler) AddCronJob(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	schedule := r.FormValue("schedule")
	serverName := r.FormValue("server_name")
	action := r.FormValue("action")
	params := r.FormValue("params")
	aclRule := r.FormValue("acl_rule")
	enabled := r.FormValue("enabled") == "on"

	if name == "" || schedule == "" || serverName == "" || action == "" {
		http.Error(w, "name, schedule, server_name, and action are required", http.StatusBadRequest)
		return
	}
	if params == "" {
		params = "{}"
	}

	j := &database.CronJob{
		Name:       name,
		Schedule:   schedule,
		ServerName: serverName,
		Action:     action,
		Params:     json.RawMessage(params),
		ACLRule:    aclRule,
		Enabled:    enabled,
	}

	if err := h.Store.CreateCronJob(j); err != nil {
		http.Error(w, "Failed to create cron job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/cron", http.StatusFound)
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

	name := r.FormValue("name")
	schedule := r.FormValue("schedule")
	serverName := r.FormValue("server_name")
	action := r.FormValue("action")
	params := r.FormValue("params")
	aclRule := r.FormValue("acl_rule")
	enabled := r.FormValue("enabled") == "on"

	if params == "" {
		params = "{}"
	}

	j := &database.CronJob{
		ID:         id,
		Name:       name,
		Schedule:   schedule,
		ServerName: serverName,
		Action:     action,
		Params:     json.RawMessage(params),
		ACLRule:    aclRule,
		Enabled:    enabled,
	}

	if err := h.Store.UpdateCronJob(j); err != nil {
		http.Error(w, "Failed to update cron job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/cron", http.StatusFound)
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

	if err := h.Store.DeleteCronJob(id); err != nil {
		http.Error(w, "Failed to delete cron job", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/admin/cron", http.StatusFound)
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

	http.Redirect(w, r, "/admin/servers/"+strconv.Itoa(serverID), http.StatusFound)
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

	entries, err := h.Store.ListAuditLog(10000, 0)
	if err != nil {
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
		w.Write([]byte("timestamp,user_email,server_name,action,params,result,reason,source\n"))
		for _, e := range entries {
			w.Write([]byte(fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s,%s\n",
				e.Timestamp, e.UserEmail, e.ServerName, e.Action,
				string(e.Params), e.Result, e.Reason, e.Source)))
		}
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=audit_log.json")
		json.NewEncoder(w).Encode(entries)
	}
}

func (h *AutomationHandler) TestConstraint(w http.ResponseWriter, r *http.Request) {
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
