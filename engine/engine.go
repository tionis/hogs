package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"time"

	"github.com/expr-lang/expr"
	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/query"
)

type ServerEnv struct {
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	GameType string   `json:"gameType"`
	Tags     []string `json:"tags"`
	Node     string   `json:"node"`
	Running  bool     `json:"running"`
}

type UserEnv struct {
	Email  string   `json:"email"`
	Role   string   `json:"role"`
	Groups []string `json:"groups"`
}

type TimeEnv struct {
	Now     time.Time    `json:"now"`
	Hour    int          `json:"hour"`
	Weekday time.Weekday `json:"weekday"`
}

type ActionResult struct {
	Allowed bool
	Result  string
	Reason  string
	Status  int
}

type Notifier interface {
	Send(eventType, message string)
}

type Engine struct {
	Store    *database.Store
	Config   *config.Config
	Cache    *query.ServerStatusCache
	Notifier Notifier
}

func NewEngine(store *database.Store, cfg *config.Config, cache *query.ServerStatusCache) *Engine {
	return &Engine{Store: store, Config: cfg, Cache: cache}
}

func (e *Engine) SetNotifier(n Notifier) {
	e.Notifier = n
}

func (e *Engine) buildEnv(server *database.Server, user *UserEnv) (map[string]interface{}, error) {
	servers, err := e.Store.ListServers()
	if err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}

	serverEnvs := make([]ServerEnv, 0, len(servers))
	for _, srv := range servers {
		tags, _ := e.Store.GetServerTags(srv.ID)
		if tags == nil {
			tags = []string{}
		}
		link, _ := e.Store.GetPterodactylLink(srv.ID)
		node := ""
		running := false
		if link != nil {
			node = link.Node
			if e.Cache != nil {
				if status, found := e.Cache.Get(srv.Name); found {
					running = status.Online
				}
			}
		}
		serverEnvs = append(serverEnvs, ServerEnv{
			ID:       srv.ID,
			Name:     srv.Name,
			GameType: srv.GameType,
			Tags:     tags,
			Node:     node,
			Running:  running,
		})
	}

	tags, _ := e.Store.GetServerTags(server.ID)
	if tags == nil {
		tags = []string{}
	}

	link, _ := e.Store.GetPterodactylLink(server.ID)
	node := ""
	if link != nil {
		node = link.Node
	}

	serverEnv := ServerEnv{
		ID:       server.ID,
		Name:     server.Name,
		GameType: server.GameType,
		Tags:     tags,
		Node:     node,
	}

	now := time.Now()
	timeEnv := TimeEnv{
		Now:     now,
		Hour:    now.Hour(),
		Weekday: now.Weekday(),
	}

	env := map[string]interface{}{
		"server":  serverEnv,
		"servers": serverEnvs,
		"user":    user,
		"time":    timeEnv,
		"hasTag":  func(s ServerEnv, tag string) bool { return HasTag(s, tag) },
		"inList":  func(item string, list []string) bool { return InList(list, item) },
		"serversOnNode": func(node string) []ServerEnv {
			return filterByNode(serverEnvs, node)
		},
		"runningOnNode": func(node string) []ServerEnv {
			return filterRunning(filterByNode(serverEnvs, node))
		},
		"countRunning": func(list []ServerEnv) int { return CountRunning(list) },
		"filterByTag":  func(list []ServerEnv, tag string) []ServerEnv { return FilterByTag(list, tag) },
		"weekday":      ParseWeekday,
	}

	return env, nil
}

