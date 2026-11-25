package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// GenerateDiff creates a temporary file and returns the git diff against the original.
func GenerateDiff(path string, newContent string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	// If the original file doesn't exist, create an empty temp file to represent it
	// so git diff has something to compare against.
	originalPath := absPath
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		tmpOrig, err := os.CreateTemp("", "clanker-orig-*.txt")
		if err != nil {
			return "", fmt.Errorf("failed to create original temp file: %v", err)
		}
		tmpOrig.Close()
		defer os.Remove(tmpOrig.Name())
		originalPath = tmpOrig.Name()
	}

	tmpFile, err := os.CreateTemp("", "clanker-diff-*.txt")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.WriteString(newContent); err != nil {
		return "", fmt.Errorf("failed to write to temp file: %v", err)
	}
	tmpFile.Close()

	// git diff --no-index <file1> <file2>
	cmd := exec.Command("git", "diff", "--no-index", "--color=always", originalPath, tmpFile.Name())
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(out) == 0 {
			return "", fmt.Errorf("git diff error: %v", err)
		}
	}

	return string(out), nil
}
