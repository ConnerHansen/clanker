package tools

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// FileMatch is one matched line within a searched file.
type FileMatch struct {
	Path  string `json:"path"`
	Line  int    `json:"line"`
	Match string `json:"match"`
}

// FindInCodebaseResult is the structured result of a codebase regex search.
type FindInCodebaseResult struct {
	Status  string      `json:"status"`
	Matches []FileMatch `json:"matches"`
	Count   int         `json:"match_count"`
	Error   string      `json:"error,omitempty"`
}

// FindInCodebase searches file contents with a regex pattern.
func FindInCodebase(regex string, rootPath *string) *FindInCodebaseResult {
	if regex == "" {
		return &FindInCodebaseResult{Status: "error", Error: "regex pattern is required"}
	}

	pattern, err := regexp.Compile(`(?i)` + regex)
	if err != nil {
		return &FindInCodebaseResult{Status: "error", Error: fmt.Sprintf("invalid regex: %v", err)}
	}

	root := "."
	if rootPath != nil && *rootPath != "" {
		root = *rootPath
	}
	if !filepath.IsAbs(root) {
		wd, _ := os.Getwd()
		root = filepath.Join(wd, root)
	}

	var matches []FileMatch
	skipDirs := map[string]bool{"node_modules": true, ".git": true, "vendor": true, "dist": true, "build": true, "__pycache__": true}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDirs[filepath.Base(path)] {
				return filepath.SkipDir
			}
			return nil
		}

		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") || !isTextFile(path) {
			return nil
		}

		if len(matches) >= MaxSearchResults {
			return filepath.SkipDir
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		lines := strings.Split(string(data), "\n")
		fullPath := toRelativePath(path)
		for i, line := range lines {
			if pattern.MatchString(line) {
				matches = append(matches, FileMatch{
					Path:  fullPath,
					Line:  i + 1,
					Match: strings.TrimSpace(line[:min(100, len(line))]),
				})
				if len(matches) >= MaxSearchResults {
					return filepath.SkipDir
				}
			}
		}
		return nil
	})

	return &FindInCodebaseResult{
		Status:  "success",
		Matches: matches,
		Count:   len(matches),
		Error:   "",
	}
}
