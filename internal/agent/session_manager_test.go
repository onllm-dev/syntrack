package agent

import (
	"log/slog"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/internal/store"
)

func newTestSessionManager(t *testing.T, idleTimeout time.Duration) (*SessionManager, *store.Store) {
	t.Helper()
	str, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	t.Cleanup(func() { str.Close() })

	logger := slog.Default()
	sm := NewSessionManager(str, "synthetic", idleTimeout, logger)
	return sm, str
}

func TestSessionManager_NoSessionWithoutUsageChange(t *testing.T) {
	sm, str := newTestSessionManager(t, 10*time.Second)

	// Report same values twice — no change, no session should be created
	sm.ReportPoll([]float64{100, 50, 500})
	sm.ReportPoll([]float64{100, 50, 500})

	sessions, _ := str.QuerySessionHistory("synthetic")
	if len(sessions) != 0 {
		t.Errorf("Expected 0 sessions when usage never changes, got %d", len(sessions))
	}
}

func TestSessionManager_CreatesSessionOnUsageChange(t *testing.T) {
	sm, str := newTestSessionManager(t, 10*time.Second)

	// First poll: baseline
	sm.ReportPoll([]float64{100, 50, 500})
	// Second poll: values changed → should create session
	sm.ReportPoll([]float64{110, 50, 500})

	sessions, _ := str.QuerySessionHistory("synthetic")
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session after usage change, got %d", len(sessions))
	}
	if sessions[0].EndedAt != nil {
		t.Error("Session should still be open (no idle timeout yet)")
	}
}

func TestSessionManager_SessionStaysOpenDuringActivity(t *testing.T) {
	sm, str := newTestSessionManager(t, 10*time.Second)

	sm.ReportPoll([]float64{100, 50, 500})
	sm.ReportPoll([]float64{110, 50, 500}) // change → session starts
	sm.ReportPoll([]float64{120, 50, 500}) // change → session stays open

	sessions, _ := str.QuerySessionHistory("synthetic")
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}
	if sessions[0].EndedAt != nil {
		t.Error("Session should still be open during active usage")
	}
}

func TestSessionManager_ClosesSessionAfterIdleTimeout(t *testing.T) {
	sm, str := newTestSessionManager(t, 100*time.Millisecond)

	sm.ReportPoll([]float64{100, 50, 500})
	sm.ReportPoll([]float64{110, 50, 500}) // change → session starts

	// Wait for idle timeout to elapse
	time.Sleep(150 * time.Millisecond)

	// Report same values — should close session
	sm.ReportPoll([]float64{110, 50, 500})

	sessions, _ := str.QuerySessionHistory("synthetic")
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}
	if sessions[0].EndedAt == nil {
		t.Error("Session should be closed after idle timeout")
	}
}

func TestSessionManager_NewSessionAfterIdleGap(t *testing.T) {
	sm, str := newTestSessionManager(t, 100*time.Millisecond)

	sm.ReportPoll([]float64{100, 50, 500})
	sm.ReportPoll([]float64{110, 50, 500}) // session 1 starts

	time.Sleep(150 * time.Millisecond)
	sm.ReportPoll([]float64{110, 50, 500}) // idle timeout → close session 1

	// New usage change → session 2
	sm.ReportPoll([]float64{120, 50, 500})

	sessions, _ := str.QuerySessionHistory("synthetic")
	if len(sessions) != 2 {
		t.Fatalf("Expected 2 sessions, got %d", len(sessions))
	}

	// Most recent session should be open
	if sessions[0].EndedAt != nil {
		t.Error("Latest session should be open")
	}
	// First session should be closed
	if sessions[1].EndedAt == nil {
		t.Error("First session should be closed")
	}
}

func TestSessionManager_CloseClosesActiveSession(t *testing.T) {
	sm, str := newTestSessionManager(t, 10*time.Second)

	sm.ReportPoll([]float64{100, 50, 500})
	sm.ReportPoll([]float64{110, 50, 500}) // session starts

	sm.Close() // agent shutdown

	sessions, _ := str.QuerySessionHistory("synthetic")
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}
	if sessions[0].EndedAt == nil {
		t.Error("Session should be closed after Close()")
	}
}

