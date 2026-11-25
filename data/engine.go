package data

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	_ "github.com/glebarez/sqlite"
)

var db *sql.DB

func openDB(path string) (*sql.DB, error) {
	var err error
	db, err = sql.Open("sqlite", path+"?_foreign_keys=on")
	if err != nil {
		return nil, err
	}

	// SQLite PRAGMAs MUST be run per connection
	pragmas := []string{
		"PRAGMA foreign_keys = ON;",
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA busy_timeout = 5000;", // Handle contention gracefully
	}

	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to apply PRAGMA %s: %w", p, err)
		}
	}

	// Optional: ensure consistent datetime format
	if _, err := db.Exec("PRAGMA journal_size_limit = 4096;"); err != nil {
		log.Printf("warning: PRAGMA application failed: %v", err)
	}

	db.SetMaxOpenConns(1) // SQLite recommends single writer for stability
	return db, nil
}

// InitDB opens the application database at its canonical path and ensures all
// tables exist.
func InitDB() error {
	if err := os.MkdirAll(".clanker", 0o755); err != nil {
		return err
	}

	d, err := openDB(".clanker/db")
	if err != nil {
		return err
	}
	return createTables(d)
}

func createTables(d *sql.DB) error {
	createTablesSQL := `
CREATE TABLE IF NOT EXISTS active_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    inserted_at DATETIME NOT NULL,
    input TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS historical_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    inserted_at DATETIME NOT NULL,
    input TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    inserted_at DATETIME NOT NULL,
    name TEXT,
    prompt_context TEXT DEFAULT '',
    context_id INTEGER NOT NULL DEFAULT 0,
    context_size INTEGER NOT NULL DEFAULT 262144,
    reasoning_effort TEXT DEFAULT '',
    parent_session_id INTEGER
);
CREATE TABLE IF NOT EXISTS messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL,
    inserted_at DATETIME NOT NULL,
    contents TEXT NOT NULL,
    model TEXT NOT NULL,
    source TEXT NOT NULL,
    tool_calls TEXT,
    FOREIGN KEY (session_id) REFERENCES sessions(id)
        ON DELETE CASCADE
        ON UPDATE CASCADE
);
CREATE TABLE IF NOT EXISTS summaries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id INTEGER NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    last_summarized_message_id INTEGER NOT NULL DEFAULT 0,
    summarized_at DATETIME,
    summarized_by TEXT NOT NULL DEFAULT '',
    UNIQUE(session_id),
    FOREIGN KEY (session_id) REFERENCES sessions(id)
        ON DELETE CASCADE
        ON UPDATE CASCADE
);
CREATE TABLE IF NOT EXISTS contexts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    thinking INTEGER NOT NULL DEFAULT 0,
    inserted_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS prompts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    content TEXT NOT NULL,
    inserted_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS context_prompts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    context_id INTEGER NOT NULL,
    prompt_id INTEGER NOT NULL,
    prompt_order INTEGER NOT NULL,
    UNIQUE(context_id, prompt_id),
    FOREIGN KEY (prompt_id) REFERENCES prompts(id)
        ON DELETE CASCADE
        ON UPDATE CASCADE
);
`

	_, err := d.Exec(createTablesSQL)
	if err != nil {
		return err
	}

	// MigratePromptContext2(d)
	// MigratePromptName(d)
	if err := MigrateToolCalls(d); err != nil {
		return err
	}
	if err := MigrateSessionContextSize(d); err != nil {
		return err
	}
	if err := MigrateMessageSummary(d); err != nil {
		return err
	}
	if err := MigrateSessionParent(d); err != nil {
		return err
	}
	if err := MigrateSessionReasoning(d); err != nil {
		return err
	}
	return nil
}

// OpenTestDB points the package database at a throwaway file and ensures all
// tables exist, so tests can exercise real persistence without touching the
// application DB. Returns the previous handle (nil if none) for restoration.
func OpenTestDB(path string) (*sql.DB, error) {
	prev := db
	d, err := openDB(path)
	if err != nil {
		return prev, err
	}
	if err := createTables(d); err != nil {
		return prev, err
	}
	return prev, nil
}

// CloseTestDB closes and clears the package database handle (used by tests).
func CloseTestDB() error {
	if db == nil {
		return nil
	}
	err := db.Close()
	db = nil
	return err
}

