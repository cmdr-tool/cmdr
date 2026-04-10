package daemon

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/mikehu/cmdr/internal/db"
	"github.com/mikehu/cmdr/internal/scheduler"
)

func httpAddr() string {
	return "127.0.0.1:7369"
}

func runtimeDir() string {
	dir := filepath.Join(os.TempDir(), "cmdr")
	os.MkdirAll(dir, 0o700)
	return dir
}

func sockPath() string {
	return filepath.Join(runtimeDir(), "cmdr.sock")
}

func pidPath() string {
	return filepath.Join(runtimeDir(), "cmdr.pid")
}

// WebFS is set by main to the embedded web/build filesystem.
var WebFS fs.FS

// Version is set by main from the build-time version string.
var Version = "dev"

// Run starts the daemon in the foreground (blocking).
func Run() error {
	if err := writePID(); err != nil {
		return fmt.Errorf("writing pid: %w", err)
	}
	defer cleanup()

	database, err := db.Open()
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer database.Close()

	bus := NewEventBus()

	s := scheduler.New(database, scheduler.Hooks{
		OnCommitsSync: func() {
			bus.Publish(Event{Type: "commits:sync", Data: true})
		},
	})
	s.Start()
	defer s.Stop()
	stopPoller := startPoller(bus, s, database)
	defer stopPoller()

	mux := http.NewServeMux()
	registerAPI(mux, s, bus, database)

	// Debug routes
	registerDebugAPI(mux)

	// Serve embedded SPA for non-API routes (production)
	if WebFS != nil {
		mux.Handle("/", serveSPA(WebFS))
	}

	// Unix socket for CLI IPC
	os.Remove(sockPath())
	unixLn, err := net.Listen("unix", sockPath())
	if err != nil {
		return fmt.Errorf("listen unix: %w", err)
	}
	defer unixLn.Close()

	// TCP listener for web UI
	addr := httpAddr()
	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen tcp: %w", err)
	}
	defer tcpLn.Close()

	unixSrv := &http.Server{Handler: mux}
	tcpSrv := &http.Server{Handler: mux}

	// Graceful shutdown on SIGTERM/SIGINT
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		<-sig
		fmt.Println("\ncmdr: shutting down")
		unixSrv.Close()
		tcpSrv.Close()
	}()

	go func() {
		if err := tcpSrv.Serve(tcpLn); err != http.ErrServerClosed {
			log.Printf("cmdr: tcp server error: %v", err)
		}
	}()

	fmt.Printf("cmdr: daemon running (pid %d, http %s)\n", os.Getpid(), addr)
	if err := unixSrv.Serve(unixLn); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Start launches the daemon as a background process.
