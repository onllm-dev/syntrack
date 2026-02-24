package store

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/onllm-dev/onwatch/internal/api"
)

// AntigravityResetCycle represents an Antigravity quota reset cycle.
type AntigravityResetCycle struct {
	ID         int64
	ModelID    string
	CycleStart time.Time
	CycleEnd   *time.Time
	ResetTime  *time.Time
	PeakUsage  float64
	TotalDelta float64
}

// AntigravityUsagePoint is a lightweight time+remaining pair for rate/series computation.
type AntigravityUsagePoint struct {
	CapturedAt        time.Time
	RemainingFraction float64
}

// InsertAntigravitySnapshot inserts an Antigravity snapshot with its model quotas.
func (s *Store) InsertAntigravitySnapshot(snapshot *api.AntigravitySnapshot) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`INSERT INTO antigravity_snapshots (captured_at, email, plan_name, prompt_credits, monthly_credits, raw_json, model_count)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		snapshot.CapturedAt.Format(time.RFC3339Nano),
		snapshot.Email,
		snapshot.PlanName,
		snapshot.PromptCredits,
		snapshot.MonthlyCredits,
		snapshot.RawJSON,
		len(snapshot.Models),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert antigravity snapshot: %w", err)
	}

	snapshotID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get snapshot ID: %w", err)
	}

	for _, m := range snapshot.Models {
		var resetTimeVal interface{}
		if m.ResetTime != nil {
			resetTimeVal = m.ResetTime.Format(time.RFC3339Nano)
		}

		exhausted := 0
		if m.IsExhausted {
			exhausted = 1
		}

		_, err := tx.Exec(
			`INSERT INTO antigravity_model_values (snapshot_id, model_id, label, remaining_fraction, remaining_percent, is_exhausted, reset_time)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			snapshotID, m.ModelID, m.Label, m.RemainingFraction, m.RemainingPercent, exhausted, resetTimeVal,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to insert antigravity model value %s: %w", m.ModelID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit: %w", err)
	}

	return snapshotID, nil
}

// QueryLatestAntigravity returns the most recent Antigravity snapshot with models.
func (s *Store) QueryLatestAntigravity() (*api.AntigravitySnapshot, error) {
	var snapshot api.AntigravitySnapshot
	var capturedAt string
	var email, planName sql.NullString
	var promptCredits sql.NullFloat64
	var monthlyCredits sql.NullInt64

	err := s.db.QueryRow(
		`SELECT id, captured_at, email, plan_name, prompt_credits, monthly_credits, model_count
		FROM antigravity_snapshots ORDER BY captured_at DESC LIMIT 1`,
	).Scan(&snapshot.ID, &capturedAt, &email, &planName, &promptCredits, &monthlyCredits, new(int))

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest antigravity: %w", err)
	}

	snapshot.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
	if email.Valid {
		snapshot.Email = email.String
	}
	if planName.Valid {
		snapshot.PlanName = planName.String
	}
	if promptCredits.Valid {
		snapshot.PromptCredits = promptCredits.Float64
	}
	if monthlyCredits.Valid {
		snapshot.MonthlyCredits = int(monthlyCredits.Int64)
	}

	// Load model values
	rows, err := s.db.Query(
		`SELECT model_id, label, remaining_fraction, remaining_percent, is_exhausted, reset_time
		FROM antigravity_model_values WHERE snapshot_id = ? ORDER BY model_id`,
		snapshot.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query antigravity model values: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var m api.AntigravityModelQuota
		var exhausted int
		var resetTime sql.NullString
		if err := rows.Scan(&m.ModelID, &m.Label, &m.RemainingFraction, &m.RemainingPercent, &exhausted, &resetTime); err != nil {
			return nil, fmt.Errorf("failed to scan antigravity model value: %w", err)
		}
		m.IsExhausted = exhausted == 1
		if resetTime.Valid && resetTime.String != "" {
			t, _ := time.Parse(time.RFC3339Nano, resetTime.String)
			m.ResetTime = &t
			m.TimeUntilReset = time.Until(t)
			if m.TimeUntilReset < 0 {
				m.TimeUntilReset = 0
			}
		}
		snapshot.Models = append(snapshot.Models, m)
	}

	return &snapshot, rows.Err()
}