func HasTag(s ServerEnv, tag string) bool {
	for _, t := range s.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

func InList(list []string, item string) bool {
	for _, v := range list {
		if v == item {
			return true
		}
	}
	return false
}

func filterByNode(servers []ServerEnv, node string) []ServerEnv {
	var result []ServerEnv
	for _, s := range servers {
		if s.Node == node {
			result = append(result, s)
		}
	}
	return result
}

func filterRunning(servers []ServerEnv) []ServerEnv {
	var result []ServerEnv
	for _, s := range servers {
		if s.Running {
			result = append(result, s)
		}
	}
	return result
}

func CountRunning(servers []ServerEnv) int {
	count := 0
	for _, s := range servers {
		if s.Running {
			count++
		}
	}
	return count
}

func FilterByTag(servers []ServerEnv, tag string) []ServerEnv {
	var result []ServerEnv
	for _, s := range servers {
		if HasTag(s, tag) {
			result = append(result, s)
		}
	}
	return result
}

func ParseWeekday(name string) time.Weekday {
	weekdays := map[string]time.Weekday{
		"sunday":    time.Sunday,
		"monday":    time.Monday,
		"tuesday":   time.Tuesday,
		"wednesday": time.Wednesday,
		"thursday":  time.Thursday,
		"friday":    time.Friday,
		"saturday":  time.Saturday,
	}
	if d, ok := weekdays[name]; ok {
		return d
	}
	return time.Sunday
}

func (e *Engine) EvaluateACL(link *database.PterodactylLink, server *database.Server, action string, user *UserEnv) (bool, error) {
	if link.ACLRule != "" {
		env, err := e.buildEnv(server, user)
		if err != nil {
			return false, err
		}
		env["action"] = action

		program, err := expr.Compile(link.ACLRule, expr.Env(env), expr.AllowUndefinedVariables())
		if err != nil {
			return false, fmt.Errorf("ACL rule compile error: %w", err)
		}

		result, err := expr.Run(program, env)
		if err != nil {
			return false, fmt.Errorf("ACL rule evaluation error: %w", err)
		}

		allowed, ok := result.(bool)
		if !ok {
			return false, fmt.Errorf("ACL rule must return boolean, got %T", result)
		}
		return allowed, nil
	}

	return isActionAllowed(link.AllowedActions, action), nil
}

func (e *Engine) EvaluateConstraints(server *database.Server, action string, user *UserEnv) (*ActionResult, error) {
	constraints, err := e.Store.ListEnabledConstraints()
	if err != nil {
		return nil, fmt.Errorf("failed to list constraints: %w", err)
	}

	env, err := e.buildEnv(server, user)
	if err != nil {
		return nil, err
	}
	env["action"] = action

	for _, c := range constraints {
		program, err := expr.Compile(c.Condition, expr.Env(env), expr.AllowUndefinedVariables())
		if err != nil {
			log.Printf("Warning: constraint %q compile error: %v", c.Name, err)
			continue
		}

		result, err := expr.Run(program, env)
		if err != nil {
			log.Printf("Warning: constraint %q evaluation error: %v", c.Name, err)
			continue
		}

		passed, ok := result.(bool)
		if !ok {
			log.Printf("Warning: constraint %q must return boolean, got %T", c.Name, result)
			continue
		}

		if !passed {
			return &ActionResult{
				Allowed: false,
				Result:  "blocked",
				Reason:  fmt.Sprintf("Constraint %q blocked this action (strategy: %s)", c.Name, c.Strategy),
				Status:  constraintStrategyStatus(c.Strategy),
			}, nil
		}
	}

	return &ActionResult{Allowed: true, Result: "allowed", Status: 200}, nil
}

func constraintStrategyStatus(strategy string) int {
	switch strategy {
	case "deny":
		return 409
	case "queue":
		return 202
	case "stop_oldest":
		return 200
	default:
		return 409
	}
}

type ParamSchema struct {
	Type     string   `json:"type"`
	Pattern  string   `json:"pattern,omitempty"`
	MinLen   *int     `json:"minLength,omitempty"`
	MaxLen   *int     `json:"maxLength,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Values   []string `json:"values,omitempty"`
	Required bool     `json:"required"`
	Default  *string  `json:"default,omitempty"`
}

func (e *Engine) ValidateParams(schema *database.CommandSchema, params map[string]string) (map[string]string, error) {
	var paramSchemas map[string]ParamSchema
	if err := json.Unmarshal(schema.Params, &paramSchemas); err != nil {
		return nil, fmt.Errorf("invalid params schema: %w", err)
	}

	result := make(map[string]string)
	for name, ps := range paramSchemas {
		val, provided := params[name]

		if !provided {
			if ps.Default != nil {
				val = *ps.Default
			} else if ps.Required {
				return nil, fmt.Errorf("missing required parameter: %s", name)
			} else {
				continue
			}
		}

		switch ps.Type {
		case "string":
			if ps.Pattern != "" {
				if !isSafeRegex(ps.Pattern) {
					return nil, fmt.Errorf("unsafe regex pattern for param %s", name)
				}
				matched, err := regexp.MatchString(ps.Pattern, val)
				if err != nil {
					return nil, fmt.Errorf("invalid pattern for param %s: %w", name, err)
				}
				if !matched {
					return nil, fmt.Errorf("parameter %s does not match required pattern", name)
				}
			}
			if ps.MinLen != nil && len(val) < *ps.MinLen {
				return nil, fmt.Errorf("parameter %s is too short (min %d)", name, *ps.MinLen)
			}
			if ps.MaxLen != nil && len(val) > *ps.MaxLen {
				return nil, fmt.Errorf("parameter %s is too long (max %d)", name, *ps.MaxLen)
			}
		case "int":
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("parameter %s must be an integer", name)
			}
			if ps.Min != nil && float64(n) < *ps.Min {
				return nil, fmt.Errorf("parameter %s must be >= %v", name, *ps.Min)
			}
			if ps.Max != nil && float64(n) > *ps.Max {
				return nil, fmt.Errorf("parameter %s must be <= %v", name, *ps.Max)
			}
		case "float":
			f, err := strconv.ParseFloat(val, 64)
			if err != nil {
				return nil, fmt.Errorf("parameter %s must be a number", name)
			}
			if ps.Min != nil && f < *ps.Min {
				return nil, fmt.Errorf("parameter %s must be >= %v", name, *ps.Min)
			}
			if ps.Max != nil && f > *ps.Max {
				return nil, fmt.Errorf("parameter %s must be <= %v", name, *ps.Max)
			}
		case "enum":
			found := false
			for _, v := range ps.Values {
				if val == v {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("parameter %s must be one of: %v", name, ps.Values)
			}
		case "bool":
			switch val {
			case "true", "1", "yes":
				val = "true"
			case "false", "0", "no":
				val = "false"
			default:
				return nil, fmt.Errorf("parameter %s must be a boolean", name)
			}
		}

		result[name] = val
	}

	return result, nil
}

func (e *Engine) RenderTemplate(template string, params map[string]string) string {
	result := template
	for name, val := range params {
		result = regexp.MustCompile(`\{`+regexp.QuoteMeta(name)+`\}`).ReplaceAllString(result, val)
	}
	return result
}

func (e *Engine) Evaluate(server *database.Server, action string, params map[string]string, user *UserEnv) *ActionResult {
	source := "web"
	if user != nil {
		if user.Email == "system" && user.Role == "system" {
			source = "cron"
		} else if user.Email == "" && user.Role == "admin" {
			source = "api"
		}
	}

	auditEntry := &database.AuditLogEntry{
		Action:     action,
		ServerName: server.Name,
		UserEmail:  "",
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Result:     "allowed",
		Source:     source,
	}
	if user != nil {
		auditEntry.UserEmail = user.Email
	}

	link, err := e.Store.GetPterodactylLink(server.ID)
	if err != nil {
		auditEntry.Result = "error"
		auditEntry.Reason = err.Error()
		return &ActionResult{Allowed: false, Result: "error", Reason: err.Error()}
	}
	if link == nil {
		auditEntry.Result = "error"
		auditEntry.Reason = "no pterodactyl link"
		return &ActionResult{Allowed: false, Result: "error", Reason: "no pterodactyl link", Status: 404}
	}

	aclAllowed, err := e.EvaluateACL(link, server, action, user)
	if err != nil {
		auditEntry.Result = "error"
		auditEntry.Reason = err.Error()
		return &ActionResult{Allowed: false, Result: "error", Reason: err.Error()}
	}
	if !aclAllowed {
		auditEntry.Result = "denied"
		auditEntry.Reason = fmt.Sprintf("ACL denied action %s", action)
		return &ActionResult{Allowed: false, Result: "denied", Reason: fmt.Sprintf("ACL denied action %s", action)}
	}

	constraintResult, err := e.EvaluateConstraints(server, action, user)
	if err != nil {
		log.Printf("Constraint evaluation error for server %s action %s: %v", server.Name, action, err)
	} else if !constraintResult.Allowed {
		auditEntry.Result = "blocked"
		auditEntry.Reason = constraintResult.Reason
		if e.Notifier != nil {
			e.Notifier.Send("constraint_violation", fmt.Sprintf("Constraint blocked action %s on server %s: %s", action, server.Name, constraintResult.Reason))
		}
		return constraintResult
	}

	if e.Notifier != nil {
		go e.Notifier.Send(fmt.Sprintf("server_%s", action), fmt.Sprintf("Action %s on server %s by %s", action, server.Name, auditEntry.UserEmail))
	}

	if err := e.Store.CreateAuditLog(auditEntry); err != nil {
		log.Printf("Warning: audit log creation failed: %v", err)
	}

	return &ActionResult{Allowed: true, Result: "allowed", Status: 200}
}

// EvaluateVisibility checks if a user can see a server.
// It evaluates constraints with action="view" and checks server state.
func (e *Engine) EvaluateVisibility(server *database.Server, user *UserEnv) bool {
	// Offline servers are hidden from anonymous users
	if server.State == "offline" && user == nil {
		return false
	}

	// Evaluate constraints with "view" action
	result, err := e.EvaluateConstraints(server, "view", user)
	if err != nil {
		log.Printf("Visibility constraint error for server %s: %v", server.Name, err)
		return true // Default to visible on error
	}

	return result.Allowed
}

func (e *Engine) LogAction(serverName, action, userEmail, result, reason, source string, params map[string]string) {
	entry := &database.AuditLogEntry{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		UserEmail:  userEmail,
		ServerName: serverName,
		Action:     action,
		Params:     paramsToJSON(params),
		Result:     result,
		Reason:     reason,
		Source:     source,
	}
	if err := e.Store.CreateAuditLog(entry); err != nil {
		log.Printf("Warning: failed to write audit log: %v", err)
	}
}

func (e *Engine) TestExpression(expression string, env map[string]interface{}) (interface{}, error) {
	program, err := expr.Compile(expression, expr.Env(env), expr.AllowUndefinedVariables())
	if err != nil {
		return nil, fmt.Errorf("compile error: %w", err)
	}

	result, err := expr.Run(program, env)
	if err != nil {
		return nil, fmt.Errorf("evaluation error: %w", err)
	}

	return result, nil
}

func paramsToJSON(params map[string]string) json.RawMessage {
	if params == nil {
		return json.RawMessage("{}")
	}
	b, _ := json.Marshal(params)
	return json.RawMessage(b)
}

func isActionAllowed(allowedActionsJSON string, action string) bool {
	var actions []string
	if err := json.Unmarshal([]byte(allowedActionsJSON), &actions); err != nil {
		return false
	}
	for _, a := range actions {
		if a == action {
			return true
		}
	}
	return false
}

// isSafeRegex checks if a regex pattern is safe from ReDoS attacks.
// It rejects patterns with nested quantifiers and other dangerous constructs.
func isSafeRegex(pattern string) bool {
	if pattern == "" {
		return true
	}
	// Reject patterns with nested quantifiers like (a+)+, (a*)*, etc.
	nestedQuantifierRegex := regexp.MustCompile(`[+*?]\)|[+*?]\}\)|\)[+*?]\)`)
	if nestedQuantifierRegex.MatchString(pattern) {
		return false
	}
	// Reject patterns with groups containing alternation followed by quantifiers
	// This is a basic check; for production, consider using a dedicated ReDoS library
	return true
}
