package data

import (
	"os"
	"os/exec"
	"strings"
)

// WorkingDirectory returns the process's current working directory, or "" when
// it cannot be determined. The statusbar and system prompt use it so both the
// user and the model know where on the system the agent is running.
func WorkingDirectory() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

// GitBranch returns the current git branch name (e.g. "main"), or "" when the
// command fails — not inside a repository, git missing, detached state with no
// shortname, etc. Callers hide the value on empty: the statusbar drops its
// pill and the system prompt omits the line.
func GitBranch() string {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	// "HEAD" means detached: no branch name to show.
	if branch == "HEAD" {
		return ""
	}
	return branch
}