// QueryAntigravityRange returns Antigravity snapshots within a time range.
func (s *Store) QueryAntigravityRange(start, end time.Time, limit ...int) ([]*api.AntigravitySnapshot, error) {
	// Order by ASC for chronological chart display (oldest to newest, left to right)
	query := `SELECT id, captured_at, email, plan_name, prompt_credits, monthly_credits, model_count
		FROM antigravity_snapshots
		WHERE captured_at BETWEEN ? AND ? ORDER BY captured_at ASC`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query antigravity range: %w", err)
	}
	defer rows.Close()

	var snapshots []*api.AntigravitySnapshot
	for rows.Next() {
		var snap api.AntigravitySnapshot
		var capturedAt string
		var email, planName sql.NullString
		var promptCredits sql.NullFloat64
		var monthlyCredits sql.NullInt64

		if err := rows.Scan(&snap.ID, &capturedAt, &email, &planName, &promptCredits, &monthlyCredits, new(int)); err != nil {
			return nil, fmt.Errorf("failed to scan antigravity snapshot: %w", err)
		}
		snap.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		if email.Valid {
			snap.Email = email.String
		}
		if planName.Valid {
			snap.PlanName = planName.String
		}
		if promptCredits.Valid {
			snap.PromptCredits = promptCredits.Float64
		}
		if monthlyCredits.Valid {
			snap.MonthlyCredits = int(monthlyCredits.Int64)
		}
		snapshots = append(snapshots, &snap)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load model values for each snapshot
	for _, snap := range snapshots {
		mRows, err := s.db.Query(
			`SELECT model_id, label, remaining_fraction, remaining_percent, is_exhausted, reset_time
			FROM antigravity_model_values WHERE snapshot_id = ? ORDER BY model_id`,
			snap.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to query antigravity model values for snapshot %d: %w", snap.ID, err)
		}
		for mRows.Next() {
			var m api.AntigravityModelQuota
			var exhausted int
			var resetTime sql.NullString
			if err := mRows.Scan(&m.ModelID, &m.Label, &m.RemainingFraction, &m.RemainingPercent, &exhausted, &resetTime); err != nil {
				mRows.Close()
				return nil, fmt.Errorf("failed to scan antigravity model value: %w", err)
			}
			m.IsExhausted = exhausted == 1
			if resetTime.Valid && resetTime.String != "" {
				t, _ := time.Parse(time.RFC3339Nano, resetTime.String)
				m.ResetTime = &t
			}
			snap.Models = append(snap.Models, m)
		}
		mRows.Close()
	}

	return snapshots, nil
}

// CreateAntigravityCycle creates a new Antigravity reset cycle.
func (s *Store) CreateAntigravityCycle(modelID string, cycleStart time.Time, resetTime *time.Time) (int64, error) {
	var resetTimeVal interface{}
	if resetTime != nil {
		resetTimeVal = resetTime.Format(time.RFC3339Nano)
	}

	result, err := s.db.Exec(
		`INSERT INTO antigravity_reset_cycles (model_id, cycle_start, reset_time) VALUES (?, ?, ?)`,
		modelID, cycleStart.Format(time.RFC3339Nano), resetTimeVal,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create antigravity cycle: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get cycle ID: %w", err)
	}
	return id, nil
}

// CloseAntigravityCycle closes an Antigravity reset cycle with final stats.
func (s *Store) CloseAntigravityCycle(modelID string, cycleEnd time.Time, peakUsage, totalDelta float64) error {
	_, err := s.db.Exec(
		`UPDATE antigravity_reset_cycles SET cycle_end = ?, peak_usage = ?, total_delta = ?
		WHERE model_id = ? AND cycle_end IS NULL`,
		cycleEnd.Format(time.RFC3339Nano), peakUsage, totalDelta, modelID,
	)
	if err != nil {
		return fmt.Errorf("failed to close antigravity cycle: %w", err)
	}
	return nil
}

// UpdateAntigravityCycle updates the peak and delta for an active Antigravity cycle.
func (s *Store) UpdateAntigravityCycle(modelID string, peakUsage, totalDelta float64) error {
	_, err := s.db.Exec(
		`UPDATE antigravity_reset_cycles SET peak_usage = ?, total_delta = ?
		WHERE model_id = ? AND cycle_end IS NULL`,
		peakUsage, totalDelta, modelID,
	)
	if err != nil {
		return fmt.Errorf("failed to update antigravity cycle: %w", err)
	}
	return nil
}

