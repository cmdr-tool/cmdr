package scheduler

import (
	"database/sql"
	"fmt"
	"log"

	"github.com/mikehu/cmdr/internal/tasks"
	"github.com/robfig/cron/v3"
)

// Task represents a scheduled task.
type Task struct {
	Name        string
	Description string
	Schedule    string // cron expression (with seconds)
	Fn          func() error
}

// Scheduler manages cron-scheduled tasks.
type Scheduler struct {
	cron  *cron.Cron
	tasks []Task
}

// Hooks holds optional callbacks that tasks can invoke.
type Hooks struct {
	OnCommitsSync func() // called when sync-commits finds new commits
}

// New creates a scheduler with all registered tasks.
func New(db *sql.DB, hooks Hooks) *Scheduler {
	s := &Scheduler{
		cron: cron.New(cron.WithSeconds()),
	}
	s.register(db, hooks)
	return s
}

// register adds all defined tasks.
func (s *Scheduler) register(db *sql.DB, hooks Hooks) {
	s.tasks = []Task{
		{
			Name:        "hello",
			Description: "Example task — prints a message",
			Schedule:    "0 0 * * * *", // every hour
			Fn:          tasks.Hello,
		},
		{
			Name:        "sync-commits",
			Description: "Fetch new commits from monitored repos",
			Schedule:    "0 */5 * * * *", // every 5 minutes
			Fn:          tasks.SyncCommits(db, hooks.OnCommitsSync),
		},
		{
			Name:        "prune-commits",
			Description: "Delete commits older than 2 weeks",
			Schedule:    "0 0 3 * * *", // daily at 3am
			Fn:          tasks.PruneCommits(db),
		},
	}
}

// Start begins running all scheduled tasks.
func (s *Scheduler) Start() {
	for _, t := range s.tasks {
		task := t // capture
		if _, err := s.cron.AddFunc(task.Schedule, func() {
			log.Printf("cmdr: running task %q", task.Name)
			if err := task.Fn(); err != nil {
				log.Printf("cmdr: task %q failed: %v", task.Name, err)
			}
		}); err != nil {
			log.Printf("cmdr: failed to schedule %q: %v", task.Name, err)
		}
	}
	s.cron.Start()
	log.Printf("cmdr: scheduler started with %d tasks", len(s.tasks))
}

// Stop gracefully stops the scheduler.
func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// Tasks returns all registered tasks.
func (s *Scheduler) Tasks() []Task {
	return s.tasks
}

// RunTask runs a task by name immediately.
func (s *Scheduler) RunTask(name string) error {
	for _, t := range s.tasks {
		if t.Name == name {
			return t.Fn()
		}
	}
	return fmt.Errorf("unknown task: %s", name)
}
