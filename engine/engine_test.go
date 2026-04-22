package engine

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/query"
)

func testEngine(t *testing.T) *Engine {
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
		AuditLogRetentionDays: 90,
	}
	cache := query.NewServerStatusCache()
	return NewEngine(store, cfg, cache)
}

func testEngineStore(t *testing.T) *database.Store {
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
	return store
}

func TestHasTag(t *testing.T) {
	s := ServerEnv{Tags: []string{"minecraft", "creative"}}
	if !HasTag(s, "minecraft") {
		t.Error("expected HasTag to find minecraft")
	}
	if HasTag(s, "vanilla") {
		t.Error("expected HasTag not to find vanilla")
	}
}

func TestHasTagEmpty(t *testing.T) {
	s := ServerEnv{Tags: []string{}}
	if HasTag(s, "anything") {
		t.Error("expected HasTag to return false for empty tags")
	}
}

func TestCountRunning(t *testing.T) {
	servers := []ServerEnv{
		{Name: "a", Running: true},
		{Name: "b", Running: false},
		{Name: "c", Running: true},
	}
	if got := CountRunning(servers); got != 2 {
		t.Errorf("CountRunning = %d, want 2", got)
	}
}

func TestCountRunningNone(t *testing.T) {
	servers := []ServerEnv{
		{Name: "a", Running: false},
	}
	if got := CountRunning(servers); got != 0 {
		t.Errorf("CountRunning = %d, want 0", got)
	}
}

func TestFilterByTag(t *testing.T) {
	servers := []ServerEnv{
		{Name: "a", Tags: []string{"minecraft", "creative"}},
		{Name: "b", Tags: []string{"valheim"}},
		{Name: "c", Tags: []string{"minecraft", "survival"}},
	}
	filtered := FilterByTag(servers, "minecraft")
	if len(filtered) != 2 {
		t.Errorf("FilterByTag = %d results, want 2", len(filtered))
	}
}

func TestFilterByTagNone(t *testing.T) {
	servers := []ServerEnv{
		{Name: "a", Tags: []string{"valheim"}},
	}
	filtered := FilterByTag(servers, "minecraft")
	if len(filtered) != 0 {
		t.Errorf("FilterByTag = %d results, want 0", len(filtered))
	}
}

func TestParseWeekday(t *testing.T) {
	tests := []struct {
		input string
		want  time.Weekday
	}{
		{"monday", time.Monday},
		{"tuesday", time.Tuesday},
		{"wednesday", time.Wednesday},
		{"thursday", time.Thursday},
		{"friday", time.Friday},
		{"saturday", time.Saturday},
		{"sunday", time.Sunday},
		{"invalid", time.Sunday},
	}
	for _, tt := range tests {
		got := ParseWeekday(tt.input)
		if got != tt.want {
			t.Errorf("ParseWeekday(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestRenderTemplate(t *testing.T) {
	e := &Engine{}
	result := e.RenderTemplate("whitelist add {player}", map[string]string{"player": "Steve"})
	if result != "whitelist add Steve" {
		t.Errorf("RenderTemplate = %q, want %q", result, "whitelist add Steve")
	}
}

func TestRenderTemplateMultipleParams(t *testing.T) {
	e := &Engine{}
	result := e.RenderTemplate("say {msg} to {target}", map[string]string{"msg": "hello", "target": "world"})
	if result != "say hello to world" {
		t.Errorf("RenderTemplate = %q, want %q", result, "say hello to world")
	}
}

func TestRenderTemplateNoMatch(t *testing.T) {
	e := &Engine{}
	result := e.RenderTemplate("say {msg}", map[string]string{"other": "val"})
	if result != "say {msg}" {
		t.Errorf("RenderTemplate = %q, want %q", result, "say {msg}")
	}
}

func TestValidateParamsStringType(t *testing.T) {
	e := &Engine{}
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"player": map[string]interface{}{
				"type":     "string",
				"required": true,
			},
		}),
	}

	result, err := e.ValidateParams(schema, map[string]string{"player": "Steve"})
	if err != nil {
		t.Fatalf("ValidateParams error: %v", err)
	}
	if result["player"] != "Steve" {
		t.Errorf("player = %q, want %q", result["player"], "Steve")
	}
}