// MigrateToolCalls adds the tool_calls column to the messages table if it doesn't exist.
func MigrateToolCalls(d *sql.DB) error {
	_, err := d.Exec("ALTER TABLE messages ADD COLUMN tool_calls TEXT")
	if err != nil {
		// Silently succeed if the column already exists
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil
		}
		return fmt.Errorf("migration failed: %w", err)
	}
	return nil
}

// MigrateSessionContextSize adds the context_size column to the sessions table
// if it doesn't exist (existing rows inherit the 256k default).
func MigrateSessionContextSize(d *sql.DB) error {
	_, err := d.Exec("ALTER TABLE sessions ADD COLUMN context_size INTEGER NOT NULL DEFAULT 262144")
	if err != nil {
		// Silently succeed if the column already exists
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil
		}
		return fmt.Errorf("migration failed: %w", err)
	}
	return nil
}

// MigrateSessionParent adds the parent_session_id column to the sessions table
// if it doesn't exist. It links a subagent's session to the conversation that
// spawned it (NULL for top-level sessions). NULL, not NOT NULL DEFAULT 0, so an
// absent parent is unambiguous.
func MigrateSessionParent(d *sql.DB) error {
	_, err := d.Exec("ALTER TABLE sessions ADD COLUMN parent_session_id INTEGER")
	if err != nil {
		// Silently succeed if the column already exists.
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil
		}
		return fmt.Errorf("migration failed: %w", err)
	}
	return nil
}

// MigrateSessionReasoning adds the reasoning_effort column to the sessions table
// if it doesn't exist. Holds the session's chosen reasoning level; empty means
// "inherit the model's default" (no override recorded).
func MigrateSessionReasoning(d *sql.DB) error {
	_, err := d.Exec("ALTER TABLE sessions ADD COLUMN reasoning_effort TEXT DEFAULT ''")
	if err != nil {
		// Silently succeed if the column already exists.
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil
		}
		return fmt.Errorf("migration failed: %w", err)
	}
	return nil
}

// MigrateMessageSummary adds the summary column to the messages table if it
// doesn't exist. Holds the tool-defined one-line self-summary for tool-call
// rows (populated at save time); NULL for user/assistant rows.
func MigrateMessageSummary(d *sql.DB) error {
	_, err := d.Exec("ALTER TABLE messages ADD COLUMN summary TEXT")
	if err != nil {
		// Silently succeed if the column already exists
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil
		}
		return fmt.Errorf("migration failed: %w", err)
	}
	return nil
}

func MigratePromptContext(db *sql.DB) error {
	_, err := db.Exec("ALTER TABLE sessions ADD COLUMN prompt_context TEXT DEFAULT ''")
	if err != nil {
		// Silently succeed if the column already exists
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil
		}
		return fmt.Errorf("migration failed: %w", err)
	}
	return nil
}

func MigratePromptContext2(db *sql.DB) error {
	_, err := db.Exec("ALTER TABLE sessions ADD COLUMN context_id INTEGER NOT NULL DEFAULT 0")
	if err != nil {
		// Silently succeed if the column already exists
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil
		}
		return fmt.Errorf("migration failed: %w", err)
	}
	return nil
}

func MigratePromptName(db *sql.DB) error {
	_, err := db.Exec("ALTER TABLE prompts ADD COLUMN name TEXT NOT NULL DEFAULT ''")
	if err != nil {
		// Silently succeed if the column already exists
		if strings.Contains(err.Error(), "duplicate column name") {
			return nil
		}
		return fmt.Errorf("migration failed: %w", err)
	}
	return nil
}

func StartHTTPServer(port int) {
	defer db.Close()

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	http.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var req struct {
				Input string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			id, err := InsertActiveJob(req.Input)
			if err != nil {
				http.Error(w, "DB error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]int64{"id": id})
		case http.MethodGet:
			jobs, err := ListActiveJobs()
			if err != nil {
				http.Error(w, "DB error: "+err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(jobs)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/jobs/historical", func(w http.ResponseWriter, r *http.Request) {
		jobs, err := ListHistoricalJobs()
		if err != nil {
			http.Error(w, "DB error: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jobs)
	})

	url := fmt.Sprintf(":%d", port)
	log.Printf("Starting HTTP server on %s", url)
	log.Fatal(http.ListenAndServe(url, nil))
}
