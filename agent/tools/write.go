package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileResult is the structured result of a file write.
type WriteFileResult struct {
	Status string `json:"status"` // "success" or "error"
	Path   string `json:"path"`
	Bytes  int    `json:"bytes_written"`
	Error  string `json:"error,omitempty"`
}

// DeleteFileResult is the structured result of a file deletion.
type DeleteFileResult struct {
	Status string `json:"status"` // "success" or "error"
	Path   string `json:"path"`
	Error  string `json:"error,omitempty"`
}

// WriteFile creates or completely overwrites a file with the provided content.
func WriteFile(path string, content string) *WriteFileResult {
	if path == "" {
		return &WriteFileResult{Status: "error", Error: "path is required"}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return &WriteFileResult{Status: "error", Error: fmt.Sprintf("failed to resolve path: %v", err)}
	}

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &WriteFileResult{Status: "error", Error: fmt.Sprintf("failed to create directories: %v", err)}
	}

	contentBytes := []byte(content)
	if err := os.WriteFile(absPath, contentBytes, 0644); err != nil {
		return &WriteFileResult{Status: "error", Path: absPath, Error: fmt.Sprintf("failed to write file: %v", err)}
	}

	return &WriteFileResult{
		Status: "success",
		Path:   toRelativePath(absPath),
		Bytes:  len(contentBytes),
	}
}

// DeleteFile removes a single file from disk. Directories are not supported;
// callers (the handler) stat-gate before asking the user for permission, so a
// deletion only runs against an existing regular file.
func DeleteFile(path string) *DeleteFileResult {
	if path == "" {
		return &DeleteFileResult{Status: "error", Error: "path is required"}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return &DeleteFileResult{Status: "error", Error: fmt.Sprintf("failed to resolve path: %v", err)}
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return &DeleteFileResult{Status: "error", Path: absPath, Error: fmt.Sprintf("file does not exist: %v", err)}
	}
	if info.IsDir() {
		return &DeleteFileResult{Status: "error", Path: absPath, Error: "path is a directory; only files can be deleted"}
	}

	if err := os.Remove(absPath); err != nil {
		return &DeleteFileResult{Status: "error", Path: absPath, Error: fmt.Sprintf("failed to delete file: %v", err)}
	}

	return &DeleteFileResult{
		Status: "success",
		Path:   toRelativePath(absPath),
	}
}