func TestValidateParamsMissingRequired(t *testing.T) {
	e := &Engine{}
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"player": map[string]interface{}{
				"type":     "string",
				"required": true,
			},
		}),
	}

	_, err := e.ValidateParams(schema, map[string]string{})
	if err == nil {
		t.Error("expected error for missing required param")
	}
}

func TestValidateParamsDefault(t *testing.T) {
	e := &Engine{}
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"mode": map[string]interface{}{
				"type":    "string",
				"default": "survival",
			},
		}),
	}

	result, err := e.ValidateParams(schema, map[string]string{})
	if err != nil {
		t.Fatalf("ValidateParams error: %v", err)
	}
	if result["mode"] != "survival" {
		t.Errorf("mode = %q, want %q", result["mode"], "survival")
	}
}

func TestValidateParamsIntType(t *testing.T) {
	e := &Engine{}
	min := float64(1)
	max := float64(100)
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"count": map[string]interface{}{
				"type": "int",
				"min":  min,
				"max":  max,
			},
		}),
	}

	result, err := e.ValidateParams(schema, map[string]string{"count": "50"})
	if err != nil {
		t.Fatalf("ValidateParams error: %v", err)
	}
	if result["count"] != "50" {
		t.Errorf("count = %q, want %q", result["count"], "50")
	}
}

func TestValidateParamsIntOutOfRange(t *testing.T) {
	e := &Engine{}
	min := float64(1)
	max := float64(100)
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"count": map[string]interface{}{
				"type": "int",
				"min":  min,
				"max":  max,
			},
		}),
	}

	_, err := e.ValidateParams(schema, map[string]string{"count": "200"})
	if err == nil {
		t.Error("expected error for int out of range")
	}
}

func TestValidateParamsInvalidInt(t *testing.T) {
	e := &Engine{}
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"count": map[string]interface{}{
				"type": "int",
			},
		}),
	}

	_, err := e.ValidateParams(schema, map[string]string{"count": "abc"})
	if err == nil {
		t.Error("expected error for invalid int")
	}
}

func TestValidateParamsEnumType(t *testing.T) {
	e := &Engine{}
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"difficulty": map[string]interface{}{
				"type":   "enum",
				"values": []string{"peaceful", "easy", "normal", "hard"},
			},
		}),
	}

	result, err := e.ValidateParams(schema, map[string]string{"difficulty": "hard"})
	if err != nil {
		t.Fatalf("ValidateParams error: %v", err)
	}
	if result["difficulty"] != "hard" {
		t.Errorf("difficulty = %q, want %q", result["difficulty"], "hard")
	}
}

func TestValidateParamsEnumInvalid(t *testing.T) {
	e := &Engine{}
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"difficulty": map[string]interface{}{
				"type":   "enum",
				"values": []string{"peaceful", "easy", "normal", "hard"},
			},
		}),
	}

	_, err := e.ValidateParams(schema, map[string]string{"difficulty": "extreme"})
	if err == nil {
		t.Error("expected error for invalid enum value")
	}
}

func TestValidateParamsBoolType(t *testing.T) {
	e := &Engine{}
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"flag": map[string]interface{}{
				"type": "bool",
			},
		}),
	}

	tests := []struct{ input, want string }{
		{"true", "true"},
		{"1", "true"},
		{"yes", "true"},
		{"false", "false"},
		{"0", "false"},
		{"no", "false"},
	}
	for _, tt := range tests {
		result, err := e.ValidateParams(schema, map[string]string{"flag": tt.input})
		if err != nil {
			t.Errorf("ValidateParams(%q) error: %v", tt.input, err)
			continue
		}
		if result["flag"] != tt.want {
			t.Errorf("ValidateParams(%q) = %q, want %q", tt.input, result["flag"], tt.want)
		}
	}
}

func TestValidateParamsBoolInvalid(t *testing.T) {
	e := &Engine{}
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"flag": map[string]interface{}{
				"type": "bool",
			},
		}),
	}

	_, err := e.ValidateParams(schema, map[string]string{"flag": "maybe"})
	if err == nil {
		t.Error("expected error for invalid bool")
	}
}

func TestValidateParamsStringPattern(t *testing.T) {
	e := &Engine{}
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"player": map[string]interface{}{
				"type":    "string",
				"pattern": "^[a-zA-Z0-9_]{1,16}$",
			},
		}),
	}

	_, err := e.ValidateParams(schema, map[string]string{"player": "valid_name"})
	if err != nil {
		t.Errorf("ValidateParams valid name error: %v", err)
	}

	_, err = e.ValidateParams(schema, map[string]string{"player": "invalid name!"})
	if err == nil {
		t.Error("expected error for pattern mismatch")
	}
}

