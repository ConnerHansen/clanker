package agent

import (
	"os"
	"testing"
)

// TestMain redirects per-session log writes to a temp dir so test runs never
// create .clanker/logs inside the source tree, and removes it on exit.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "clanker-agent-test-logs")
	if err != nil {
		os.Stderr.WriteString("TestMain: cannot create log temp dir: " + err.Error() + "\n")
		os.Exit(1)
	}
	prev := sessionLogDir
	sessionLogDir = dir
	code := m.Run()
	sessionLogDir = prev
	os.RemoveAll(dir)
	os.Exit(code)
}
