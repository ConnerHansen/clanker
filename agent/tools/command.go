package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CommandResult is the structured result of running a shell command via the
// RunCommand tool.
type CommandResult struct {
	Status     string `json:"status"` // "success" (exit 0) or "error" (non-zero exit / run failure)
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// commandDisplay renders a resolved argv spec as a single shell-like string
// for display (permission prompts, results). Unquoted and informational:
// spaces inside arguments are not escaped; the prompt shows what would be typed.
func commandDisplay(command string, params []string) string {
	if len(params) == 0 {
		return command
	}
	return command + " " + strings.Join(params, " ")
}

// RunCommandArgs is the argv form of RunCommand: it executes command directly
// (exec, no /bin/sh) with the given arguments and captures its output. It is
// intended for configured tool run blocks ({command, params[]}); dir semantics
// and timeout behavior match RunCommand.
func RunCommandArgs(command string, params []string, dir string, timeout time.Duration) *CommandResult {
	if command == "" {
		return &CommandResult{Status: "error", Command: command, Error: "command is required"}
	}

	if timeout <= 0 {
		timeout = CommandTimeout
	}

	workDir := dir
	if workDir == "" || workDir == "." {
		workDir = "."
	}

	if !filepath.IsAbs(workDir) {
		if wd, err := os.Getwd(); err == nil {
			workDir = filepath.Join(wd, workDir)
		}
	}
	if _, err := os.Stat(workDir); err != nil {
		return &CommandResult{Status: "error", Command: command, Error: fmt.Sprintf("invalid working directory %q: %v", dir, err)}
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, params...)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(start)

	outStr := stdout.String()
	errStr := stderr.String()
	truncated := false
	if len(outStr) > MaxCommandOutput {
		outStr = outStr[:MaxCommandOutput]
		truncated = true
	}
	if len(errStr) > MaxCommandOutput {
		errStr = errStr[:MaxCommandOutput]
		truncated = true
	}

	display := commandDisplay(command, params)

	res := &CommandResult{
		Command:    display,
		Stdout:     outStr,
		Stderr:     errStr,
		Truncated:  truncated,
		DurationMs: duration.Milliseconds(),
	}

	if err != nil {
		// A deadline SIGKILL surfaces as an *exec.ExitError too, so the timeout
		// check must precede the exit-error classification to report distinctly.
		if ctx.Err() == context.DeadlineExceeded {
			res.Status = "error"
			res.ExitCode = -1
			res.Error = fmt.Sprintf("command timed out after %s", timeout)
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			// The command ran but exited non-zero (e.g. a failing build).
			res.Status = "error"
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.Status = "error"
			res.ExitCode = -1
			res.Error = fmt.Sprintf("failed to execute command: %v", err)
		}
		return res
	}

	// Exit code 0: successful build / clean run.
	res.Status = "success"
	res.ExitCode = 0
	return res
}
