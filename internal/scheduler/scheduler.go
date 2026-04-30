package scheduler

import (
	"database/sql"
	"fmt"
	"log"
	"sync"

	"github.com/cmdr-tool/cmdr/internal/tasks"
	"github.com/robfig/cron/v3"
)

// Task represents a scheduled task.
type Task struct {
	Name        string
	Description string
	Schedule    string // cron expression (with seconds)
	Fn          func() error
	entryID     cron.EntryID
}

// Scheduler manages cron-scheduled tasks.
type Scheduler struct {
	mu    sync.Mutex
	cron  *cron.Cron
	tasks []Task
}

// Hooks holds optional callbacks that tasks can invoke.
type Hooks struct {
	OnCommitsSync     func()                        // called when sync-commits finds new commits
	OnGraphWatchBuild tasks.GraphWatchHook          // invoked by graph-watch when a repo's HEAD has moved
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
			Name:        "sync-commits",
			Description: "Fetch new commits from monitored repos",
			Schedule:    "0 */5 * * * *", // every 5 minutes
			Fn:          tasks.SyncCommits(db, hooks.OnCommitsSync),
		},
		{
			Name:        "prune",
			Description: "Clean up stale commits, tasks, and delegations",
			Schedule:    "0 0 3 * * *", // daily at 3am
			Fn:          tasks.Prune(db),
		},
		{
			Name:        "graph-watch",
			Description: "Rebuild knowledge graphs when monitored repos' HEAD moves",
			Schedule:    "0 */15 * * * *", // every 15 minutes
			Fn:          tasks.GraphWatch(db, hooks.OnGraphWatchBuild),
		},
	}
}

// Start begins running all scheduled tasks.
func (s *Scheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.tasks {
		task := &s.tasks[i]
		if task.entryID != 0 {
			continue // already registered via AddTask
		}
		eid, err := s.cron.AddFunc(task.Schedule, func() {
			log.Printf("cmdr: running task %q", task.Name)
			if err := task.Fn(); err != nil {
				log.Printf("cmdr: task %q failed: %v", task.Name, err)
			}
		})
		if err != nil {
			log.Printf("cmdr: failed to schedule %q: %v", task.Name, err)
			continue
		}
		task.entryID = eid
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
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Task, len(s.tasks))
	copy(out, s.tasks)
	return out
}

// RunTask runs a task by name immediately.
func (s *Scheduler) RunTask(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.tasks {
		if t.Name == name {
			return t.Fn()
		}
	}
	return fmt.Errorf("unknown task: %s", name)
}

// AddTask dynamically registers a new task into the running scheduler.
func (s *Scheduler) AddTask(t Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	task := t // capture for closure
	eid, err := s.cron.AddFunc(task.Schedule, func() {
		log.Printf("cmdr: running task %q", task.Name)
		if err := task.Fn(); err != nil {
			log.Printf("cmdr: task %q failed: %v", task.Name, err)
		}
	})
	if err != nil {
		return fmt.Errorf("schedule %q: %w", t.Name, err)
	}
	t.entryID = eid
	s.tasks = append(s.tasks, t)
	log.Printf("cmdr: scheduled dynamic task %q", t.Name)
	return nil
}

// RemoveTask unschedules and removes a task by name.
func (s *Scheduler) RemoveTask(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, t := range s.tasks {
		if t.Name == name {
			s.cron.Remove(t.entryID)
			s.tasks = append(s.tasks[:i], s.tasks[i+1:]...)
			log.Printf("cmdr: removed task %q", name)
			return
		}
	}
}
