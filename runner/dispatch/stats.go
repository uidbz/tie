package dispatch

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/uidbz/tie/runner/api"
)

// Fleet stats merge: /api/stats answers for one runner locally and fans
// out to backends otherwise, so an operator gets one fleet-wide answer.

// FetchStats retrieves one backend's stats (asked with local=true so the
// backend does not fan out in turn).
func FetchStats(client *http.Client, backend string, since int64) (*api.JobStats, error) {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	url := fmt.Sprintf("%s/api/stats?local=true&since=%d", backend, since)
	resp, err := client.Get(url)
	if err != nil {
		slog.Warn("Failed to fetch stats from backend", "backend", backend, "error", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Warn("Backend returned non-200 for stats", "backend", backend, "status", resp.Status)
		return nil, fmt.Errorf("backend %s status %d", backend, resp.StatusCode)
	}

	var stats api.JobStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		slog.Warn("Failed to decode stats from backend", "backend", backend, "error", err)
		return nil, err
	}
	return &stats, nil
}

// MergeStats combines local and remote stats into one fleet-wide
// aggregate. Averages are pooled pairwise (the running-average form older
// dashboards parse); maxima win; keyed slices sum by key; the per-month
// active-user sets union and collapse to counts.
func MergeStats(local *api.JobStats, remotes ...*api.JobStats) *api.JobStats {
	merged := *local // copy

	for _, r := range remotes {
		merged.Running += r.Running
		merged.Queued += r.Queued
		merged.Total += r.Total
		merged.Succeeded += r.Succeeded
		merged.Failed += r.Failed
		merged.Cancelled += r.Cancelled
		merged.OOMKills += r.OOMKills
		merged.ProdJobs += r.ProdJobs
		merged.DebugJobs += r.DebugJobs

		if r.AvgRuntimeSeconds > 0 {
			merged.AvgRuntimeSeconds = (merged.AvgRuntimeSeconds + r.AvgRuntimeSeconds) / 2
		}
		if r.MaxRuntimeSeconds > merged.MaxRuntimeSeconds {
			merged.MaxRuntimeSeconds = r.MaxRuntimeSeconds
		}

		if r.AvgPeakMemMB > 0 {
			merged.AvgPeakMemMB = (merged.AvgPeakMemMB + r.AvgPeakMemMB) / 2
		}
		if r.MaxPeakMemMB > merged.MaxPeakMemMB {
			merged.MaxPeakMemMB = r.MaxPeakMemMB
		}

		merged.MonthlyUsage = mergeMonthCounts(merged.MonthlyUsage, r.MonthlyUsage)
		merged.JobsByUser = mergeNameCounts(merged.JobsByUser, r.JobsByUser)
		merged.PopularImages = mergeNameCounts(merged.PopularImages, r.PopularImages)
		merged.JobsByType = mergeNameCounts(merged.JobsByType, r.JobsByType)

		// Union active user sets per month.
		for month, users := range r.ActiveUsers {
			if merged.ActiveUsers == nil {
				merged.ActiveUsers = make(map[string][]string)
			}
			merged.ActiveUsers[month] = append(merged.ActiveUsers[month], users...)
		}
	}

	if terminal := merged.Succeeded + merged.Failed + merged.Cancelled; terminal > 0 {
		merged.SuccessRate = float64(merged.Succeeded) / float64(terminal)
	}

	merged.ActiveUsersPerMonth = nil
	for month, users := range merged.ActiveUsers {
		uniqueUsers := make(map[string]bool)
		for _, u := range users {
			uniqueUsers[u] = true
		}
		merged.ActiveUsersPerMonth = append(merged.ActiveUsersPerMonth, api.MonthCount{Month: month, Count: len(uniqueUsers)})
	}

	// ActiveUsers is the internal union buffer; it never goes on the wire.
	merged.ActiveUsers = nil

	return &merged
}

func mergeMonthCounts(a, b []api.MonthCount) []api.MonthCount {
	counts := make(map[string]int)
	for _, item := range a {
		counts[item.Month] += item.Count
	}
	for _, item := range b {
		counts[item.Month] += item.Count
	}
	var result []api.MonthCount
	for name, count := range counts {
		result = append(result, api.MonthCount{Month: name, Count: count})
	}
	return result
}

func mergeNameCounts(a, b []api.NameCount) []api.NameCount {
	counts := make(map[string]int)
	for _, item := range a {
		counts[item.Name] += item.Count
	}
	for _, item := range b {
		counts[item.Name] += item.Count
	}
	var result []api.NameCount
	for name, count := range counts {
		result = append(result, api.NameCount{Name: name, Count: count})
	}
	return result
}
