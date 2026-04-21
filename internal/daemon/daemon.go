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
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/cmdr-tool/cmdr/internal/agent"
	_ "github.com/cmdr-tool/cmdr/internal/agent/claude"
	"github.com/cmdr-tool/cmdr/internal/db"
	"github.com/cmdr-tool/cmdr/internal/scheduler"
	"github.com/cmdr-tool/cmdr/internal/summarizer"
	_ "github.com/cmdr-tool/cmdr/internal/summarizer/apple"
	_ "github.com/cmdr-tool/cmdr/internal/summarizer/ollama"
	"github.com/cmdr-tool/cmdr/internal/terminal"
	_ "github.com/cmdr-tool/cmdr/internal/terminal/adapters/cmux"
	_ "github.com/cmdr-tool/cmdr/internal/terminal/adapters/tmux"
)

// term, emu, sum, and agt are the active adapters, resolved once at daemon startup.
var (
	term terminal.Multiplexer
	emu  terminal.Emulator
	sum  summarizer.Summarizer
	agt  agent.Agent
	caps Capabilities
)

// Capabilities describes optional features available in the current environment.
// Computed once at startup and served via /api/status.
type Capabilities struct {
	AskSkill bool `json:"askSkill"`
}

// detectCapabilities probes the local environment for optional features.
func detectCapabilities() Capabilities {
	return Capabilities{
		AskSkill: skillExists("ask"),
	}
}

// skillExists checks whether a Claude skill directory exists in the standard locations.
func skillExists(name string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, base := range []string{
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
	} {
		if _, err := os.Stat(filepath.Join(base, name, "SKILL.md")); err == nil {
			return true
		}
	}
	return false
}

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

	// Resolve terminal adapter
	adapterName := os.Getenv("CMDR_MULTIPLEXER")
	if adapterName == "" {
		adapterName = "tmux"
	}
	var err error
	term, err = terminal.New(adapterName)
	if err != nil {
		return fmt.Errorf("terminal adapter: %w", err)
	}
	appName := os.Getenv("CMDR_TERMINAL_APP")
	if appName == "" {
		appName = "Ghostty"
	}
	emu = &terminal.MacOSEmulator{AppName: appName}

	// Resolve summarizer adapter
	sumName := os.Getenv("CMDR_SUMMARIZER")
	if sumName == "" {
		sumName = "apple"
	}
	sum, err = summarizer.New(sumName)
	if err != nil {
		log.Printf("cmdr: summarizer %q unavailable, titles will not be enhanced: %v", sumName, err)
	}

	// Resolve default agent adapter (claude is the baseline)
	agt, err = agent.New("claude")
	if err != nil {
		return fmt.Errorf("agent adapter: %w", err)
	}
	agentCaps := agt.Capabilities()
	log.Printf("cmdr: agent %q (streaming=%v worktrees=%v)", agt.Name(), agentCaps.Streaming, agentCaps.Worktrees)

	// Load agent override files
	loadOverrides()

	// Detect available capabilities
	caps = detectCapabilities()
	log.Printf("cmdr: capabilities: askSkill=%v", caps.AskSkill)

	database, err := db.Open()
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer database.Close()

	bus := NewEventBus()

	// Mark any ask tasks orphaned by a previous daemon instance
	cleanupOrphanedHeadlessTasks(database)

	s := scheduler.New(database, scheduler.Hooks{
		OnCommitsSync: func() {
			bus.Publish(Event{Type: "commits:sync", Data: true})
		},
	})
	LoadAgenticTasks(s, database, bus)
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
	resp, err := Client().Get("http://cmdr/api/status")
	if err == nil {
		defer resp.Body.Close()
		body := make([]byte, 1024)
		n, _ := resp.Body.Read(body)
		fmt.Println(string(body[:n]))
	}
	return nil
}

