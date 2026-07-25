package cron

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/tionis/hogs/config"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
	"github.com/tionis/hogs/query"
)

func testScheduler(t *testing.T) (*Scheduler, *database.Store) {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	store, err := database.NewStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	if err := store.CreateServer(&database.Server{Name: "test", GameType: "generic", State: "online"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.DB.Close()
		os.Remove(dbPath)
	})
	cfg := &config.Config{
		AuditLogRetentionDays: 90,
	}
	cache := query.NewServerStatusCache()
	eng := engine.NewEngine(store, cfg, cache)
	sched := NewScheduler(store, eng, cache)
	return sched, store
}

func TestConditionalRuleWaitsForContinuousKnownEmptyState(t *testing.T) {
	sched, store := testScheduler(t)
	server, err := store.GetServer(1)
	if err != nil || server == nil {
		t.Fatalf("load server: %v", err)
	}
	if err := store.CreatePterodactylLink(&database.PterodactylLink{
		ServerID: server.ID, PteroServerID: "test", AllowedActions: `["stop"]`,
	}); err != nil {
		t.Fatal(err)
	}
	sched.Cache.Set(server.ManagementID, &query.ServerStatus{
		Online: true, PlayersKnown: true, Players: 0, LastUpdated: time.Now(),
	})
	executions := 0
	sched.SetActionExecutor(func(_ context.Context, _ *database.Server, action string, _ map[string]string) error {
		executions++
		if action != "stop" {
			t.Fatalf("unexpected action %q", action)
		}
		return nil
	})
	job := &database.CronJob{
		Name: "idle-stop", Schedule: "0 * * * * *", ServerID: server.ID,
		Action: "stop", Condition: "server.Running && activity.Fresh && activity.PlayersKnown && activity.Players == 0",
		StabilitySeconds: 900, Enabled: true,
	}
	if err := store.CreateCronJob(job); err != nil {
		t.Fatal(err)
	}

	sched.executeJob(*job)
	if executions != 0 {
		t.Fatal("rule acted before the stability period")
	}
	waiting, _ := store.GetCronJob(job.ID)
	if waiting.ConditionTrueSince == nil || waiting.LastResult != "waiting" {
		t.Fatalf("expected persisted waiting state, got %#v", waiting)
	}

	past := time.Now().UTC().Add(-16 * time.Minute).Format(time.RFC3339)
	if err := store.UpdateCronJobRuntime(job.ID, &past, nil); err != nil {
		t.Fatal(err)
	}
	ready, _ := store.GetCronJob(job.ID)
	sched.executeJob(*ready)
	if executions != 1 {
		t.Fatalf("expected one lifecycle action, got %d", executions)
	}
	completed, _ := store.GetCronJob(job.ID)
	if completed.LastResult != "success" || completed.LastActionAt == nil {
		t.Fatalf("expected successful persisted execution, got %#v", completed)
	}
}

func TestPlayerRuleFailsClosedWhenTelemetryIsUnknown(t *testing.T) {
	sched, store := testScheduler(t)
	server, _ := store.GetServer(1)
	sched.Cache.Set(server.ManagementID, &query.ServerStatus{
		Online: true, PlayersKnown: false, LastUpdated: time.Now(),
	})
	executions := 0
	sched.SetActionExecutor(func(context.Context, *database.Server, string, map[string]string) error {
		executions++
		return nil
	})
	job := database.CronJob{
		ID: 1, Name: "safe-idle-stop", ServerID: server.ID, Action: "stop",
		Condition: "server.Running && activity.Fresh && activity.PlayersKnown && activity.Players == 0",
	}
	if err := store.CreateCronJob(&job); err != nil {
		t.Fatal(err)
	}
	sched.executeJob(job)
	if executions != 0 {
		t.Fatal("unknown occupancy must never be treated as empty")
	}
}

func TestNewScheduler(t *testing.T) {
	sched, _ := testScheduler(t)
	if sched == nil {
		t.Fatal("expected non-nil scheduler")
	}
	if sched.Cron == nil {
		t.Error("expected non-nil Cron")
	}
	if sched.Store == nil {
		t.Error("expected non-nil Store")
	}
}

func TestLoadJobsEmpty(t *testing.T) {
	sched, _ := testScheduler(t)
	err := sched.LoadJobs()
	if err != nil {
		t.Fatalf("LoadJobs error: %v", err)
	}
}

func TestAddJob(t *testing.T) {
	sched, _ := testScheduler(t)

	params, _ := json.Marshal(map[string]string{})
	job := &database.CronJob{
		Name:     "add-test",
		Schedule: "0 0 6 * * *",
		ServerID: 1,
		Action:   "start",
		Params:   params,
		Enabled:  true,
	}
	err := sched.AddJob(job)
	if err != nil {
		t.Fatalf("AddJob error: %v", err)
	}

	if job.ID == 0 {
		t.Error("expected job ID to be set after creation")
	}

	if len(sched.jobs) != 1 {
		t.Errorf("expected 1 job, got %d", len(sched.jobs))
	}
}

func TestRemoveJob(t *testing.T) {
	sched, store := testScheduler(t)

	params, _ := json.Marshal(map[string]string{})
	job := &database.CronJob{
		Name:     "remove-test",
		Schedule: "0 0 6 * * *",
		ServerID: 1,
		Action:   "start",
		Params:   params,
		Enabled:  true,
	}
	sched.AddJob(job)

	err := sched.RemoveJob(job.ID)
	if err != nil {
		t.Fatalf("RemoveJob error: %v", err)
	}

	if len(sched.jobs) != 0 {
		t.Errorf("expected 0 jobs after removal, got %d", len(sched.jobs))
	}

	got, err := store.GetCronJob(job.ID)
	if err != nil {
		t.Fatalf("GetCronJob error: %v", err)
	}
	if got != nil {
		t.Error("expected job to be deleted from database")
	}
}

func TestUpdateJob(t *testing.T) {
	sched, _ := testScheduler(t)

	params, _ := json.Marshal(map[string]string{})
	job := &database.CronJob{
		Name:     "update-test",
		Schedule: "0 0 6 * * *",
		ServerID: 1,
		Action:   "start",
		Params:   params,
		Enabled:  true,
	}
	sched.AddJob(job)

	job.Schedule = "0 0 12 * * *"
	job.Action = "stop"
	err := sched.UpdateJob(job)
	if err != nil {
		t.Fatalf("UpdateJob error: %v", err)
	}

	if len(sched.jobs) != 1 {
		t.Errorf("expected 1 job after update, got %d", len(sched.jobs))
	}
}

func TestUpdateJobDisable(t *testing.T) {
	sched, _ := testScheduler(t)

	params, _ := json.Marshal(map[string]string{})
	job := &database.CronJob{
		Name:     "disable-test",
		Schedule: "0 0 6 * * *",
		ServerID: 1,
		Action:   "start",
		Params:   params,
		Enabled:  true,
	}
	sched.AddJob(job)

	job.Enabled = false
	err := sched.UpdateJob(job)
	if err != nil {
		t.Fatalf("UpdateJob error: %v", err)
	}

	if len(sched.jobs) != 0 {
		t.Errorf("expected 0 active jobs after disable, got %d", len(sched.jobs))
	}
}

func TestSchedulerStartStop(t *testing.T) {
	sched, _ := testScheduler(t)

	err := sched.Start()
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}

	sched.Stop()
}
