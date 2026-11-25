package tools

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// SearchFilesByNameResult is the structured result of a glob filename search.
type SearchFilesByNameResult struct {
	Status  string   `json:"status"`
	Pattern string   `json:"pattern"`
	Matches []string `json:"matches"`
	Count   int      `json:"match_count"`
	Error   string   `json:"error,omitempty"`
}

// SearchFilesByName finds files matching a glob pattern recursively.
func SearchFilesByName(pattern string, rootPath *string) *SearchFilesByNameResult {
	if pattern == "" {
		return &SearchFilesByNameResult{Status: "error", Error: "pattern is required"}
	}

	root := "."
	if rootPath != nil && *rootPath != "" {
		root = *rootPath
	}
	if !filepath.IsAbs(root) {
		wd, _ := os.Getwd()
		root = filepath.Join(wd, root)
	}

	var matches []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		matched, err := filepath.Match(pattern, d.Name())
		if err != nil {
			return nil
		}
		if matched {
			fullPath := toRelativePath(p)
			matches = append(matches, fullPath)
			if len(matches) >= MaxNameSearch {
				return filepath.SkipDir
			}
		}
		return nil
	})

	if err != nil {
		return &SearchFilesByNameResult{
			Status: "error",
			Error:  fmt.Sprintf("failed to write file: %v", err),
		}
	}

	return &SearchFilesByNameResult{
		Status:  "success",
		Pattern: pattern,
		Matches: matches,
		Count:   len(matches),
		Error:   "",
	}
}

// isTextFile checks if a file is text by reading the first 512 bytes.
func isTextFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return false
	}

	return !slices.Contains(buf[:n], 0)
}

// toRelativePath renders path relative to the working directory with a "./"
// prefix (used by listings and search results so model-visible paths are
// stable). Falls back to the input on any resolution error.
func toRelativePath(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil {
		return path
	}
	if !strings.HasPrefix(rel, ".") {
		return "./" + rel
	}
	return rel
}
