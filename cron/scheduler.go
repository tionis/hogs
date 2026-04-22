package cron

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/tionis/hogs/database"
	"github.com/tionis/hogs/engine"
)

type Scheduler struct {
	Cron     *cron.Cron
	Store    *database.Store
	Engine   *engine.Engine
	Notifier engine.Notifier
	jobs     map[int]cron.EntryID
}

func NewScheduler(store *database.Store, eng *engine.Engine) *Scheduler {
	return &Scheduler{
		Cron:   cron.New(cron.WithSeconds()),
		Store:  store,
		Engine: eng,
		jobs:   make(map[int]cron.EntryID),
	}
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
	var params map[string]string
	if job.Params != nil {
		json.Unmarshal(job.Params, &params)
	}
	if params == nil {
		params = make(map[string]string)
	}

	user := &engine.UserEnv{
		Email: "system",
		Role:  "system",
	}

	entryID, err := s.Cron.AddFunc(job.Schedule, func() {
		s.executeJob(job, params, user)
	})
	if err != nil {
		return err
	}
	s.jobs[job.ID] = entryID
	log.Printf("Loaded cron job %q (ID %d, schedule %q)", job.Name, job.ID, job.Schedule)
	return nil
}

func (s *Scheduler) executeJob(job database.CronJob, params map[string]string, user *engine.UserEnv) {
	server, err := s.Store.GetServerByName(job.ServerName)
	if err != nil || server == nil {
		log.Printf("Cron job %q: server %q not found", job.Name, job.ServerName)
		return
	}

	action := job.Action
	start := time.Now()

	result := s.Engine.Evaluate(server, action, params, user)

	var resultStr, output string
	switch result.Result {
	case "allowed":
		resultStr = "success"
		output = s.executeAction(server, action, params)
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

	now := time.Now().UTC().Format(time.RFC3339)
	s.Store.UpdateCronJobTimestamps(job.ID, now, "")
	s.Store.UpdateCronJobResult(job.ID, resultStr, output)

	s.Store.CreateCronJobLog(&database.CronJobLog{
		CronJobID:  job.ID,
		Timestamp:  now,
		Result:     resultStr,
		Output:     output,
		DurationMs: int(time.Since(start).Milliseconds()),
	})
}

func (s *Scheduler) executeAction(server *database.Server, action string, params map[string]string) string {
	link, err := s.Store.GetPterodactylLink(server.ID)
	if err != nil || link == nil {
		log.Printf("Cron action %s/%s: server not linked", server.Name, action)
		return "server not linked"
	}

	log.Printf("Cron executing %s on %s", action, server.Name)
	return fmt.Sprintf("cron %s on %s", action, server.Name)
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
