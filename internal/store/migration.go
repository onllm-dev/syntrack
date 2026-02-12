package store

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// MigrationResult contains the results of a cycle migration
type MigrationResult struct {
	Provider       string
	QuotaType      string
	CyclesFixed    int
	CyclesCreated  int
	CyclesDeleted  int
	SnapshotsUsed  int
}

// RunCycleMigrationIfNeeded checks for bad cycles and fixes them if migration hasn't run yet.
// This is safe to call multiple times - it will only run once per migration version.
func (s *Store) RunCycleMigrationIfNeeded(logger *slog.Logger) ([]MigrationResult, error) {
	const migrationKey = "cycle_migration_v2" // v2: improved boundary detection with utilization drops

	// Check if migration has already run
	var completed string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, migrationKey).Scan(&completed)
	if err == nil && completed == "completed" {
		logger.Debug("Cycle migration already completed, skipping")
		return nil, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("failed to check migration status: %w", err)
	}

	// Detect if there are any bad cycles that need fixing
	badCycleCount, err := s.countBadCycles()
	if err != nil {
		return nil, fmt.Errorf("failed to count bad cycles: %w", err)
	}

	if badCycleCount == 0 {
		// No bad cycles, mark migration as complete
		logger.Info("No bad cycles detected, marking migration complete")
		if err := s.SetSetting(migrationKey, "completed"); err != nil {
			return nil, fmt.Errorf("failed to mark migration complete: %w", err)
		}
		return nil, nil
	}

	logger.Info("Detected bad cycles, running migration",
		"badCycleCount", badCycleCount,
	)

	// Run the actual migration
	results, err := s.fixBadCycles(logger)
	if err != nil {
		return nil, fmt.Errorf("failed to fix bad cycles: %w", err)
	}

	// Mark migration as complete
	if err := s.SetSetting(migrationKey, "completed"); err != nil {
		return nil, fmt.Errorf("failed to mark migration complete: %w", err)
	}

	logger.Info("Cycle migration completed successfully",
		"results", results,
	)

	return results, nil
}

// countBadCycles counts cycles with abnormal durations across all providers
func (s *Store) countBadCycles() (int, error) {
	var total int

	// Anthropic: 5-hour cycles should be ~5h, 7-day cycles ~168h
	// Count cycles where duration > 1.5x expected
	anthropicCount, err := s.countBadAnthropicCycles()
	if err != nil {
		return 0, err
	}
	total += anthropicCount

	// Synthetic: subscription renews daily, search hourly, toolcall varies
	// Count cycles where duration > 2x expected
	syntheticCount, err := s.countBadSyntheticCycles()
	if err != nil {
		return 0, err
	}
	total += syntheticCount

	// Z.ai: tokens quota has varying reset periods
	zaiCount, err := s.countBadZaiCycles()
	if err != nil {
		return 0, err
	}
	total += zaiCount

	return total, nil
}

// countBadAnthropicCycles counts Anthropic cycles with abnormal durations
func (s *Store) countBadAnthropicCycles() (int, error) {
	// five_hour cycles > 6 hours are suspicious
	// seven_day cycles > 8 days are suspicious
	query := `
		SELECT COUNT(*) FROM anthropic_reset_cycles
		WHERE cycle_end IS NOT NULL AND (
			(quota_name = 'five_hour' AND
			 (julianday(cycle_end) - julianday(cycle_start)) * 24 > 6) OR
			(quota_name LIKE 'seven_day%' AND
			 (julianday(cycle_end) - julianday(cycle_start)) > 8) OR
			(quota_name = 'monthly_limit' AND
			 (julianday(cycle_end) - julianday(cycle_start)) > 32)
		)
	`
	var count int
	if err := s.db.QueryRow(query).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count bad anthropic cycles: %w", err)
	}
	return count, nil
}

