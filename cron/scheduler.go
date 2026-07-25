package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
	"github.com/tionis/hogs/query"
)

type ActionExecutor func(context.Context, *database.Server, string, map[string]string) error

type ActivityEnv struct {
	Online                bool
	Players               int
	MaxPlayers            int
	PlayersKnown          bool
	Fresh                 bool
	ObservationAgeSeconds int
}

type ServerEnv struct {
	ID      string
	Name    string
	Running bool
}

type Scheduler struct {
	Cron           *cron.Cron
	Store          *database.Store
	Engine         *engine.Engine
	Cache          *query.ServerStatusCache
	Notifier       engine.Notifier
	ActionExecutor ActionExecutor
	jobs           map[int]cron.EntryID
}

func NewScheduler(store *database.Store, eng *engine.Engine, cache ...*query.ServerStatusCache) *Scheduler {
	var statusCache *query.ServerStatusCache
	if len(cache) > 0 {
		statusCache = cache[0]
	}
	return &Scheduler{
		Cron:   cron.New(cron.WithSeconds()),
		Store:  store,
		Engine: eng,
		Cache:  statusCache,
		jobs:   make(map[int]cron.EntryID),
	}
}

func (s *Scheduler) SetActionExecutor(executor ActionExecutor) {
	s.ActionExecutor = executor
}

func (s *Scheduler) SetNotifier(n engine.Notifier) {
	s.Notifier = n
	if s.Engine != nil {
		s.Engine.SetNotifier(n)
	}
}

func (s *Scheduler) Start() error {
	if err := s.LoadJobs(); err != nil {
		return err
	}
	s.Cron.Start()
	log.Println("Cron scheduler started.")
	return nil
}

func (s *Scheduler) Stop() {
	ctx := s.Cron.Stop()
	<-ctx.Done()
	log.Println("Cron scheduler stopped.")
}

func (s *Scheduler) LoadJobs() error {
	jobs, err := s.Store.ListEnabledCronJobs()
	if err != nil {
		return err
	}

	for _, entryID := range s.jobs {
		s.Cron.Remove(entryID)
	}
	s.jobs = make(map[int]cron.EntryID)

	for _, job := range jobs {
		if err := s.addJobInternal(job); err != nil {
			log.Printf("Warning: failed to load cron job %q: %v", job.Name, err)
		}
	}
	return nil
}

func (s *Scheduler) addJobInternal(job database.CronJob) error {
	entryID, err := s.Cron.AddFunc(job.Schedule, func() {
		current, err := s.Store.GetCronJob(job.ID)
		if err != nil || current == nil {
			log.Printf("Automation rule %q disappeared before evaluation", job.Name)
			return
		}
		s.executeJob(*current)
	})
	if err != nil {
		return err
	}
	s.jobs[job.ID] = entryID
	log.Printf("Loaded cron job %q (ID %d, schedule %q)", job.Name, job.ID, job.Schedule)
	return nil
}

func (s *Scheduler) executeJob(job database.CronJob) {
	server, err := s.Store.GetServer(job.ServerID)
	if err != nil || server == nil {
		log.Printf("Cron job %q: server ID %d not found", job.Name, job.ServerID)
		return
	}

	start := time.Now()
	now := time.Now().UTC()
	params := map[string]string{}
	if job.Params != nil {
		_ = json.Unmarshal(job.Params, &params)
	}
	user := &engine.UserEnv{Username: "system", Role: "system"}

	conditionOK, conditionOutput := s.evaluateCondition(&job, server, now)
	if !conditionOK {
		var since *string
		if strings.HasPrefix(conditionOutput, "Waiting for") {
			since = job.ConditionTrueSince
		}
		s.finishJob(job, start, now, "waiting", conditionOutput, since, job.LastActionAt)
		return
	}

	if job.CooldownSeconds > 0 && job.LastActionAt != nil {
		if lastAction, parseErr := time.Parse(time.RFC3339, *job.LastActionAt); parseErr == nil {
			remaining := time.Duration(job.CooldownSeconds)*time.Second - now.Sub(lastAction)
			if remaining > 0 {
				s.finishJob(job, start, now, "cooldown",
					fmt.Sprintf("Cooldown active for another %s", remaining.Round(time.Second)),
					job.ConditionTrueSince, job.LastActionAt)
				return
			}
		}
	}

	var resultStr, output string
	result := s.Engine.Evaluate(server, job.Action, params, user)
	switch result.Result {
	case "allowed":
		if s.ActionExecutor == nil {
			resultStr = "error"
			output = "No lifecycle action executor is configured"
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			err = s.ActionExecutor(ctx, server, job.Action, params)
			cancel()
			if err != nil {
				resultStr = "error"
				output = err.Error()
			} else {
				resultStr = "success"
				output = fmt.Sprintf("%s completed on %s", job.Action, server.Name)
				actionAt := now.Format(time.RFC3339)
				job.LastActionAt = &actionAt
			}
		}
	case "blocked":
		resultStr = "blocked"
		output = result.Reason
		log.Printf("Cron job %q: blocked - %s", job.Name, result.Reason)
		if s.Notifier != nil {
			s.Notifier.Send("cron_failure", fmt.Sprintf("Cron job %q blocked on server %s: %s", job.Name, server.Name, result.Reason))
		}
	case "denied":
		resultStr = "denied"
		output = result.Reason
		log.Printf("Cron job %q: denied - %s", job.Name, result.Reason)
		if s.Notifier != nil {
			s.Notifier.Send("cron_failure", fmt.Sprintf("Cron job %q denied on server %s: %s", job.Name, server.Name, result.Reason))
		}
	case "queued":
		resultStr = "queued"
		log.Printf("Cron job %q: queued (not yet implemented as async)", job.Name)
	default:
		resultStr = result.Result
		log.Printf("Cron job %q: unexpected result %q", job.Name, result.Result)
	}

	s.finishJob(job, start, now, resultStr, output, job.ConditionTrueSince, job.LastActionAt)
}

