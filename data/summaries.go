package data

import (
	"database/sql"
	"fmt"
	"time"
)

// Summary is the persisted conversation summary for a session. One row per
// session (UNIQUE on session_id), produced by the summary engine. The content
// is free-form markdown prose; lastSummarizedMessageId marks how far the
// summary has been read so revived sessions can be incrementally re-summarized
// from where they left off, and summarizedAt/summarizedBy record when and with
// which model it was generated (used to decide regeneration when the
// summarizing algorithm or model changes).
type Summary struct {
	ID                      int64     `json:"id"`
	SessionID               int64     `json:"session_id"`
	Content                 string    `json:"content"`
	LastSummarizedMessageID int64     `json:"last_summarized_message_id"`
	SummarizedAt            time.Time `json:"summarized_at"`
	SummarizedBy            string    `json:"summarized_by"`
}

// StaleSession is a session whose summary is missing or no longer covers the
// full conversation (its lastSummarizedMessageId trails the newest message).
// PrevSummary*/PrevLast... carry the previous row's values so the engine can
// merge incrementally in one pass.
type StaleSession struct {
	Session                 Session
	LastMessageID           int64
	PrevSummaryContent      string
	PrevLastSummarizedMsgID int64
}

// GetSummary returns the stored summary for a session, or (nil, nil) when no
// row exists yet.
func GetSummary(sessionID int64) (*Summary, error) {
	var s Summary
	var summarizedAt sql.NullTime
	err := db.QueryRow(`
		SELECT id, session_id, content, last_summarized_message_id, summarized_at, summarized_by
		FROM summaries
		WHERE session_id = ?
	`, sessionID).Scan(&s.ID, &s.SessionID, &s.Content, &s.LastSummarizedMessageID, &summarizedAt, &s.SummarizedBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get summary for session %d: %w", sessionID, err)
	}
	s.SummarizedAt = summarizedAt.Time
	return &s, nil
}

// UpsertSummary inserts or updates the summary row for a session (one row per
// session keyed on session_id).
func UpsertSummary(sessionID int64, content string, lastMessageID int64, modelID string) error {
	now := time.Now()
	_, err := db.Exec(`
		INSERT INTO summaries (session_id, content, last_summarized_message_id, summarized_at, summarized_by)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			content = excluded.content,
			last_summarized_message_id = excluded.last_summarized_message_id,
			summarized_at = excluded.summarized_at,
			summarized_by = excluded.summarized_by
	`, sessionID, content, lastMessageID, now, modelID)
	if err != nil {
		return fmt.Errorf("failed to upsert summary for session %d: %w", sessionID, err)
	}
	return nil
}

// FindStaleSessions returns every session that has at least one message and
// either has no summary row yet or whose lastSummarizedMessageId does not match
// the session's newest message ID. A session only qualifies once it has been
// quiet: its newest message must be older than now - idleWindow (a 0 window
// disables the recency check). This keeps active conversations out of the
// summary engine even when their last persisted message is old, because a live
// run's in-flight output isn't written to the DB yet. Ordered
// oldest-activity first so long-idle sessions are summarized before busy ones
// in a single sweep.
func FindStaleSessions(idleWindow time.Duration) ([]StaleSession, error) {
	recency := ""
	if idleWindow > 0 {
		recency = " AND MAX(m.inserted_at) <= ?"
	}

	query := `
		SELECT s.id, s.inserted_at, s.name, COALESCE(s.prompt_context, '') as prompt_context,
			s.context_id, s.context_size,
			MAX(m.id) as last_message_id,
			COALESCE(sm.content, '') as prev_summary_content,
			COALESCE(sm.last_summarized_message_id, 0) as prev_last_summarized
		FROM sessions s
		LEFT JOIN messages m ON s.id = m.session_id
		LEFT JOIN summaries sm ON sm.session_id = s.id
		GROUP BY s.id, s.inserted_at, s.name, sm.content, sm.last_summarized_message_id
		HAVING MAX(m.id) IS NOT NULL
			AND (sm.id IS NULL OR COALESCE(sm.last_summarized_message_id, 0) < MAX(m.id))` + recency + `
		ORDER BY CASE WHEN MAX(m.inserted_at) IS NULL THEN 1 ELSE 0 END, MAX(m.inserted_at) ASC
	`

	var rows *sql.Rows
	var err error
	if recency != "" {
		// Stored timestamps are local time; compare against the local clock.
		rows, err = db.Query(query, time.Now().Add(-idleWindow))
	} else {
		rows, err = db.Query(query)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query stale sessions: %w", err)
	}
	defer rows.Close()

	var out []StaleSession
	for rows.Next() {
		var st StaleSession
		var promptCtx sql.NullString
		if err := rows.Scan(&st.Session.ID, &st.Session.InsertedAt, &st.Session.Name, &promptCtx,
			&st.Session.ContextID, &st.Session.ContextSize,
			&st.LastMessageID, &st.PrevSummaryContent, &st.PrevLastSummarizedMsgID); err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}
		st.Session.PromptContext = &promptCtx.String
		st.Session.ContextSize = NormalizeContextSize(st.Session.ContextSize)
		out = append(out, st)
	}
	return out, rows.Err()
}