func TestSessionManager_CloseNoopWithoutActiveSession(t *testing.T) {
	sm, _ := newTestSessionManager(t, 10*time.Second)

	// Close without any session — should not panic
	sm.Close()
}

func TestSessionManager_FirstPollNeverCreatesSession(t *testing.T) {
	sm, str := newTestSessionManager(t, 10*time.Second)

	// Only one poll — establishes baseline, no session
	sm.ReportPoll([]float64{100, 50, 500})

	sessions, _ := str.QuerySessionHistory("synthetic")
	if len(sessions) != 0 {
		t.Errorf("Expected 0 sessions after first poll, got %d", len(sessions))
	}
}

func TestSessionManager_SliceLengthChange_IsUsageChange(t *testing.T) {
	sm, str := newTestSessionManager(t, 10*time.Second)

	sm.ReportPoll([]float64{10, 20})
	sm.ReportPoll([]float64{10, 20, 30}) // length changed → usage change

	sessions, _ := str.QuerySessionHistory("synthetic")
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session when slice length changes, got %d", len(sessions))
	}
}

func TestSessionManager_UpdatesSnapshotCount(t *testing.T) {
	sm, str := newTestSessionManager(t, 10*time.Second)

	sm.ReportPoll([]float64{100, 50, 500})
	sm.ReportPoll([]float64{110, 50, 500}) // session starts, snapshot count = 1
	sm.ReportPoll([]float64{120, 50, 500}) // snapshot count = 2
	sm.ReportPoll([]float64{130, 50, 500}) // snapshot count = 3

	sessions, _ := str.QuerySessionHistory("synthetic")
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}
	if sessions[0].SnapshotCount < 3 {
		t.Errorf("Expected snapshot count >= 3, got %d", sessions[0].SnapshotCount)
	}
}

func TestSessionManager_UpdatesMaxRequests(t *testing.T) {
	sm, str := newTestSessionManager(t, 10*time.Second)

	sm.ReportPoll([]float64{100, 50, 500})
	sm.ReportPoll([]float64{200, 60, 600})  // session starts
	sm.ReportPoll([]float64{150, 100, 700}) // sub goes down, search goes up

	sessions, _ := str.QuerySessionHistory("synthetic")
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}

	// Max should be the peak values
	s := sessions[0]
	if s.MaxSubRequests != 200.0 {
		t.Errorf("Expected max_sub_requests = 200, got %f", s.MaxSubRequests)
	}
	if s.MaxSearchRequests != 100.0 {
		t.Errorf("Expected max_search_requests = 100, got %f", s.MaxSearchRequests)
	}
	if s.MaxToolRequests != 700.0 {
		t.Errorf("Expected max_tool_requests = 700, got %f", s.MaxToolRequests)
	}
}

func TestSessionManager_RecordsStartValues(t *testing.T) {
	sm, str := newTestSessionManager(t, 10*time.Second)

	// First poll: baseline (100, 50, 500)
	sm.ReportPoll([]float64{100, 50, 500})
	// Second poll: values changed → session starts with start values = baseline
	sm.ReportPoll([]float64{200, 60, 600})

	sessions, _ := str.QuerySessionHistory("synthetic")
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}

	s := sessions[0]
	if s.StartSubRequests != 100.0 {
		t.Errorf("Expected start_sub_requests = 100, got %f", s.StartSubRequests)
	}
	if s.StartSearchRequests != 50.0 {
		t.Errorf("Expected start_search_requests = 50, got %f", s.StartSearchRequests)
	}
	if s.StartToolRequests != 500.0 {
		t.Errorf("Expected start_tool_requests = 500, got %f", s.StartToolRequests)
	}
}

func TestSessionManager_InactivePolls_IncrementSnapshotInWindow(t *testing.T) {
	sm, str := newTestSessionManager(t, 10*time.Second)

	sm.ReportPoll([]float64{100, 50, 500})
	sm.ReportPoll([]float64{110, 50, 500}) // session starts
	sm.ReportPoll([]float64{110, 50, 500}) // no change, within idle window → still increments

	sessions, _ := str.QuerySessionHistory("synthetic")
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}
	// The no-change poll should have incremented the snapshot count
	if sessions[0].SnapshotCount < 2 {
		t.Errorf("Expected snapshot count >= 2, got %d", sessions[0].SnapshotCount)
	}
}