func TestValidateParamsStringLength(t *testing.T) {
	e := &Engine{}
	minLen := 3
	maxLen := 10
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"name": map[string]interface{}{
				"type":      "string",
				"minLength": minLen,
				"maxLength": maxLen,
			},
		}),
	}

	_, err := e.ValidateParams(schema, map[string]string{"name": "ab"})
	if err == nil {
		t.Error("expected error for string too short")
	}

	_, err = e.ValidateParams(schema, map[string]string{"name": "a"})
	if err == nil {
		t.Error("expected error for string too short")
	}

	_, err = e.ValidateParams(schema, map[string]string{"name": "validname"})
	if err != nil {
		t.Errorf("ValidateParams valid length error: %v", err)
	}
}

func TestValidateParamsFloatType(t *testing.T) {
	e := &Engine{}
	min := float64(0.0)
	max := float64(1.0)
	schema := &database.CommandSchema{
		Params: jsonRawMessage(map[string]interface{}{
			"ratio": map[string]interface{}{
				"type": "float",
				"min":  min,
				"max":  max,
			},
		}),
	}

	result, err := e.ValidateParams(schema, map[string]string{"ratio": "0.5"})
	if err != nil {
		t.Fatalf("ValidateParams error: %v", err)
	}
	if result["ratio"] != "0.5" {
		t.Errorf("ratio = %q, want %q", result["ratio"], "0.5")
	}
}

func TestEvaluateACLAllowedActions(t *testing.T) {
	eng := testEngine(t)
	store := eng.Store

	srv := &database.Server{Name: "test", Address: "t:25565", GameType: "minecraft", State: "online"}
	store.CreateServer(srv)
	created, _ := store.GetServerByName("test")

	link := &database.PterodactylLink{
		ServerID:       created.ID,
		PteroServerID:  "abc",
		AllowedActions: `["start","stop"]`,
	}
	store.CreatePterodactylLink(link)

	user := &UserEnv{Email: "admin@example.com", Role: "admin"}

	allowed, err := eng.EvaluateACL(link, created, "start", user)
	if err != nil {
		t.Fatalf("EvaluateACL error: %v", err)
	}
	if !allowed {
		t.Error("expected start to be allowed")
	}

	allowed, err = eng.EvaluateACL(link, created, "restart", user)
	if err != nil {
		t.Fatalf("EvaluateACL error: %v", err)
	}
	if allowed {
		t.Error("expected restart to be denied")
	}
}

func TestEvaluateACLExpressionRule(t *testing.T) {
	eng := testEngine(t)
	store := eng.Store

	srv := &database.Server{Name: "test", Address: "t:25565", GameType: "minecraft", State: "online"}
	store.CreateServer(srv)
	created, _ := store.GetServerByName("test")

	link := &database.PterodactylLink{
		ServerID:       created.ID,
		PteroServerID:  "abc",
		AllowedActions: `["start","stop"]`,
		ACLRule:        `action == "start" and user.Role == "admin"`,
	}
	store.CreatePterodactylLink(link)

	adminUser := &UserEnv{Email: "admin@example.com", Role: "admin"}
	regularUser := &UserEnv{Email: "user@example.com", Role: "user"}

	allowed, err := eng.EvaluateACL(link, created, "start", adminUser)
	if err != nil {
		t.Fatalf("EvaluateACL error: %v", err)
	}
	if !allowed {
		t.Error("expected admin start to be allowed")
	}

	allowed, err = eng.EvaluateACL(link, created, "start", regularUser)
	if err != nil {
		t.Fatalf("EvaluateACL error: %v", err)
	}
	if allowed {
		t.Error("expected regular user start to be denied by ACL expression")
	}
}

func TestEvaluateConstraintsNoConstraints(t *testing.T) {
	eng := testEngine(t)
	store := eng.Store

	srv := &database.Server{Name: "test", Address: "t:25565", GameType: "minecraft", State: "online"}
	store.CreateServer(srv)
	created, _ := store.GetServerByName("test")

	user := &UserEnv{Email: "admin@example.com", Role: "admin"}

	result, err := eng.EvaluateConstraints(created, "start", user)
	if err != nil {
		t.Fatalf("EvaluateConstraints error: %v", err)
	}
	if !result.Allowed {
		t.Error("expected no constraints to mean allowed")
	}
}

