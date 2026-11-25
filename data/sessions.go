package data

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// SummaryStatus describes how current a session's summary is, for list-level
// display: "none" (never summarized), "stale" (summarized but new messages
// since), or "fresh" (summary covers the latest message). Empty when the
// session has no messages.
const (
	SummaryStatusNone  = "none"
	SummaryStatusStale = "stale"
	SummaryStatusFresh = "fresh"
)

type Session struct {
	ID            int64     `db:"id" json:"id"`
	InsertedAt    time.Time `db:"inserted_at" json:"inserted_at"`
	Name          string    `db:"name" json:"name"`
	PromptContext *string   `db:"prompt_context" json:"prompt_context"`
	ContextID     int64     `db:"context_id" json:"context_id"`
	// ParentSessionID links a subagent's session to the conversation that
	// spawned it (0 for top-level sessions). Set at creation time so the audit
	// trail survives; the summary sweep and list views can use it later.
	ParentSessionID int64 `db:"parent_session_id" json:"parent_session_id"`
	// ContextSize is the model context window (in tokens) used for this
	// session's streaming payloads. Defaults to DefaultContextSize (256k).
	ContextSize int `db:"context_size" json:"context_size"`
	// ReasoningEffort is the session's chosen reasoning level; empty means
	// "inherit the current model's default" (no override recorded). Set only
	// when it differs from that model's default.
	ReasoningEffort string `db:"reasoning_effort" json:"reasoning_effort"`
	// SummaryStatus and SummarizedAt reflect the summaries table; they stay
	// empty/zero when the session has no messages or no summary yet.
	SummaryStatus string     `json:"summary_status"`
	SummarizedAt  *time.Time `json:"summarized_at,omitempty"`
	// CurrentPlan holds the agreed-upon execution plan for this session.
	// Intentionally in-memory only (no DB column): a plan lives at most until
	// the next cleanly completed tool loop, which flushes it. This keeps
	// stealth and persisted sessions behaving identically.
	CurrentPlan string `json:"current_plan"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type Message struct {
	ID         int64      `db:"id"`
	SessionID  int64      `db:"session_id"`
	InsertedAt time.Time  `db:"inserted_at"`
	Contents   string     `db:"contents"`
	Model      string     `db:"model"`
	Source     string     `db:"source"`
	ToolCalls  []ToolCall `db:"tool_calls"` // Nullable field
	// Summary is the tool-defined one-line self-summary for tool-call rows
	// (e.g. "Wrote 1234 bytes to ./agent/summary_engine.go"). NULL for
	// user/assistant rows; populated at save time so the session summarizer
	// and UI never have to re-parse full tool results.
	Summary *string `db:"summary"`
}

// DefaultContextSize is the token window applied to model payloads when a
// session has no explicit context size set.
const DefaultContextSize = 262144 // 256k

// ContextSizeOptions are the selectable context window sizes, smallest first.
var ContextSizeOptions = []int{
	16384,    // 16k
	32768,    // 32k
	65536,    // 64k
	131072,   // 128k
	DefaultContextSize,
}

// NormalizeContextSize maps a raw token count onto the supported context size
// options (nearest option wins). Unset values (<= 0, e.g. rows predating the
// column) fall back to the default.
func NormalizeContextSize(tokens int) int {
	if tokens <= 0 {
		return DefaultContextSize
	}

	best := ContextSizeOptions[0]
	bestDist := 0
	for i, opt := range ContextSizeOptions {
		dist := tokens - opt
		if dist < 0 {
			dist = -dist
		}
		if i == 0 || dist < bestDist {
			best = opt
			bestDist = dist
		}
	}
	return best
}

// FormatContextSize renders a token count as a human-friendly "16k"-style label.
func FormatContextSize(tokens int) string {
	if tokens%1024 == 0 {
		return fmt.Sprintf("%dk", tokens/1024)
	}
	return fmt.Sprintf("%d tokens", tokens)
}

// ListSessions returns all sessions ordered by their most recent message time,
// annotated with summary staleness (none/stale/fresh) and the summary
// timestamp for UI display.
func ListSessions() ([]Session, error) {
	rows, err := db.Query(`
		SELECT s.id, s.inserted_at, s.name, COALESCE(s.prompt_context, '') as prompt_context,
			   s.context_id, s.context_size, s.reasoning_effort,
			   MAX(m.id) as last_message_id,
			   sm.last_summarized_message_id as sm_last_id, sm.summarized_at as summarized_at
		FROM sessions s
		LEFT JOIN messages m ON s.id = m.session_id
		LEFT JOIN summaries sm ON sm.session_id = s.id
		GROUP BY s.id, s.inserted_at, s.name
		ORDER BY CASE WHEN MAX(m.inserted_at) IS NULL THEN 1 ELSE 0 END, MAX(m.inserted_at) DESC
	`)
	if err != nil {
		log.Println("Error getting sessions", err)
		return nil, err
	}
	defer rows.Close()

	var sessions []Session

	for rows.Next() {
		var s Session
		var lastMessageID sql.NullInt64 // Invalid when the session has no messages
		var smLastID sql.NullInt64
		var summarizedAt sql.NullTime
		var promptCtx sql.NullString

		if err := rows.Scan(&s.ID, &s.InsertedAt, &s.Name, &promptCtx, &s.ContextID, &s.ContextSize,
			&s.ReasoningEffort, &lastMessageID, &smLastID, &summarizedAt); err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}

		s.PromptContext = &promptCtx.String
		// Rows predating the context_size column carry 0; normalize so
		// callers never see an unset value.
		s.ContextSize = NormalizeContextSize(s.ContextSize)

		// Annotate summary staleness for list display.
		if !lastMessageID.Valid {
			continue // No messages yet: nothing to summarize, status stays empty.
		}
		switch {
		case !smLastID.Valid:
			s.SummaryStatus = SummaryStatusNone
		case smLastID.Int64 >= lastMessageID.Int64 && summarizedAt.Valid:
			s.SummaryStatus = SummaryStatusFresh
			s.SummarizedAt = &summarizedAt.Time
		default:
			s.SummaryStatus = SummaryStatusStale
		}

		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// LoadSession returns all messages for a given session, ordered by insertion time.
func LoadSession(session *Session) ([]Message, error) {
	return LoadMessagesAfter(session.ID, 0)
}

// LoadMessagesAfter returns the session's messages with id > afterMsgID,
// ordered by insertion time. The session summarizer uses it to read only the
// activity since the previous summary; callers pass 0 for a full reload.
func LoadMessagesAfter(sessionID int64, afterMsgID int64) ([]Message, error) {
	rows, err := db.Query(`
        SELECT id, session_id, inserted_at, contents, model, source, tool_calls, summary
        FROM messages
        WHERE session_id = ? AND id > ?
        ORDER BY id ASC
    `, sessionID, afterMsgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var m Message
		var toolCallsJSON sql.NullString // Use sql.NullString to handle DB NULLs
		var summary sql.NullString

		// Scan the tool_calls column (stored as JSON text)
		if err := rows.Scan(&m.ID, &m.SessionID, &m.InsertedAt, &m.Contents, &m.Model, &m.Source, &toolCallsJSON, &summary); err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}

		// Unmarshal the JSON string back into the ToolCalls slice
		if toolCallsJSON.Valid {
			if err := json.Unmarshal([]byte(toolCallsJSON.String), &m.ToolCalls); err != nil {
				return nil, fmt.Errorf("failed to unmarshal tool_calls: %w", err)
			}
		}

		if summary.Valid && summary.String != "" {
			s := summary.String
			m.Summary = &s
		}

		messages = append(messages, m)
	}

	return messages, rows.Err()
}

// CompactionSeedPrefix frames the single user message that re-seeds a compacted
// runtime's history. It is persisted with the seed row, so session reloads cut
// at it to rebuild the model payload without schema flags (see SetSession and
// CompactionCutIndex). The agent package aliases it for its seeding code.
const CompactionSeedPrefix = "The previous context for this session was compacted. Continue from the following summary:\n\n"

// CompactionCutIndex returns the index of the LATEST user row starting with
// CompactionSeedPrefix — the re-seeded summary that leads every post-compaction
// model payload (see agent.AgentRuntime.RegisterCompaction). Returns -1 when the
// session has no compaction: the full history is used. Only the RUNTIME's model
// payload assembly cuts at it (agent SetSession); display code renders the full
// persisted history, with pre-seed rows as display-only context.
func CompactionCutIndex(messages []Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Source != "user" {
			continue
		}
		if strings.HasPrefix(messages[i].Contents, CompactionSeedPrefix) {
			return i
		}
	}
	return -1
}

func UpdateSession(s *Session) (*Session, error) {
	// Prevent accidental UPDATEs that affect all rows
	if s.ID == 0 {
		return nil, errors.New("cannot update session: ID is required")
	}

	log.Println("Updating session", s)
	result, err := db.Exec(`
		UPDATE sessions 
		SET inserted_at = ?, name = ?, prompt_context = ? 
		WHERE id = ?
	`, s.InsertedAt, s.Name, s.PromptContext, s.ID)

	if err != nil {
		return nil, fmt.Errorf("failed to update session %d: %w", s.ID, err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		return nil, errors.New("session not found or unchanged")
	}

	return s, nil
}

func DeleteSession(session *Session) error {
	log.Println("Deleteydo", session.ID)
	// Guard against invalid input
	if session == nil || session.ID == 0 {
		return fmt.Errorf("invalid session: cannot delete with zero ID")
	}

	// Parameterized delete query
	_, err := db.Exec("DELETE FROM sessions WHERE id = ?", session.ID)
	if err != nil {
		return fmt.Errorf("failed to delete session %d: %w", session.ID, err)
	}

	return nil
}

// SetContextSize persists the context window size (in tokens) for a session.
// Unknown token counts are rejected so only supported sizes ever reach models.
func SetContextSize(sessionID int64, tokens int) error {
	known := false
	for _, opt := range ContextSizeOptions {
		if opt == tokens {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("unsupported context size %d; expected one of: 16384, 32768, 65536, 131072, 262144", tokens)
	}

	result, err := db.Exec(`UPDATE sessions SET context_size = ? WHERE id = ?`, tokens, sessionID)
	if err != nil {
		return fmt.Errorf("failed to set context size for session %d: %w", sessionID, err)
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("session %d not found", sessionID)
	}
	return nil
}

// SetReasoningEffort persists the chosen reasoning level for a session. The
// value is stored verbatim ("" clears it back to model-default inheritance).
func SetReasoningEffort(sessionID int64, level string) error {
	result, err := db.Exec(`UPDATE sessions SET reasoning_effort = ? WHERE id = ?`, level, sessionID)
	if err != nil {
		return fmt.Errorf("failed to set reasoning effort for session %d: %w", sessionID, err)
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("session %d not found", sessionID)
	}
	return nil
}

// NewSession creates a new session in the database and returns it.
func NewSession(name string) (*Session, error) {
	now := time.Now()
	res, err := db.Exec(`
                INSERT INTO sessions (inserted_at, name)
                VALUES (?, ?)
        `, now, name)
	if err != nil {
		return &Session{}, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return &Session{}, err
	}

	return &Session{
		ID:          id,
		InsertedAt:  now,
		Name:        name,
		ContextSize: DefaultContextSize,
	}, nil
}

// SetSessionParent records which session spawned a subagent's session (for the
// audit trail). parentID of 0 means top-level/no parent. It is a targeted UPDATE
// so it cannot disturb other columns, and it reports whether the row existed.
func SetSessionParent(sessionID int64, parentID int64) error {
	if sessionID <= 0 {
		return fmt.Errorf("invalid session id %d", sessionID)
	}

	result, err := db.Exec(`UPDATE sessions SET parent_session_id = ? WHERE id = ?`, parentID, sessionID)
	if err != nil {
		return fmt.Errorf("failed to set parent for session %d: %w", sessionID, err)
	}

	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("session %d not found", sessionID)
	}
	return nil
}

// SaveMessage inserts a new message linked to the provided session.
func SaveMessage(session *Session, msg Message) (Message, error) {
	now := time.Now()
	newMsg := Message{
		SessionID:  session.ID,
		InsertedAt: now,
		Contents:   msg.Contents,
		Model:      msg.Model,
		Source:     msg.Source,
		ToolCalls:  msg.ToolCalls,
		Summary:    msg.Summary,
	}

	// Marshal ToolCalls to JSON string for storage
	var toolCallsVal sql.NullString
	if len(newMsg.ToolCalls) > 0 {
		jsonBytes, err := json.Marshal(newMsg.ToolCalls)
		if err != nil {
			return Message{}, fmt.Errorf("failed to marshal tool calls: %w", err)
		}
		toolCallsVal = sql.NullString{String: string(jsonBytes), Valid: true}
	} else {
		// Explicitly set as NULL if empty/nil
		toolCallsVal = sql.NullString{Valid: false}
	}

	var summaryVal sql.NullString
	if newMsg.Summary != nil && *newMsg.Summary != "" {
		summaryVal = sql.NullString{String: *newMsg.Summary, Valid: true}
	} else {
		summaryVal = sql.NullString{Valid: false}
	}

	res, err := db.Exec(`
                INSERT INTO messages (session_id, inserted_at, contents, model, source, tool_calls, summary)
                VALUES (?, ?, ?, ?, ?, ?, ?)
        `, newMsg.SessionID, newMsg.InsertedAt, newMsg.Contents, newMsg.Model, newMsg.Source, toolCallsVal, summaryVal)
	if err != nil {
		log.Println(err)
		return Message{}, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		log.Println(err)
		return Message{}, err
	}
	newMsg.ID = id

	return newMsg, nil
}