func Start() error {
	if pid, running := isRunning(); running {
		return fmt.Errorf("cmdr is already running (pid %d)", pid)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	proc, err := os.StartProcess(exe, []string{exe, "start", "-f"}, &os.ProcAttr{
		Dir:   "/",
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}
	proc.Release()
	fmt.Println("cmdr: daemon started")
	return nil
}

// Stop sends SIGTERM to the running daemon.
func Stop() error {
	pid, running := isRunning()
	if !running {
		return fmt.Errorf("cmdr is not running")
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("sending signal: %w", err)
	}
	fmt.Println("cmdr: stop signal sent")
	return nil
}

// Status prints daemon status.
func Status() error {
	pid, running := isRunning()
	if !running {
		fmt.Println("cmdr: not running")
		return nil
	}
	fmt.Printf("cmdr: running (pid %d)\n", pid)

	// Try querying the daemon's HTTP endpoint
	client := &http.Client{
		Transport: unixDialer(sockPath()),
	}
	resp, err := client.Get("http://cmdr/api/status")
	if err == nil {
		defer resp.Body.Close()
		body := make([]byte, 1024)
		n, _ := resp.Body.Read(body)
		fmt.Println(string(body[:n]))
	}
	return nil
}

func registerAPI(mux *http.ServeMux, s *scheduler.Scheduler, bus *EventBus, database *sql.DB) {
	mux.HandleFunc("/status", handleStatus(s))
	mux.HandleFunc("/run", handleRun(s))

	// /api prefix for web UI
	mux.HandleFunc("/api/status", handleStatus(s))
	mux.HandleFunc("/api/tasks", handleTasks(s))
	mux.HandleFunc("/api/run", handleRun(s))
	mux.HandleFunc("/api/tmux/sessions", handleTmuxSessions())
	mux.HandleFunc("/api/tmux/sessions/create", handleTmuxCreateSession())
	mux.HandleFunc("/api/tmux/sessions/switch", handleTmuxSwitch())
	mux.HandleFunc("/api/tmux/sessions/focus", handleTmuxFocus())
	mux.HandleFunc("/api/tmux/sessions/kill", handleTmuxKill())
	mux.HandleFunc("/api/open", handleOpenFolder())
	mux.HandleFunc("/api/editor/open", handleEditorOpen())
	mux.HandleFunc("/api/claude/sessions", handleClaudeSessions())
	mux.HandleFunc("/api/events", handleEvents(bus))

	// Git monitoring
	mux.HandleFunc("/api/repos", handleListRepos(database))
	mux.HandleFunc("/api/repos/discover", handleDiscoverRepos(database))
	mux.HandleFunc("/api/repos/add", handleAddRepo(database))
	mux.HandleFunc("/api/repos/remove", handleRemoveRepo(database))
	mux.HandleFunc("/api/commits", handleListCommits(database))
	mux.HandleFunc("/api/commits/diff", handleCommitDiff(database))
	mux.HandleFunc("/api/commits/files", handleCommitFiles(database))
	mux.HandleFunc("/api/commits/seen", handleMarkSeen(database))
	mux.HandleFunc("/api/commits/flag", handleToggleFlag(database))
	mux.HandleFunc("/api/repos/sync", handleSyncRepos(database, bus))
	mux.HandleFunc("/api/repos/pull", handleRepoPull(bus))

	// Analytics
	mux.HandleFunc("/api/analytics/activity", handleActivityAnalytics(database))

	// Brew
	mux.HandleFunc("/api/brew/outdated", handleBrewOutdated())
	mux.HandleFunc("/api/brew/upgrade", handleBrewUpgrade(bus))

	// Review
	mux.HandleFunc("/api/review/comments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleSaveReviewComment(database, bus)(w, r)
		} else {
			handleListReviewComments(database)(w, r)
		}
	})
	mux.HandleFunc("/api/review/comments/delete", handleDeleteReviewComment(database, bus))
	mux.HandleFunc("/api/review/submit", handleSubmitReview(database, bus))

	// Claude tasks
	mux.HandleFunc("/api/claude/tasks", handleListClaudeTasks(database))
	mux.HandleFunc("/api/claude/tasks/result", handleGetClaudeTaskResult(database))
	mux.HandleFunc("/api/claude/tasks/update", handleUpdateClaudeTaskResult(database))
	mux.HandleFunc("/api/claude/tasks/dismiss", handleDismissClaudeTask(database, bus))

	// Refactor
	mux.HandleFunc("/api/review/refactor", handleStartRefactor(database, bus))
	mux.HandleFunc("/api/claude/tasks/resolve", handleResolveTask(database, bus))

	// Directives (draft → submit via claude_tasks)
	mux.HandleFunc("/api/directives/create", handleCreateDirective(database, bus))
	mux.HandleFunc("/api/directives/save", handleSaveDirective(database, bus))
	mux.HandleFunc("/api/directives/submit", handleSubmitDirective(database, bus))
	mux.HandleFunc("/api/directives/intents", handleListIntents())

	// Code + Images (for directive composer)
	mux.HandleFunc("/api/code/files", handleCodeFiles())
	mux.HandleFunc("/api/code/snippet", handleCodeSnippet())
	mux.HandleFunc("/api/images/upload", handleImageUpload())
	mux.HandleFunc("/api/images/", handleImageServe())
}

func handleStatus(s *scheduler.Scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":  "running",
			"version": Version,
			"pid":     os.Getpid(),
			"tasks":   len(s.Tasks()),
		})
	}
}

func handleTasks(s *scheduler.Scheduler) http.HandlerFunc {
	type taskInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Schedule    string `json:"schedule"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		items := make([]taskInfo, 0, len(s.Tasks()))
		for _, t := range s.Tasks() {
			items = append(items, taskInfo{
				Name:        t.Name,
				Description: t.Description,
				Schedule:    t.Schedule,
			})
		}
		json.NewEncoder(w).Encode(items)
	}
}

func handleRun(s *scheduler.Scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		task := r.URL.Query().Get("task")
		if task == "" {
			http.Error(w, `{"error":"missing ?task= parameter"}`, http.StatusBadRequest)
			return
		}
		if err := s.RunTask(task); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"output": fmt.Sprintf("task %q executed", task)})
	}
}

func writePID() error {
	return os.WriteFile(pidPath(), []byte(strconv.Itoa(os.Getpid())), 0o644)
}

func cleanup() {
	os.Remove(pidPath())
	os.Remove(sockPath())
}

func isRunning() (int, bool) {
	data, err := os.ReadFile(pidPath())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return 0, false
	}
	// Signal 0 checks if process exists
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return 0, false
	}
	return pid, true
}