// countBadSyntheticCycles counts Synthetic cycles with abnormal durations
func (s *Store) countBadSyntheticCycles() (int, error) {
	// search quota cycles > 2 hours are suspicious (hourly reset)
	// subscription cycles > 48 hours are suspicious
	query := `
		SELECT COUNT(*) FROM reset_cycles
		WHERE provider = 'synthetic' AND cycle_end IS NOT NULL AND (
			(quota_type = 'search' AND
			 (julianday(cycle_end) - julianday(cycle_start)) * 24 > 2) OR
			(quota_type = 'subscription' AND
			 (julianday(cycle_end) - julianday(cycle_start)) * 24 > 48)
		)
	`
	var count int
	if err := s.db.QueryRow(query).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count bad synthetic cycles: %w", err)
	}
	return count, nil
}

// countBadZaiCycles counts Z.ai cycles with abnormal durations
func (s *Store) countBadZaiCycles() (int, error) {
	// tokens quota - check for cycles > 2x the gap between stored reset times
	// This is harder to detect without knowing the expected period, so we check
	// for cycles > 48 hours as a heuristic
	query := `
		SELECT COUNT(*) FROM zai_reset_cycles
		WHERE cycle_end IS NOT NULL AND quota_type = 'tokens' AND
		(julianday(cycle_end) - julianday(cycle_start)) * 24 > 48
	`
	var count int
	if err := s.db.QueryRow(query).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count bad zai cycles: %w", err)
	}
	return count, nil
}

// fixBadCycles fixes all bad cycles across all providers
func (s *Store) fixBadCycles(logger *slog.Logger) ([]MigrationResult, error) {
	var results []MigrationResult

	// Fix Anthropic cycles
	anthropicResults, err := s.fixBadAnthropicCycles(logger)
	if err != nil {
		return nil, fmt.Errorf("failed to fix anthropic cycles: %w", err)
	}
	results = append(results, anthropicResults...)

	// Fix Synthetic cycles
	syntheticResults, err := s.fixBadSyntheticCycles(logger)
	if err != nil {
		return nil, fmt.Errorf("failed to fix synthetic cycles: %w", err)
	}
	results = append(results, syntheticResults...)

	// Fix Z.ai cycles
	zaiResults, err := s.fixBadZaiCycles(logger)
	if err != nil {
		return nil, fmt.Errorf("failed to fix zai cycles: %w", err)
	}
	results = append(results, zaiResults...)

	return results, nil
}

// fixBadAnthropicCycles recalculates Anthropic cycles based on snapshot data
func (s *Store) fixBadAnthropicCycles(logger *slog.Logger) ([]MigrationResult, error) {
	var results []MigrationResult

	// Get all quota names that have bad cycles
	quotaNames, err := s.getAnthropicQuotasWithBadCycles()
	if err != nil {
		return nil, err
	}

	for _, quotaName := range quotaNames {
		result, err := s.fixAnthropicQuotaCycles(quotaName, logger)
		if err != nil {
			logger.Error("Failed to fix cycles for quota",
				"quota", quotaName,
				"error", err,
			)
			continue
		}
		results = append(results, *result)
	}

	return results, nil
}

