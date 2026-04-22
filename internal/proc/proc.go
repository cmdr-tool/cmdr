package proc

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Process is a single row from the process table.
type Process struct {
	PID     int
	PPID    int
	TTY     string
	Elapsed time.Duration
	Comm    string
	Args    string
}

// Snapshot holds a point-in-time view of running processes.
type Snapshot struct {
	processes []Process
	byPID     map[int]Process
	parentMap map[int]int
}

// List returns a process snapshot from a single ps invocation.
func List() (*Snapshot, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,tty=,etimes=,comm=,args=").Output()
	if err != nil {
		return nil, err
	}

	s := &Snapshot{
		byPID:     make(map[int]Process),
		parentMap: make(map[int]int),
	}

	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		elapsedSec, err3 := strconv.Atoi(fields[3])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		p := Process{
			PID:     pid,
			PPID:    ppid,
			TTY:     fields[2],
			Elapsed: time.Duration(elapsedSec) * time.Second,
			Comm:    fields[4],
			Args:    strings.Join(fields[5:], " "),
		}
		s.processes = append(s.processes, p)
		s.byPID[pid] = p
		s.parentMap[pid] = ppid
	}

	return s, nil
}

func (s *Snapshot) Processes() []Process {
	if s == nil {
		return nil
	}
	return s.processes
}

func (s *Snapshot) Process(pid int) (Process, bool) {
	if s == nil {
		return Process{}, false
	}
	p, ok := s.byPID[pid]
	return p, ok
}

func (s *Snapshot) ParentMap() map[int]int {
	if s == nil {
		return nil
	}
	m := make(map[int]int, len(s.parentMap))
	for pid, ppid := range s.parentMap {
		m[pid] = ppid
	}
	return m
}

// Cwd returns the current working directory for pid, if available.
func Cwd(pid int) string {
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n")
		}
	}
	return ""
}

func BaseCommand(args string) string {
	args = strings.TrimSpace(args)
	if args == "" {
		return ""
	}
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

func FormatUptime(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return strings.TrimSuffix(d.Truncate(time.Minute).String(), "0s")
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h >= 24 {
		days := h / 24
		h = h % 24
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd %dh", days, h)
	}
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