func TestEvaluateConstraintsBlocking(t *testing.T) {
	eng := testEngine(t)
	store := eng.Store

	srv := &database.Server{Name: "test", Address: "t:25565", GameType: "minecraft", State: "online"}
	store.CreateServer(srv)
	created, _ := store.GetServerByName("test")

	constraint := &database.Constraint{
		Name:        "max-servers",
		Description: "Test constraint",
		Condition:   `countRunning(servers) > 0`,
		Strategy:    "deny",
		Priority:    1,
		Enabled:     true,
	}
	store.CreateConstraint(constraint)

	user := &UserEnv{Email: "admin@example.com", Role: "admin"}

	result, err := eng.EvaluateConstraints(created, "start", user)
	if err != nil {
		t.Fatalf("EvaluateConstraints error: %v", err)
	}
	if result.Allowed {
		t.Error("expected constraint to block action")
	}
	if result.Status != 409 {
		t.Errorf("Status = %d, want 409 (deny strategy)", result.Status)
	}
}

func TestEvaluateDeniedNoLink(t *testing.T) {
	eng := testEngine(t)
	store := eng.Store

	srv := &database.Server{Name: "test", Address: "t:25565", GameType: "minecraft", State: "online"}
	store.CreateServer(srv)
	created, _ := store.GetServerByName("test")

	user := &UserEnv{Email: "admin@example.com", Role: "admin"}

	result := eng.Evaluate(created, "start", nil, user)
	if result.Allowed {
		t.Error("expected denied when no Pterodactyl link")
	}
	if result.Status != 404 {
		t.Errorf("Status = %d, want 404", result.Status)
	}
}

func TestEvaluateAllowed(t *testing.T) {
	eng := testEngine(t)
	store := eng.Store

	srv := &database.Server{Name: "test", Address: "t:25565", GameType: "minecraft", State: "online"}
	store.CreateServer(srv)
	created, _ := store.GetServerByName("test")

	link := &database.PterodactylLink{
		ServerID:       created.ID,
		PteroServerID:  "abc",
		AllowedActions: `["start","stop","restart"]`,
	}
	store.CreatePterodactylLink(link)

	user := &UserEnv{Email: "admin@example.com", Role: "admin"}

	result := eng.Evaluate(created, "start", nil, user)
	if !result.Allowed {
		t.Errorf("expected allowed, got result=%q reason=%q", result.Result, result.Reason)
	}
}

func TestConstraintStrategyStatus(t *testing.T) {
	tests := []struct {
		strategy string
		want     int
	}{
		{"deny", 409},
		{"queue", 202},
		{"stop_oldest", 200},
		{"unknown", 409},
	}
	for _, tt := range tests {
		got := constraintStrategyStatus(tt.strategy)
		if got != tt.want {
			t.Errorf("constraintStrategyStatus(%q) = %d, want %d", tt.strategy, got, tt.want)
		}
	}
}

func TestIsActionAllowed(t *testing.T) {
	if !isActionAllowed(`["start","stop"]`, "start") {
		t.Error("expected start to be allowed")
	}
	if isActionAllowed(`["start","stop"]`, "restart") {
		t.Error("expected restart to not be allowed")
	}
	if isActionAllowed(`invalid json`, "start") {
		t.Error("expected invalid JSON to return false")
	}
}

func TestParamsToJSON(t *testing.T) {
	result := paramsToJSON(nil)
	if string(result) != "{}" {
		t.Errorf("paramsToJSON(nil) = %q, want {}", string(result))
	}
	result = paramsToJSON(map[string]string{"key": "val"})
	if string(result) != `{"key":"val"}` {
		t.Errorf("paramsToJSON = %q, want {\"key\":\"val\"}", string(result))
	}
}