func (s *Scheduler) evaluateCondition(job *database.CronJob, server *database.Server, now time.Time) (bool, string) {
	condition := strings.TrimSpace(job.Condition)
	if condition == "" {
		condition = "true"
	}
	activity := ActivityEnv{}
	if s.Cache != nil {
		if status, ok := s.Cache.Latest(server.ManagementID); ok && status != nil {
			age := now.Sub(status.LastUpdated)
			if age < 0 {
				age = 0
			}
			activity = ActivityEnv{
				Online: status.Online, Players: status.Players, MaxPlayers: status.MaxPlayers,
				PlayersKnown: status.PlayersKnown, Fresh: age <= 45*time.Second,
				ObservationAgeSeconds: int(age.Seconds()),
			}
		}
	}
	usesOccupancy := strings.Contains(condition, "activity.Players") ||
		strings.Contains(condition, "activity.MaxPlayers")
	if usesOccupancy && (!activity.Fresh || !activity.PlayersKnown) {
		return false, "Player telemetry is unavailable or stale; occupancy conditions fail closed"
	}
	env := map[string]interface{}{
		"server":   ServerEnv{ID: server.ManagementID, Name: server.Name, Running: activity.Online && activity.Fresh},
		"activity": activity,
		"time":     engine.TimeEnv{Now: now, Hour: now.Hour(), Weekday: now.Weekday()},
		"duration": func(value string) int {
			d, err := time.ParseDuration(value)
			if err != nil {
				return -1
			}
			return int(d.Seconds())
		},
	}
	value, err := s.Engine.TestExpression(condition, env)
	if err != nil {
		return false, "Condition error: " + err.Error()
	}
	ok, valid := value.(bool)
	if !valid {
		return false, "Condition error: expression must return true or false"
	}
	if !ok {
		return false, "Condition is false"
	}

	if job.StabilitySeconds <= 0 {
		return true, "Condition is true"
	}
	if job.ConditionTrueSince == nil {
		value := now.Format(time.RFC3339)
		job.ConditionTrueSince = &value
		_ = s.Store.UpdateCronJobRuntime(job.ID, job.ConditionTrueSince, job.LastActionAt)
	}
	since, err := time.Parse(time.RFC3339, *job.ConditionTrueSince)
	if err != nil {
		return false, "Condition timer is being initialized"
	}
	remaining := time.Duration(job.StabilitySeconds)*time.Second - now.Sub(since)
	if remaining > 0 {
		return false, fmt.Sprintf("Waiting for condition to remain true for %s more", remaining.Round(time.Second))
	}
	return true, "Condition has remained true for the required period"
}

func (s *Scheduler) finishJob(job database.CronJob, start, now time.Time, result, output string, conditionSince, lastActionAt *string) {
	if result != "waiting" || output == "Condition is false" || strings.HasPrefix(output, "Condition error:") {
		conditionSince = nil
	}
	timestamp := now.Format(time.RFC3339)
	_ = s.Store.UpdateCronJobTimestamps(job.ID, timestamp, "")
	_ = s.Store.UpdateCronJobResult(job.ID, result, output)
	_ = s.Store.UpdateCronJobRuntime(job.ID, conditionSince, lastActionAt)
	_ = s.Store.CreateCronJobLog(&database.CronJobLog{
		CronJobID: job.ID, Timestamp: timestamp, Result: result, Output: output,
		DurationMs: int(time.Since(start).Milliseconds()),
	})
}

func (s *Scheduler) AddJob(job *database.CronJob) error {
	if err := s.Store.CreateCronJob(job); err != nil {
		return err
	}
	return s.addJobInternal(*job)
}

func (s *Scheduler) UpdateJob(job *database.CronJob) error {
	if err := s.Store.UpdateCronJob(job); err != nil {
		return err
	}
	if entryID, ok := s.jobs[job.ID]; ok {
		s.Cron.Remove(entryID)
		delete(s.jobs, job.ID)
	}
	if job.Enabled {
		return s.addJobInternal(*job)
	}
	return nil
}

func (s *Scheduler) RemoveJob(id int) error {
	if entryID, ok := s.jobs[id]; ok {
		s.Cron.Remove(entryID)
		delete(s.jobs, id)
	}
	return s.Store.DeleteCronJob(id)
}
