package daemon

import (
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"time"
)

type activityBucket struct {
	Bucket        int `json:"bucket"`
	Samples       int `json:"samples"`       // total 5s samples in this window
	Nvim          int `json:"nvim"`           // samples where active tool was nvim
	Claude        int `json:"claude"`         // samples where active tool was claude
	Other         int `json:"other"`          // samples where active tool was other
	Inactive      int `json:"inactive"`       // samples where inactive (no attached session)
	Away          int `json:"away"`           // samples where user was away from keyboard
	ClaudeTotal   int `json:"claudeTotal"`    // avg total agent instances
	ClaudeWorking int `json:"claudeWorking"`  // avg working
	ClaudeWaiting int `json:"claudeWaiting"`  // avg waiting
	ClaudeIdle    int `json:"claudeIdle"`     // avg idle
	ClaudeUnknown int `json:"claudeUnknown"`  // avg unknown
}

type activityDay struct {
	Date          string           `json:"date"`
	CurrentBucket int              `json:"currentBucket,omitempty"`
	Buckets       []activityBucket `json:"buckets"`
}

type activityResponse struct {
	Resolution    string      `json:"resolution"`
	SamplesPerBar int         `json:"samplesPerBar"`
	Today         activityDay `json:"today"`
	Yesterday     activityDay `json:"yesterday"`
}

type rawSample struct {
	bucket                                int
	tool                                  string
	total, working, waiting, idle, unknown int
}

func handleActivityAnalytics(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		todaySlot := now.YearDay() % 2
		yesterdaySlot := (todaySlot + 1) % 2

		resParam := r.URL.Query().Get("resolution")
		mergeCount := 60 // default 5m (60 × 5s)
		switch resParam {
		case "1m":
			mergeCount = 12
		case "5s":
			mergeCount = 1
		default:
			resParam = "5m"
		}

		_, curBucket := currentBucket(now)

		resp := activityResponse{
			Resolution:    resParam,
			SamplesPerBar: mergeCount,
			Today: activityDay{
				Date:          now.Format("2006-01-02"),
				CurrentBucket: curBucket / mergeCount,
				Buckets:       querySlot(db, todaySlot, mergeCount),
			},
			Yesterday: activityDay{
				Date:    now.AddDate(0, 0, -1).Format("2006-01-02"),
				Buckets: querySlot(db, yesterdaySlot, mergeCount),
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func querySlot(db *sql.DB, slot, mergeCount int) []activityBucket {
	rows, err := db.Query(`
		SELECT bucket, active_tool, claude_total, claude_working, claude_waiting, claude_idle, claude_unknown
		FROM activity_buckets
		WHERE slot = ? AND recorded_at IS NOT NULL
		ORDER BY bucket
	`, slot)
	if err != nil {
		return []activityBucket{}
	}
	defer rows.Close()

	groups := make(map[int][]rawSample)
	for rows.Next() {
		var r rawSample
		if err := rows.Scan(&r.bucket, &r.tool, &r.total, &r.working, &r.waiting, &r.idle, &r.unknown); err != nil {
			continue
		}
		groups[r.bucket/mergeCount] = append(groups[r.bucket/mergeCount], r)
	}

	if len(groups) == 0 {
		return []activityBucket{}
	}

	maxGroup := 0
	for k := range groups {
		if k > maxGroup {
			maxGroup = k
		}
	}

	var result []activityBucket
	for i := 0; i <= maxGroup; i++ {
		if raws, ok := groups[i]; ok {
			result = append(result, mergeSamples(i, raws))
		}
	}
	return result
}

func mergeSamples(idx int, raws []rawSample) activityBucket {
	n := len(raws)
	if n == 0 {
		return activityBucket{Bucket: idx}
	}

	b := activityBucket{Bucket: idx, Samples: n}
	var sumTotal, sumWorking, sumWaiting, sumIdle, sumUnknown int
	for _, r := range raws {
		switch r.tool {
		case "nvim", "vim":
			b.Nvim++
		case "claude":
			b.Claude++
		case "other":
			b.Other++
		case "away":
			b.Away++
		default:
			b.Inactive++
		}
		sumTotal += r.total
		sumWorking += r.working
		sumWaiting += r.waiting
		sumIdle += r.idle
		sumUnknown += r.unknown
	}

	b.ClaudeTotal = int(math.Round(float64(sumTotal) / float64(n)))
	b.ClaudeWorking = int(math.Round(float64(sumWorking) / float64(n)))
	b.ClaudeWaiting = int(math.Round(float64(sumWaiting) / float64(n)))
	b.ClaudeIdle = int(math.Round(float64(sumIdle) / float64(n)))
	b.ClaudeUnknown = int(math.Round(float64(sumUnknown) / float64(n)))
	return b
}
