package repl

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type ShellSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	wg     sync.WaitGroup
}

func NewShellSession(ctx context.Context) (*ShellSession, error) {
	// Prefer bash/zsh with interactive flags
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}

	cmd := exec.Command(shell, "-i", "-l")
	// Strip dangerous env vars; keep PATH, HOME, TERM, etc.
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "EDITOR=nvim", "PAGER=less")

	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	s := &ShellSession{cmd: cmd, stdin: stdin, stdout: stdout}
	s.wg.Add(1)
	go s.streamOutput(ctx) // goroutine to read stdout & push to Wails events

	return s, nil
}

func (s *ShellSession) SendCommand(raw string) error {
	// The shell parses this natively. cd, exports, aliases, $VARS all work.
	_, err := s.stdin.Write([]byte(raw + "\n"))
	return err
}

func (s *ShellSession) streamOutput(ctx context.Context) {
	defer s.wg.Done()
	buf := make([]byte, 4096)
	for {
		n, err := s.stdout.Read(buf)
		if n > 0 {
			// Push chunk to Wails frontend via wails.Events.Emit("terminal-output", string(buf[:n]))
			fmt.Println(string(buf[:n]))
			runtime.EventsEmit(ctx, "terminal-output", string(buf[:n]))
		}
		if err != nil {
			break
		}
	}
}

func (s *ShellSession) Close() {
	s.stdin.Close()
	s.cmd.Process.Kill()
	s.wg.Wait()
}
