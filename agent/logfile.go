package agent

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// sessionLogWriter is a thread-safe io.Writer that tees log output to:
//   - os.Stderr (when enabled — desktop/Wails mode)
//   - an optional per-session file (.clanker/logs/{session_id}.log)
//   - an in-memory buffer (unbounded, process lifetime) for the TUI /log view
//
// It is set as the global log output via log.SetOutput at startup. The
// session file is opened lazily when InferSession establishes a real session.
// Stderr can be disabled (TUI mode) so log lines never corrupt the terminal
// rendering; there the memory buffer and the file are the record.
type sessionLogWriter struct {
	mu     sync.Mutex
	stderr bool     // write to os.Stderr (desktop); off for the TUI
	file   *os.File // per-session log (nil until a session is established)
	buf    bytes.Buffer
}

func (w *sessionLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.stderr {
		os.Stderr.Write(p)
	}
	if w.file != nil {
		w.file.Write(p)
	}
	w.buf.Write(p)
	w.mu.Unlock()
	return len(p), nil
}

// SetStderr toggles console (stderr) mirroring. On by default for desktop;
// the TUI disables it so log lines do not clobber the terminal rendering.
func (w *sessionLogWriter) SetStderr(on bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stderr = on
}

// OpenSessionFile opens (or creates) the per-session log file and switches
// the writer to tee into it. Safe to call multiple times: a new session
// replaces the previous file handle. The directory is created if missing.
func (w *sessionLogWriter) OpenSessionFile(sessionID int64) {
	dir := sessionLogDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("log: cannot create %s: %v", dir, err)
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.log", sessionID))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("log: cannot open %s: %v", path, err)
		return
	}

	w.mu.Lock()
	if w.file != nil {
		w.file.Close()
	}
	w.file = f
	w.mu.Unlock()

	// Logged OUTSIDE the lock: log writes flow through this same writer, so a
	// re-entrant Write while holding mu would deadlock.
	log.Printf("Log file: %s", path)
}

// Contents returns a snapshot copy of everything logged since process start.
func (w *sessionLogWriter) Contents() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// CloseSessionFile closes the current session log file (e.g. at shutdown).
func (w *sessionLogWriter) CloseSessionFile() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
}

var (
	// sessionLogDir is the per-session log directory, relative to CWD.
	// Tests repoint it at a temp dir (TestMain) so runs never write into the
	// source tree.
	sessionLogDir = filepath.Join(".clanker", "logs")

	// sessionLog is created with stderr ON (desktop default); the TUI path
	// disables it. init() installs it as the global log output.
	sessionLog = &sessionLogWriter{stderr: true}
)

// SessionLog returns the package-level tee writer so other packages (main) can
// switch session files or toggle console mirroring without importing internals.
func SessionLog() *sessionLogWriter {
	return sessionLog
}

func init() {
	log.SetOutput(sessionLog)
}
