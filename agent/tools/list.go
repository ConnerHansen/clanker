package tools

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// DirEntry is one file or directory within a listing.
type DirEntry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"` // file size in bytes; 0 for directories
	Lines int    `json:"lines,omitempty"` // line count (system-read record semantics); 0 for directories
}

// ListDirectoryResult is the structured result of a directory listing.
type ListDirectoryResult struct {
	Status  string     `json:"status"`
	Path    string     `json:"path"`
	Entries []DirEntry `json:"entries"`
	Count   int        `json:"entry_count"`
	Error   string     `json:"error,omitempty"`
}

// ListDirectory returns files and directories in a path. Hidden (dot) files
// and directories are skipped unless showHidden is true; each file entry
// carries its byte size and line count.
func ListDirectory(path string, recursive *bool, maxDepth *int, showHidden *bool) *ListDirectoryResult {
	if path == "" {
		path = "./"
	}

	entries := []DirEntry{}
	rec := false
	if recursive != nil {
		rec = *recursive
	}
	showH := false
	if showHidden != nil {
		showH = *showHidden
	}
	max := 1
	if maxDepth != nil {
		max = *maxDepth
	}
	if max == 1 {
		max = MaxDirectoryDepth
	}

	if !rec {
		max = 1
	}

	currentDepth := 0
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		base := filepath.Base(p)
		if !showH && strings.HasPrefix(base, ".") && base != "." && base != ".." {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, _ := d.Info()
		e := DirEntry{
			Path:  toRelativePath(p),
			IsDir: d.IsDir(),
			Size:  info.Size(),
		}
		if !e.IsDir {
			e.Lines = countFileLines(p)
		}
		entries = append(entries, e)

		if d.IsDir() {
			currentDepth++
			if currentDepth > max {
				return filepath.SkipDir
			}
		}
		return nil
	})

	if err != nil {
		return &ListDirectoryResult{
			Status: "error",
			Error:  fmt.Sprintf("failed to write file: %v", err),
		}
	}

	return &ListDirectoryResult{
		Status:  "success",
		Path:    path,
		Entries: entries,
		Count:   len(entries),
		Error:   "",
	}
}