// QueryActiveAntigravityCycle returns the active cycle for an Antigravity model.
func (s *Store) QueryActiveAntigravityCycle(modelID string) (*AntigravityResetCycle, error) {
	var cycle AntigravityResetCycle
	var cycleStart string
	var cycleEnd, resetTime sql.NullString

	err := s.db.QueryRow(
		`SELECT id, model_id, cycle_start, cycle_end, reset_time, peak_usage, total_delta
		FROM antigravity_reset_cycles
		WHERE model_id = ? AND cycle_end IS NULL
		ORDER BY cycle_start DESC, id DESC
		LIMIT 1`,
		modelID,
	).Scan(&cycle.ID, &cycle.ModelID, &cycleStart, &cycleEnd, &resetTime, &cycle.PeakUsage, &cycle.TotalDelta)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active antigravity cycle: %w", err)
	}

	cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
	if cycleEnd.Valid {
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd.String)
		cycle.CycleEnd = &t
	}
	if resetTime.Valid {
		t, _ := time.Parse(time.RFC3339Nano, resetTime.String)
		cycle.ResetTime = &t
	}

	return &cycle, nil
}

// QueryAntigravityCycleHistory returns completed cycles for an Antigravity model with optional limit.
func (s *Store) QueryAntigravityCycleHistory(modelID string, limit ...int) ([]*AntigravityResetCycle, error) {
	query := `SELECT id, model_id, cycle_start, cycle_end, reset_time, peak_usage, total_delta
		FROM antigravity_reset_cycles WHERE model_id = ? AND cycle_end IS NOT NULL ORDER BY cycle_start DESC`
	args := []interface{}{modelID}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query antigravity cycles: %w", err)
	}
	defer rows.Close()

	var cycles []*AntigravityResetCycle
	for rows.Next() {
		var cycle AntigravityResetCycle
		var cycleStart, cycleEnd string
		var resetTime sql.NullString

		if err := rows.Scan(&cycle.ID, &cycle.ModelID, &cycleStart, &cycleEnd, &resetTime,
			&cycle.PeakUsage, &cycle.TotalDelta); err != nil {
			return nil, fmt.Errorf("failed to scan antigravity cycle: %w", err)
		}

		cycle.CycleStart, _ = time.Parse(time.RFC3339Nano, cycleStart)
		t, _ := time.Parse(time.RFC3339Nano, cycleEnd)
		cycle.CycleEnd = &t
		if resetTime.Valid {
			rt, _ := time.Parse(time.RFC3339Nano, resetTime.String)
			cycle.ResetTime = &rt
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// QueryAntigravityUsageSeries returns per-model usage points since a given time.
func (s *Store) QueryAntigravityUsageSeries(modelID string, since time.Time) ([]AntigravityUsagePoint, error) {
	rows, err := s.db.Query(
		`SELECT s.captured_at, mv.remaining_fraction
		FROM antigravity_model_values mv
		JOIN antigravity_snapshots s ON s.id = mv.snapshot_id
		WHERE mv.model_id = ? AND s.captured_at >= ?
		ORDER BY s.captured_at ASC`,
		modelID, since.Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query antigravity usage series: %w", err)
	}
	defer rows.Close()

	var points []AntigravityUsagePoint
	for rows.Next() {
		var capturedAt string
		var pt AntigravityUsagePoint
		if err := rows.Scan(&capturedAt, &pt.RemainingFraction); err != nil {
			return nil, fmt.Errorf("failed to scan antigravity usage point: %w", err)
		}
		pt.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		points = append(points, pt)
	}

	return points, rows.Err()
}

// QueryAntigravityHistory returns Antigravity snapshots within a time range (alias for QueryAntigravityRange).
func (s *Store) QueryAntigravityHistory(start, end time.Time) ([]*api.AntigravitySnapshot, error) {
	return s.QueryAntigravityRange(start, end)
}

// QueryAllAntigravityModelIDs returns all distinct model IDs from Antigravity model values.
func (s *Store) QueryAllAntigravityModelIDs() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT model_id FROM antigravity_model_values ORDER BY model_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query antigravity model IDs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan antigravity model ID: %w", err)
		}
		ids = append(ids, id)
	}

	return ids, rows.Err()
}

