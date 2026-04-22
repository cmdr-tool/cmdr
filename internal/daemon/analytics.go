package daemon

import (
	"database/sql"
	"log"
	"time"

	"github.com/cmdr-tool/cmdr/internal/agent"
	"github.com/cmdr-tool/cmdr/internal/terminal"
)

// lastClearedDay tracks which day-of-year we've verified the slot for.
// On startup (-1), we check if existing data is from today before clearing.
var lastClearedDay = -1

// recordActivity persists one 5-second activity snapshot into the fixed-bucket table.
// When away=true, the user is idle — record tool as "away" but still capture Claude states.
func recordActivity(db *sql.DB, termSessions []terminal.Session, agentInstances []agent.Instance, now time.Time, away bool) {
	slot, bucket := currentBucket(now)
	today := now.YearDay()

	// Only clear the slot if the day changed (not just on restart).
	// Check if existing data in this slot is from today — if so, keep it.
	if today != lastClearedDay {
		if !slotHasDataForToday(db, slot, now) {
			clearSlot(db, slot)
		}
		lastClearedDay = today
	}

	var activeTool string
	if away {
		activeTool = "away"
	} else {
		activeTool = determineActiveTool(termSessions)
	}
	cTotal, cWorking, cWaiting, cIdle, cUnknown := countAgentStates(agentInstances, "claude")
	pTotal, pWorking, pWaiting, pIdle, pUnknown := countAgentStates(agentInstances, "pi")

	_, err := db.Exec(`INSERT OR REPLACE INTO activity_buckets
		(slot, bucket, active_tool,
		 claude_total, claude_working, claude_waiting, claude_idle, claude_unknown,
		 pi_total, pi_working, pi_waiting, pi_idle, pi_unknown,
		 recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		slot, bucket, activeTool,
		cTotal, cWorking, cWaiting, cIdle, cUnknown,
		pTotal, pWorking, pWaiting, pIdle, pUnknown,
		now.Format(time.RFC3339),
	)
	if err != nil {
		log.Printf("cmdr: analytics: record error: %v", err)
	}
}

// currentBucket returns the slot (0 or 1) and bucket index (0..17279) for a given time.
func currentBucket(now time.Time) (slot int, bucket int) {
	slot = now.YearDay() % 2
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	bucket = int(now.Sub(midnight).Seconds()) / 5
	return
}

// slotHasDataForToday checks if the slot already contains data from today.
// Prevents wiping valid data on daemon restart.
func slotHasDataForToday(db *sql.DB, slot int, now time.Time) bool {
	todayPrefix := now.Format("2006-01-02") // matches start of RFC3339
	var count int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM activity_buckets
		WHERE slot = ? AND recorded_at IS NOT NULL AND recorded_at LIKE ?
	`, slot, todayPrefix+"%").Scan(&count)
	return err == nil && count > 0
}

// clearSlot wipes all data for a slot so it can be reused for the new day.
func clearSlot(db *sql.DB, slot int) {
	_, err := db.Exec(`DELETE FROM activity_buckets WHERE slot = ?`, slot)
	if err != nil {
		log.Printf("cmdr: analytics: clear slot %d error: %v", slot, err)
	}
}

// backfillSleep fills the gap between sleepStart and wakeTime with "away" buckets.
// Only fills buckets within the same day as wakeTime to avoid cross-day complexity.
func backfillSleep(db *sql.DB, sleepStart, wakeTime time.Time) {
	_, startBucket := currentBucket(sleepStart)
	slot, endBucket := currentBucket(wakeTime)

	// Only backfill within today's slot (don't cross midnight boundaries)
	if sleepStart.YearDay() != wakeTime.YearDay() {
		startBucket = 0
	}

	for b := startBucket; b < endBucket; b++ {
		db.Exec(`INSERT OR IGNORE INTO activity_buckets
			(slot, bucket, active_tool,
			 claude_total, claude_working, claude_waiting, claude_idle, claude_unknown,
			 pi_total, pi_working, pi_waiting, pi_idle, pi_unknown,
			 recorded_at)
			VALUES (?, ?, 'away', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, ?)`,
			slot, b, wakeTime.Format(time.RFC3339),
		)
	}

	if startBucket < endBucket {
		log.Printf("cmdr: analytics: backfilled %d sleep buckets (%d→%d)", endBucket-startBucket, startBucket, endBucket)
	}
}

// determineActiveTool returns what tool is focused in the attached tmux session.
func determineActiveTool(sessions []terminal.Session) string {
	for _, s := range sessions {
		if !s.Attached {
			continue
		}
		for _, w := range s.Windows {
			for _, p := range w.Panes {
				if !p.Active {
					continue
				}
				switch p.Command {
				case "nvim", "vim":
					return "nvim"
				case "claude":
					return "claude"
				case "pi", "volta-shim":
					return "pi"
				default:
					return "other"
				}
			}
		}
		break // only first attached session
	}
	return "inactive"
}

// countAgentStates tallies statuses for instances of a specific agent.
func countAgentStates(instances []agent.Instance, agentName string) (total, working, waiting, idle, unknown int) {
	for _, inst := range instances {
		if inst.Agent != agentName {
			continue
		}
		total++
		switch inst.Status {
		case "working":
			working++
		case "waiting":
			waiting++
		case "idle":
			idle++
		default:
			unknown++
		}
	}
	return
}