// getAnthropicQuotasWithBadCycles returns quota names that have cycles needing repair
func (s *Store) getAnthropicQuotasWithBadCycles() ([]string, error) {
	query := `
		SELECT DISTINCT quota_name FROM anthropic_reset_cycles
		WHERE cycle_end IS NOT NULL AND (
			(quota_name = 'five_hour' AND
			 (julianday(cycle_end) - julianday(cycle_start)) * 24 > 6) OR
			(quota_name LIKE 'seven_day%' AND
			 (julianday(cycle_end) - julianday(cycle_start)) > 8) OR
			(quota_name = 'monthly_limit' AND
			 (julianday(cycle_end) - julianday(cycle_start)) > 32)
		)
	`
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to get bad quota names: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// fixAnthropicQuotaCycles fixes all bad cycles for a single Anthropic quota
func (s *Store) fixAnthropicQuotaCycles(quotaName string, logger *slog.Logger) (*MigrationResult, error) {
	result := &MigrationResult{
		Provider:  "anthropic",
		QuotaType: quotaName,
	}

	// Get bad cycles for this quota
	badCycles, err := s.getBadAnthropicCycles(quotaName)
	if err != nil {
		return nil, err
	}

	for _, cycle := range badCycles {
		fixed, created, snapshots, err := s.recalculateAnthropicCycle(cycle, quotaName, logger)
		if err != nil {
			logger.Error("Failed to fix cycle",
				"cycleID", cycle.ID,
				"error", err,
			)
			continue
		}
		if fixed {
			result.CyclesFixed++
			result.CyclesCreated += created
			result.SnapshotsUsed += snapshots
		}
	}

	return result, nil
}

// getBadAnthropicCycles returns cycles that need repair for a quota
func (s *Store) getBadAnthropicCycles(quotaName string) ([]*AnthropicResetCycle, error) {
	var maxHours float64
	switch {
	case quotaName == "five_hour":
		maxHours = 6
	case quotaName == "seven_day" || quotaName == "seven_day_sonnet":
		maxHours = 8 * 24 // 8 days
	case quotaName == "monthly_limit":
		maxHours = 32 * 24 // 32 days
	default:
		maxHours = 6 // default to 5-hour window
	}

	query := `
		SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM anthropic_reset_cycles
		WHERE quota_name = ? AND cycle_end IS NOT NULL AND
		(julianday(cycle_end) - julianday(cycle_start)) * 24 > ?
		ORDER BY cycle_start ASC
	`
	rows, err := s.db.Query(query, quotaName, maxHours)
	if err != nil {
		return nil, fmt.Errorf("failed to get bad cycles: %w", err)
	}
	defer rows.Close()

	var cycles []*AnthropicResetCycle
	for rows.Next() {
		var cycle AnthropicResetCycle
		var cycleStart, cycleEnd string
		var resetsAt sql.NullString

		if err := rows.Scan(&cycle.ID, &cycle.QuotaName, &cycleStart, &cycleEnd, &resetsAt,
			&cycle.PeakUtilization, &cycle.TotalDelta); err != nil {
			return nil, err
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd)
		cycle.CycleEnd = &t
		if resetsAt.Valid {
			rt, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
			cycle.ResetsAt = &rt
		}

		cycles = append(cycles, &cycle)
	}
	return cycles, rows.Err()
}

// recalculateAnthropicCycle splits a bad cycle into proper sub-cycles based on reset times
func (s *Store) recalculateAnthropicCycle(cycle *AnthropicResetCycle, quotaName string, logger *slog.Logger) (bool, int, int, error) {
	// Get all snapshots within this cycle's time range
	snapshots, err := s.getAnthropicSnapshotsInRange(quotaName, cycle.CycleStart, *cycle.CycleEnd)
	if err != nil {
		return false, 0, 0, err
	}

	if len(snapshots) < 2 {
		logger.Debug("Not enough snapshots to recalculate cycle",
			"cycleID", cycle.ID,
			"snapshotCount", len(snapshots),
		)
		return false, 0, 0, nil
	}

	// Find reset boundaries (where resets_at changes significantly)
	boundaries := findResetBoundaries(snapshots)
	if len(boundaries) == 0 {
		logger.Debug("No reset boundaries found in cycle",
			"cycleID", cycle.ID,
		)
		return false, 0, 0, nil
	}

	logger.Info("Found reset boundaries in bad cycle",
		"cycleID", cycle.ID,
		"boundaries", len(boundaries),
		"originalDuration", cycle.CycleEnd.Sub(cycle.CycleStart),
	)

	// Start transaction
	tx, err := s.db.Begin()
	if err != nil {
		return false, 0, 0, err
	}
	defer tx.Rollback()

	// Delete the bad cycle
	if _, err := tx.Exec(`DELETE FROM anthropic_reset_cycles WHERE id = ?`, cycle.ID); err != nil {
		return false, 0, 0, fmt.Errorf("failed to delete bad cycle: %w", err)
	}

	// Create new cycles based on boundaries
	created := 0
	prevBoundary := cycle.CycleStart
	var prevResetsAt *time.Time

	for i, boundary := range boundaries {
		// Calculate stats for this sub-cycle
		subSnapshots := filterSnapshotsByRange(snapshots, prevBoundary, boundary.Time)
		peak, delta := calculateCycleStats(subSnapshots)

		// Get the resets_at for this cycle (from the boundary snapshot)
		var resetsAtVal interface{}
		if boundary.ResetsAt != nil {
			resetsAtVal = boundary.ResetsAt.Format(time.RFC3339Nano)
			prevResetsAt = boundary.ResetsAt
		} else if prevResetsAt != nil {
			resetsAtVal = prevResetsAt.Format(time.RFC3339Nano)
		}

		// Insert the new cycle
		_, err := tx.Exec(`
			INSERT INTO anthropic_reset_cycles
			(quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta)
			VALUES (?, ?, ?, ?, ?, ?)`,
			quotaName,
			prevBoundary.Format(time.RFC3339Nano),
			boundary.Time.Format(time.RFC3339Nano),
			resetsAtVal,
			peak,
			delta,
		)
		if err != nil {
			return false, 0, 0, fmt.Errorf("failed to insert sub-cycle: %w", err)
		}
		created++

		logger.Debug("Created sub-cycle",
			"index", i,
			"start", prevBoundary,
			"end", boundary.Time,
			"peak", peak,
			"delta", delta,
		)

		prevBoundary = boundary.Time
	}

	// Create final cycle from last boundary to original cycle end
	if prevBoundary.Before(*cycle.CycleEnd) {
		subSnapshots := filterSnapshotsByRange(snapshots, prevBoundary, *cycle.CycleEnd)
		peak, delta := calculateCycleStats(subSnapshots)

		var resetsAtVal interface{}
		if prevResetsAt != nil {
			resetsAtVal = prevResetsAt.Format(time.RFC3339Nano)
		}

		_, err := tx.Exec(`
			INSERT INTO anthropic_reset_cycles
			(quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta)
			VALUES (?, ?, ?, ?, ?, ?)`,
			quotaName,
			prevBoundary.Format(time.RFC3339Nano),
			cycle.CycleEnd.Format(time.RFC3339Nano),
			resetsAtVal,
			peak,
			delta,
		)
		if err != nil {
			return false, 0, 0, fmt.Errorf("failed to insert final sub-cycle: %w", err)
		}
		created++
	}

	if err := tx.Commit(); err != nil {
		return false, 0, 0, fmt.Errorf("failed to commit: %w", err)
	}

	return true, created, len(snapshots), nil
}

// snapshotPoint represents a point in time with utilization and reset info
type snapshotPoint struct {
	CapturedAt  time.Time
	Utilization float64
	ResetsAt    *time.Time
}

// resetBoundary represents a detected reset point
type resetBoundary struct {
	Time     time.Time
	ResetsAt *time.Time
}

// getAnthropicSnapshotsInRange returns snapshots for a quota within a time range
func (s *Store) getAnthropicSnapshotsInRange(quotaName string, start, end time.Time) ([]snapshotPoint, error) {
	query := `
		SELECT s.captured_at, qv.utilization, qv.resets_at
		FROM anthropic_quota_values qv
		JOIN anthropic_snapshots s ON s.id = qv.snapshot_id
		WHERE qv.quota_name = ? AND s.captured_at >= ? AND s.captured_at <= ?
		ORDER BY s.captured_at ASC
	`
	rows, err := s.db.Query(query, quotaName, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("failed to get snapshots: %w", err)
	}
	defer rows.Close()

	var points []snapshotPoint
	for rows.Next() {
		var capturedAt string
		var util float64
		var resetsAt sql.NullString

		if err := rows.Scan(&capturedAt, &util, &resetsAt); err != nil {
			return nil, err
		}

		point := snapshotPoint{Utilization: util}
		point.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		if resetsAt.Valid && resetsAt.String != "" {
			t, _ := time.Parse(time.RFC3339Nano, resetsAt.String)
			point.ResetsAt = &t
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

// findResetBoundaries detects reset points where resets_at changes significantly
// or where utilization drops significantly (indicating a reset happened while app was offline)
func findResetBoundaries(snapshots []snapshotPoint) []resetBoundary {
	var boundaries []resetBoundary
	if len(snapshots) < 2 {
		return boundaries
	}

	var lastResetsAt *time.Time
	var lastUtil float64
	var lastCapturedAt time.Time

	for i, snap := range snapshots {
		if i == 0 {
			lastResetsAt = snap.ResetsAt
			lastUtil = snap.Utilization
			lastCapturedAt = snap.CapturedAt
			continue
		}

		boundaryDetected := false
		var boundaryResetsAt *time.Time

		// Method 1: Check if resets_at changed significantly (>10 minutes)
		if snap.ResetsAt != nil && lastResetsAt != nil {
			diff := snap.ResetsAt.Sub(*lastResetsAt)
			if diff < 0 {
				diff = -diff
			}
			if diff > 10*time.Minute {
				boundaryDetected = true
				boundaryResetsAt = snap.ResetsAt
			}
		} else if snap.ResetsAt != nil && lastResetsAt == nil {
			// resets_at appeared (was nil, now has value)
			boundaryDetected = true
			boundaryResetsAt = snap.ResetsAt
		} else if snap.ResetsAt == nil && lastResetsAt != nil {
			// resets_at disappeared (had value, now nil) - also indicates reset
			boundaryDetected = true
			boundaryResetsAt = lastResetsAt // Use the last known reset time
		}

		// Method 2: Check for significant utilization drop (>20% drop indicates reset)
		// This catches resets that happened while app was offline
		if !boundaryDetected && lastUtil > 20 && snap.Utilization < lastUtil-20 {
			// Large drop in utilization - likely a reset
			boundaryDetected = true
			// Use the last known resets_at or estimate based on time gap
			if lastResetsAt != nil {
				boundaryResetsAt = lastResetsAt
			}
		}

		// Method 3: Check for significant time gap (>1 hour) with utilization drop
		// This catches periods where app was offline during a reset
		if !boundaryDetected {
			timeGap := snap.CapturedAt.Sub(lastCapturedAt)
			if timeGap > time.Hour && snap.Utilization < lastUtil {
				boundaryDetected = true
				if lastResetsAt != nil {
					boundaryResetsAt = lastResetsAt
				}
			}
		}

		if boundaryDetected {
			boundaries = append(boundaries, resetBoundary{
				Time:     snap.CapturedAt,
				ResetsAt: boundaryResetsAt,
			})
		}

		lastResetsAt = snap.ResetsAt
		lastUtil = snap.Utilization
		lastCapturedAt = snap.CapturedAt
	}

	return boundaries
}

// filterSnapshotsByRange returns snapshots within a time range
func filterSnapshotsByRange(snapshots []snapshotPoint, start, end time.Time) []snapshotPoint {
	var filtered []snapshotPoint
	for _, snap := range snapshots {
		if (snap.CapturedAt.Equal(start) || snap.CapturedAt.After(start)) &&
			snap.CapturedAt.Before(end) {
			filtered = append(filtered, snap)
		}
	}
	return filtered
}

// calculateCycleStats calculates peak utilization and total delta for a set of snapshots
func calculateCycleStats(snapshots []snapshotPoint) (peak, delta float64) {
	if len(snapshots) == 0 {
		return 0, 0
	}

	var lastUtil float64
	for i, snap := range snapshots {
		if snap.Utilization > peak {
			peak = snap.Utilization
		}
		if i > 0 {
			d := snap.Utilization - lastUtil
			if d > 0 {
				delta += d
			}
		}
		lastUtil = snap.Utilization
	}
	return peak, delta
}

// fixBadSyntheticCycles fixes Synthetic provider cycles (placeholder - implement if needed)
func (s *Store) fixBadSyntheticCycles(logger *slog.Logger) ([]MigrationResult, error) {
	// Check if there are any bad Synthetic cycles
	count, err := s.countBadSyntheticCycles()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}

	logger.Info("Found bad Synthetic cycles",
		"count", count,
	)

	// For now, just log - Synthetic cycles can be fixed similarly to Anthropic
	// but require different snapshot queries
	return nil, nil
}

// fixBadZaiCycles fixes Z.ai provider cycles (placeholder - implement if needed)
func (s *Store) fixBadZaiCycles(logger *slog.Logger) ([]MigrationResult, error) {
	// Check if there are any bad Z.ai cycles
	count, err := s.countBadZaiCycles()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}

	logger.Info("Found bad Z.ai cycles",
		"count", count,
	)

	// For now, just log - Z.ai cycles can be fixed similarly to Anthropic
	// but require different snapshot queries
	return nil, nil
}