func TestSourceDetection(t *testing.T) {
	eng := testEngine(t)
	store := eng.Store

	srv := &database.Server{Name: "test", Address: "t:25565", GameType: "minecraft", State: "online"}
	store.CreateServer(srv)
	created, _ := store.GetServerByName("test")

	link := &database.PterodactylLink{
		ServerID:       created.ID,
		PteroServerID:  "abc",
		AllowedActions: `["start"]`,
	}
	store.CreatePterodactylLink(link)

	cronUser := &UserEnv{Email: "system", Role: "system"}
	result := eng.Evaluate(created, "start", nil, cronUser)
	if !result.Allowed {
		t.Errorf("expected allowed for cron user, got %v", result)
	}

	var source string
	row := store.DB.QueryRow("SELECT source FROM audit_log WHERE user_email = ? ORDER BY id DESC LIMIT 1", "system")
	if err := row.Scan(&source); err != nil {
		t.Fatalf("failed to read audit log source: %v", err)
	}
	if source != "cron" {
		t.Errorf("Source = %q, want %q", source, "cron")
	}

	apiUser := &UserEnv{Email: "", Role: "admin"}
	eng.Evaluate(created, "start", nil, apiUser)

	row = store.DB.QueryRow("SELECT source FROM audit_log WHERE user_email = ? ORDER BY id DESC LIMIT 1", "")
	if err := row.Scan(&source); err != nil {
		t.Fatalf("failed to read audit log source: %v", err)
	}
	if source != "api" {
		t.Errorf("Source = %q, want %q", source, "api")
	}

	normalUser := &UserEnv{Email: "user@example.com", Role: "admin"}
	eng.Evaluate(created, "start", nil, normalUser)

	row = store.DB.QueryRow("SELECT source FROM audit_log WHERE user_email = ? ORDER BY id DESC LIMIT 1", "user@example.com")
	if err := row.Scan(&source); err != nil {
		t.Fatalf("failed to read audit log source: %v", err)
	}
	if source != "user" {
		t.Errorf("Source = %q, want %q", source, "user")
	}
}

func TestTestExpression(t *testing.T) {
	eng := &Engine{}

	env := map[string]interface{}{
		"count": 5,
		"name":  "test",
	}

	result, err := eng.TestExpression("count > 3", env)
	if err != nil {
		t.Fatalf("TestExpression error: %v", err)
	}
	if result != true {
		t.Errorf("TestExpression = %v, want true", result)
	}
}

func TestTestExpressionError(t *testing.T) {
	eng := &Engine{}

	_, err := eng.TestExpression("invalid [ syntax", map[string]interface{}{})
	if err == nil {
		t.Error("expected compile error for invalid expression")
	}
}

type fakeNotifier struct {
	lastEventType string
	lastMessage   string
}

func (f *fakeNotifier) Send(eventType, message string) {
	f.lastEventType = eventType
	f.lastMessage = message
}

func TestEngineSetNotifier(t *testing.T) {
	eng := testEngine(t)
	fn := &fakeNotifier{}
	eng.SetNotifier(fn)
	if eng.Notifier != fn {
		t.Error("expected notifier to be set")
	}
}

func TestConstraintViolationNotification(t *testing.T) {
	eng := testEngine(t)
	fn := &fakeNotifier{}
	eng.SetNotifier(fn)

	// Use the engine's store
	store := eng.Store
	server := &database.Server{Name: "TestServer", GameType: "minecraft", State: "online"}
	if err := store.CreateServer(server); err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	// Link server so Evaluate proceeds to constraints
	// Set ACL rule to allow everything so we reach constraints
	store.DB.Exec("INSERT INTO pterodactyl_servers (server_id, ptero_server_id, ptero_identifier, allowed_actions, acl_rule) VALUES (?, ?, ?, ?, ?)",
		server.ID, "test-uuid", "test-id", "[\"start\"]", "true")

	// Create a constraint that always blocks
	store.DB.Exec("INSERT INTO constraints (name, condition, strategy, priority, enabled) VALUES (?, ?, ?, ?, ?)",
		"test-block", "false", "deny", 1, 1)

	user := &UserEnv{Email: "test@example.com", Role: "user"}
	result := eng.Evaluate(server, "start", nil, user)

	if result.Allowed {
		t.Errorf("expected action to be blocked, got result=%s reason=%s", result.Result, result.Reason)
	}
	if result.Result != "blocked" {
		t.Errorf("expected blocked result, got %s", result.Result)
	}
	if fn.lastEventType != "constraint_violation" {
		t.Errorf("expected constraint_violation notification, got %q (result=%s)", fn.lastEventType, result.Result)
	}
	if !strings.Contains(fn.lastMessage, "TestServer") {
		t.Errorf("expected message to contain server name, got %q", fn.lastMessage)
	}
}

func jsonRawMessage(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return json.RawMessage(b)
}
