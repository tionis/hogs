package cron

import (
	"encoding/json"
	"os"
	"testing"

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
	t.Cleanup(func() {
		store.DB.Close()
		os.Remove(dbPath)
	})
	cfg := &config.Config{
		AuditLogRetentionDays: 90,
	}
	cache := query.NewServerStatusCache()
	eng := engine.NewEngine(store, cfg, cache)
	sched := NewScheduler(store, eng)
	return sched, store
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
		Name:       "add-test",
		Schedule:   "0 0 6 * * *",
		ServerName: "non-existent",
		Action:     "start",
		Params:     params,
		Enabled:    true,
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
		Name:       "remove-test",
		Schedule:   "0 0 6 * * *",
		ServerName: "test",
		Action:     "start",
		Params:     params,
		Enabled:    true,
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
		Name:       "update-test",
		Schedule:   "0 0 6 * * *",
		ServerName: "test",
		Action:     "start",
		Params:     params,
		Enabled:    true,
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
		Name:       "disable-test",
		Schedule:   "0 0 6 * * *",
		ServerName: "test",
		Action:     "start",
		Params:     params,
		Enabled:    true,
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
