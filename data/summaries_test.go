package data

import (
	"testing"
	"time"
)

// seedSessionMessage inserts one session plus a single message at an explicit
// timestamp and returns both IDs. Direct SQL lets the recency filter be
// exercised with precise ages regardless of wall-clock time.
func seedSessionMessage(t *testing.T, name string, when time.Time) (sessionID, msgID int64) {
	t.Helper()
	res, err := db.Exec("INSERT INTO sessions (inserted_at, name) VALUES (?, ?)", when.Add(-time.Hour), name)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	sessionID, _ = res.LastInsertId()

	mres, err := db.Exec("INSERT INTO messages (session_id, inserted_at, contents, model, source) VALUES (?, ?, ?, ?, ?)",
		sessionID, when, "hi", "test-model", "user")
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}
	msgID, _ = mres.LastInsertId()
	return sessionID, msgID
}

func TestFindStaleSessionsRecencyFilter(t *testing.T) {
	tmpDB := t.TempDir() + "/test.db"
	if _, err := OpenTestDB(tmpDB); err != nil {
		t.Fatalf("open test db: %v", err)
	}
	defer CloseTestDB()

	now := time.Now()
	window := 10 * time.Minute

	// A: last message 30m ago, no summary -> stale.
	a, _ := seedSessionMessage(t, "idle-30m", now.Add(-30*time.Minute))
	// B: last message 5m ago, no summary -> NOT stale (still active).
	b, _ := seedSessionMessage(t, "active-5m", now.Add(-5*time.Minute))
	// C: last message 2h ago but summary already covers it -> NOT stale.
	c, cMsg := seedSessionMessage(t, "covered-2h", now.Add(-2*time.Hour))
	if err := UpsertSummary(c, "done", cMsg, "sum-model"); err != nil {
		t.Fatalf("upsert summary: %v", err)
	}
	// D: last message 1h ago, summary lags behind -> stale.
	d, dMsg := seedSessionMessage(t, "lagging-1h", now.Add(-time.Hour))
	if err := UpsertSummary(d, "stale", dMsg-1, "sum-model"); err != nil {
		t.Fatalf("upsert lagging summary: %v", err)
	}

	got, err := FindStaleSessions(window)
	if err != nil {
		t.Fatalf("FindStaleSessions: %v", err)
	}

	want := map[int64]bool{a: true, d: true} // B and C must be excluded
	seen := make(map[int64]bool, len(got))
	for _, st := range got {
		seen[st.Session.ID] = true
		if !want[st.Session.ID] {
			t.Errorf("session %d (%q) should not be stale", st.Session.ID, st.Session.Name)
		}
	}
	for id, shouldBeStale := range want {
		if shouldBeStale && !seen[id] {
			t.Errorf("session %d should be stale but was excluded", id)
		}
	}

	// Zero window disables the recency check: B becomes stale too.
	all, err := FindStaleSessions(0)
	if err != nil {
		t.Fatalf("FindStaleSessions(0): %v", err)
	}
	gotAll := make(map[int64]bool, len(all))
	for _, st := range all {
		gotAll[st.Session.ID] = true
	}
	if !gotAll[b] {
		t.Errorf("with a zero window the active session %d should be stale", b)
	}
}