// QueryAntigravityCycleOverview returns cycle overview rows for canonical Antigravity quota groups.
func (s *Store) QueryAntigravityCycleOverview(groupBy string, limit int) ([]CycleOverviewRow, error) {
	if groupBy == "" {
		groupBy = api.AntigravityQuotaGroupClaudeGPT
	}

	if !isAntigravityQuotaGroup(groupBy) {
		return nil, fmt.Errorf("invalid antigravity group: %s", groupBy)
	}

	groupModelIDs, err := s.QueryAntigravityModelIDsForGroup(groupBy)
	if err != nil {
		return nil, fmt.Errorf("failed to query model IDs for group %s: %w", groupBy, err)
	}
	if len(groupModelIDs) == 0 {
		return nil, nil
	}

	// Group cycles by start time (truncated to minute) instead of cycle ID
	// This ensures cycles from different models that reset together are merged
	cycleByStartTime := map[string]CycleOverviewRow{}
	const antigravityGroupedActiveKey = "active"
	var groupedActive *CycleOverviewRow

	for _, modelID := range groupModelIDs {
		active, err := s.QueryActiveAntigravityCycle(modelID)
		if err != nil {
			return nil, fmt.Errorf("failed to query active cycle for model %s: %w", modelID, err)
		}
		if active != nil {
			if groupedActive == nil {
				groupedActive = &CycleOverviewRow{
					CycleID:    -1,
					QuotaType:  groupBy,
					CycleStart: active.CycleStart,
					CycleEnd:   nil,
					PeakValue:  active.PeakUsage * 100,
					TotalDelta: active.TotalDelta * 100,
					PeakTime:   active.CycleStart,
				}
			} else {
				if active.CycleStart.Before(groupedActive.CycleStart) {
					groupedActive.CycleStart = active.CycleStart
				}
				if active.PeakUsage*100 > groupedActive.PeakValue {
					groupedActive.PeakValue = active.PeakUsage * 100
				}
				groupedActive.TotalDelta += active.TotalDelta * 100
			}
		}

		cycles, err := s.QueryAntigravityCycleHistory(modelID, limit)
		if err != nil {
			return nil, fmt.Errorf("failed to query cycle history for model %s: %w", modelID, err)
		}
		for _, cycle := range cycles {
			// Use start time truncated to minute as the grouping key
			startKey := cycle.CycleStart.Truncate(time.Minute).Format(time.RFC3339)
			existing, ok := cycleByStartTime[startKey]
			if !ok {
				existing = CycleOverviewRow{
					CycleID:    cycle.ID, // Use first cycle's ID as representative
					QuotaType:  groupBy,
					CycleStart: cycle.CycleStart,
					CycleEnd:   cycle.CycleEnd,
					PeakValue:  cycle.PeakUsage * 100,
					TotalDelta: cycle.TotalDelta * 100,
					PeakTime:   cycle.CycleStart,
				}
			} else {
				// Merge: take earliest start, latest end, max peak, sum deltas
				if cycle.CycleStart.Before(existing.CycleStart) {
					existing.CycleStart = cycle.CycleStart
				}
				if cycle.CycleEnd != nil {
					if existing.CycleEnd == nil || cycle.CycleEnd.After(*existing.CycleEnd) {
						existing.CycleEnd = cycle.CycleEnd
					}
				}
				if cycle.PeakUsage*100 > existing.PeakValue {
					existing.PeakValue = cycle.PeakUsage * 100
				}
				existing.TotalDelta += cycle.TotalDelta * 100
			}
			cycleByStartTime[startKey] = existing
		}
	}

	if groupedActive != nil {
		cycleByStartTime[antigravityGroupedActiveKey] = *groupedActive
	}

	if len(cycleByStartTime) == 0 {
		return nil, nil
	}

	rows := make([]CycleOverviewRow, 0, len(cycleByStartTime))
	for _, row := range cycleByStartTime {
		referenceTime := row.CycleStart
		if row.CycleEnd != nil {
			referenceTime = *row.CycleEnd
		}

		crossQuotas, err := s.getAntigravityGroupedCrossQuotasAt(referenceTime)
		if err != nil {
			return nil, fmt.Errorf("failed to build grouped cross quotas: %w", err)
		}
		row.CrossQuotas = crossQuotas
		rows = append(rows, row)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CycleEnd == nil && rows[j].CycleEnd != nil {
			return true
		}
		if rows[i].CycleEnd != nil && rows[j].CycleEnd == nil {
			return false
		}
		return rows[i].CycleStart.After(rows[j].CycleStart)
	})

	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}

	return rows, nil
}