func registerAPI(mux *http.ServeMux, s *scheduler.Scheduler, bus *EventBus, database *sql.DB) {
	// CLI IPC (no /api prefix)
	mux.HandleFunc("/status", handleStatus(s))
	mux.HandleFunc("/run", handleRun(s))

	// Core
	mux.HandleFunc("/api/status", handleStatus(s))
	mux.HandleFunc("/api/tasks", handleTasks(s))
	mux.HandleFunc("/api/run", handleRun(s))
	mux.HandleFunc("/api/events", handleEvents(bus))

	// Terminal sessions
	mux.HandleFunc("/api/tmux/sessions", handleSessions())
	mux.HandleFunc("/api/tmux/sessions/create", handleCreateSession())
	mux.HandleFunc("/api/tmux/sessions/switch", handleSessionSwitch())
	mux.HandleFunc("/api/tmux/sessions/focus", handleSessionFocus())
	mux.HandleFunc("/api/tmux/sessions/kill", handleSessionKill())

	// System utilities
	mux.HandleFunc("/api/open", handleOpenFolder())
	mux.HandleFunc("/api/editor/open", handleEditorOpen())

	// Repos + commits
	mux.HandleFunc("/api/repos", handleListRepos(database))
	mux.HandleFunc("/api/repos/discover", handleDiscoverRepos(database))
	mux.HandleFunc("/api/repos/add", handleAddRepo(database))
	mux.HandleFunc("/api/repos/remove", handleRemoveRepo(database))
	mux.HandleFunc("/api/repos/sync", handleSyncRepos(database, bus))
	mux.HandleFunc("/api/repos/pull", handleRepoPull(bus))
	mux.HandleFunc("/api/repos/push", handleRepoPush())
	mux.HandleFunc("/api/repos/unpushed", handleUnpushedCheck())
	mux.HandleFunc("/api/repos/squad", handleAssignRepoSquad(database))
	mux.HandleFunc("/api/repos/monitor", handleUpdateRepoMonitor(database, bus))
	mux.HandleFunc("/api/commits", handleListCommits(database))
	mux.HandleFunc("/api/commits/diff", handleCommitDiff(database))
	mux.HandleFunc("/api/commits/files", handleCommitFiles(database))
	mux.HandleFunc("/api/commits/seen", handleMarkSeen(database))
	mux.HandleFunc("/api/commits/flag", handleToggleFlag(database))

	// Squads
	mux.HandleFunc("/api/squads", handleListSquads(database))
	mux.HandleFunc("/api/squads/create", handleCreateSquad(database))
	mux.HandleFunc("/api/squads/delete", handleDeleteSquad(database))
	mux.HandleFunc("/api/squads/enlist", handleEnlist(database, bus))
	mux.HandleFunc("/api/squads/delegations", handleListDelegations(database))
	mux.HandleFunc("/api/squads/delegation-summary", handleDelegationSummary(database))

	// Review
	mux.HandleFunc("/api/review/comments", handleListReviewComments(database))
	mux.HandleFunc("/api/review/comments/save", handleSaveReviewComment(database, bus))
	mux.HandleFunc("/api/review/comments/delete", handleDeleteReviewComment(database, bus))
	mux.HandleFunc("/api/review/submit", handleSubmitReview(database, bus))

	// Agent tasks
	mux.HandleFunc("/api/agent/sessions", handleAgentSessions())
	mux.HandleFunc("/api/agent/tasks", handleListAgentTasks(database))
	mux.HandleFunc("/api/agent/tasks/result", handleGetAgentTaskResult(database))
	mux.HandleFunc("/api/agent/tasks/update", handleUpdateAgentTaskResult(database, bus))
	mux.HandleFunc("/api/agent/tasks/dismiss", handleDismissAgentTask(database, bus))
	mux.HandleFunc("/api/agent/tasks/cancel", handleCancelTask(database, bus))
	mux.HandleFunc("/api/agent/tasks/resolve", handleResolveTask(database, bus))
	mux.HandleFunc("/api/agent/tasks/spawn", handleSpawnTask(database, bus))

	// Directives
	mux.HandleFunc("/api/directives/create", handleCreateDirective(database, bus))
	mux.HandleFunc("/api/directives/save", handleSaveDirective(database, bus))
	mux.HandleFunc("/api/directives/submit", handleSubmitDirective(database, bus))
	mux.HandleFunc("/api/directives/intents", handleListIntents())


	// Ask
	mux.HandleFunc("/api/ask", handleAsk(database, bus))
	mux.HandleFunc("/api/ask/continue", handleContinueSession(database))

	// Agentic tasks
	mux.HandleFunc("/api/agentic-tasks", handleListAgenticTasks(database))
	mux.HandleFunc("/api/agentic-tasks/create", handleCreateAgenticTask(database, bus, s))
	mux.HandleFunc("/api/agentic-tasks/update", handleUpdateAgenticTask(database, bus, s))
	mux.HandleFunc("/api/agentic-tasks/delete", handleDeleteAgenticTask(database, bus, s))
	mux.HandleFunc("/api/agentic-tasks/run", handleRunAgenticTask(database, bus))

	// Analytics
	mux.HandleFunc("/api/analytics/activity", handleActivityAnalytics(database))

	// Brew
	mux.HandleFunc("/api/brew/outdated", handleBrewOutdated())
	mux.HandleFunc("/api/brew/upgrade", handleBrewUpgrade(bus))

	// Notifications (CLI → daemon SSE bridge)
	mux.HandleFunc("/api/notify", handleNotify(bus))

	// Code + Images (directive composer)
	mux.HandleFunc("/api/code/files", handleCodeFiles())
	mux.HandleFunc("/api/code/snippet", handleCodeSnippet())
	mux.HandleFunc("/api/images/upload", handleImageUpload())
	mux.HandleFunc("/api/images/", handleImageServe())
}

func handleStatus(s *scheduler.Scheduler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":       "running",
			"version":      Version,
			"pid":          os.Getpid(),
			"tasks":        len(s.Tasks()),
			"user":         currentUserName(),
			"capabilities": caps,
			"agent": map[string]any{
				"name":         agt.Name(),
				"capabilities": agt.Capabilities(),
			},
		})
	}
}

// currentUserName returns the login username for display in the greeting.
func currentUserName() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
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
			if strings.HasPrefix(t.Name, "agentic:") {
				continue
			}
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