func (s *Store) QueryAntigravityModelIDsForGroup(groupKey string) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT mv.model_id, mv.label
		 FROM antigravity_model_values mv
		 WHERE mv.model_id != ''
		 ORDER BY mv.model_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query antigravity model IDs by group: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var modelID, label string
		if err := rows.Scan(&modelID, &label); err != nil {
			return nil, fmt.Errorf("failed to scan antigravity model row: %w", err)
		}
		if api.AntigravityQuotaGroupForModel(modelID, label) == groupKey {
			ids = append(ids, modelID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed reading antigravity model rows: %w", err)
	}
	return ids, nil
}

func (s *Store) QueryAntigravitySnapshotAtOrBefore(t time.Time) (*api.AntigravitySnapshot, error) {
	var snapshot api.AntigravitySnapshot
	var capturedAt string
	var email, planName sql.NullString
	var promptCredits sql.NullFloat64
	var monthlyCredits sql.NullInt64

	err := s.db.QueryRow(
		`SELECT id, captured_at, email, plan_name, prompt_credits, monthly_credits, model_count
		 FROM antigravity_snapshots
		 WHERE captured_at <= ?
		 ORDER BY captured_at DESC
		 LIMIT 1`,
		t.Format(time.RFC3339Nano),
	).Scan(&snapshot.ID, &capturedAt, &email, &planName, &promptCredits, &monthlyCredits, new(int))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query antigravity snapshot at or before time: %w", err)
	}

	snapshot.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
	if email.Valid {
		snapshot.Email = email.String
	}
	if planName.Valid {
		snapshot.PlanName = planName.String
	}
	if promptCredits.Valid {
		snapshot.PromptCredits = promptCredits.Float64
	}
	if monthlyCredits.Valid {
		snapshot.MonthlyCredits = int(monthlyCredits.Int64)
	}

	rows, err := s.db.Query(
		`SELECT model_id, label, remaining_fraction, remaining_percent, is_exhausted, reset_time
		 FROM antigravity_model_values
		 WHERE snapshot_id = ?
		 ORDER BY model_id`,
		snapshot.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query model values for snapshot %d: %w", snapshot.ID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var m api.AntigravityModelQuota
		var exhausted int
		var resetTime sql.NullString
		if err := rows.Scan(&m.ModelID, &m.Label, &m.RemainingFraction, &m.RemainingPercent, &exhausted, &resetTime); err != nil {
			return nil, fmt.Errorf("failed to scan antigravity model value: %w", err)
		}
		m.IsExhausted = exhausted == 1
		if resetTime.Valid && resetTime.String != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, resetTime.String); err == nil {
				m.ResetTime = &parsed
			}
		}
		snapshot.Models = append(snapshot.Models, m)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed reading model values for snapshot %d: %w", snapshot.ID, err)
	}

	return &snapshot, nil
}

func (s *Store) getAntigravityGroupedCrossQuotasAt(referenceTime time.Time) ([]CrossQuotaEntry, error) {
	snapshot, err := s.QueryAntigravitySnapshotAtOrBefore(referenceTime)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		snapshot, err = s.QueryLatestAntigravity()
		if err != nil {
			return nil, err
		}
	}

	var grouped []api.AntigravityGroupedQuota
	if snapshot != nil {
		grouped = api.GroupAntigravityModelsByLogicalQuota(snapshot.Models)
	} else {
		grouped = api.GroupAntigravityModelsByLogicalQuota(nil)
	}

	if len(grouped) == 0 {
		grouped = api.GroupAntigravityModelsByLogicalQuota(nil)
	}

	startPercents := map[string]float64{}
	startSnapshot, err := s.QueryAntigravitySnapshotAtOrBefore(referenceTime.Add(-time.Second))
	if err == nil && startSnapshot != nil {
		startGroups := api.GroupAntigravityModelsByLogicalQuota(startSnapshot.Models)
		for _, g := range startGroups {
			startPercents[g.GroupKey] = g.UsagePercent
		}
	}

	entries := make([]CrossQuotaEntry, 0, len(grouped))
	for _, g := range grouped {
		start := startPercents[g.GroupKey]
		entries = append(entries, CrossQuotaEntry{
			Name:         g.GroupKey,
			Value:        g.UsagePercent,
			Limit:        100,
			Percent:      g.UsagePercent,
			StartPercent: start,
			Delta:        g.UsagePercent - start,
		})
	}

	return entries, nil
}

func isAntigravityQuotaGroup(groupKey string) bool {
	switch groupKey {
	case api.AntigravityQuotaGroupClaudeGPT, api.AntigravityQuotaGroupGeminiPro, api.AntigravityQuotaGroupGeminiFlash:
		return true
	default:
		return false
	}
}
