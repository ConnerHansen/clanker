package agent

import (
	"clankerv2/data"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// discover.go implements /discover: a deterministic scan walks the project tree,
// splits it into directory-first chunks, and fans out one read-only subagent per
// chunk to summarize what logic lives where. The main agent merges only the
// summaries (never raw code) into .clanker/project.md.

// ChunkTree splits scanned files into directory-first chunks capped by file
// count, so each subagent sees one complete area. Oversized directories recurse
// a level deeper; oversized leaves split into consecutive [N/M] parts; small
// sibling chunks merge within 50% tolerance. Deterministic: sorted input yields
// identical output.
func ChunkTree(files []ProjectFile) []ProjectChunk {
	cfg := GetAppConfig()
	limit := cfg.GetDiscoverFilesPerChunk()
	tolerance := limit + limit/2 // 50% tolerance for keeping dirs whole

	// Group by immediate parent directory; root-level files get "(root)"
	dirs := make(map[string][]ProjectFile)
	var dirOrder []string
	for _, f := range files {
		parent := filepath.Dir(f.Path)
		if parent == "." {
			parent = "(root)"
		}
		if _, ok := dirs[parent]; !ok {
			dirOrder = append(dirOrder, parent)
		}
		dirs[parent] = append(dirs[parent], f)
	}
	sort.Strings(dirOrder)

	return chunkDirectories(dirs, dirOrder, "", limit, tolerance)
}

// chunkDirectories chunks a set of directories: small ones are collected for
// merging, oversized ones recurse (subdirs) or split by file count (leaves).
func chunkDirectories(dirs map[string][]ProjectFile, dirOrder []string, parentDir string, limit int, tolerance int) []ProjectChunk {
	var chunks []ProjectChunk
	smallDirs := make(map[string][]ProjectFile)

	for _, dir := range dirOrder {
		files := dirs[dir]

		label := dir
		if parentDir != "" && !strings.HasPrefix(dir, parentDir) {
		} else if parentDir != "" {
			label = parentDir + "/" + filepath.Base(dir)
		}

		if len(files) <= limit {
			smallDirs[label] = files
		} else if containsSubdirs(files) {
			subChunks := splitDirectoryBySubdir(files, label, limit, tolerance)
			chunks = append(chunks, subChunks...)
		} else {
			parts := splitLeafDirectory(files, label, limit)
			chunks = append(chunks, parts...)
		}
	}

	merged := mergeSmallChunksByCount(smallDirs, limit, tolerance)
	return append(chunks, merged...)
}

// containsSubdirs reports whether files span more than one subdirectory.
func containsSubdirs(files []ProjectFile) bool {
	dirs := make(map[string]bool)
	for _, f := range files {
		parent := filepath.Dir(f.Path)
		dirs[parent] = true
	}
	return len(dirs) > 1
}

// splitDirectoryBySubdir groups an oversized directory's files by immediate subdirectory and recursively chunks each group.
func splitDirectoryBySubdir(files []ProjectFile, label string, limit int, tolerance int) []ProjectChunk {
	subDirs := make(map[string][]ProjectFile)
	var subDirOrder []string

	for _, f := range files {
		parent := filepath.Dir(f.Path)
		if _, ok := subDirs[parent]; !ok {
			subDirOrder = append(subDirOrder, parent)
		}
		subDirs[parent] = append(subDirs[parent], f)
	}
	sort.Strings(subDirOrder)
	return chunkDirectories(subDirs, subDirOrder, label, limit, tolerance)
}

// splitLeafDirectory splits files into consecutive parts when the directory
// is too large and has no meaningful subdirectory structure.
func splitLeafDirectory(files []ProjectFile, label string, limit int) []ProjectChunk {
	if len(files) <= limit {
		return []ProjectChunk{
			{Name: label, Files: files},
		}
	}

	var parts []ProjectChunk
	totalParts := (len(files) + limit - 1) / limit

	for i := 0; i < len(files); i += limit {
		end := i + limit
		if end > len(files) {
			end = len(files)
		}
		partFiles := files[i:end]
		var totalBytes int64
		for _, f := range partFiles {
			totalBytes += f.Size
		}
		parts = append(parts, ProjectChunk{
			Name:  fmt.Sprintf("%s [%d/%d]", label, len(parts)+1, totalParts),
			Files: partFiles,
			Bytes: totalBytes,
		})
	}
	return parts
}

// mergeSmallChunksByCount combines small chunks that share a common parent area.
// Uses the configured file count with 50% tolerance for keeping directories whole.
func mergeSmallChunksByCount(smallDirs map[string][]ProjectFile, limit int, tolerance int) []ProjectChunk {
	if len(smallDirs) <= 1 {
		for label, files := range smallDirs {
			var totalBytes int64
			for _, f := range files {
				totalBytes += f.Size
			}
			return []ProjectChunk{{Name: label, Files: files, Bytes: totalBytes}}
		}
		return nil
	}

	labels := make([]string, 0, len(smallDirs))
	for l := range smallDirs {
		labels = append(labels, l)
	}
	sort.Strings(labels)

	var merged []ProjectChunk
	var current ProjectChunk

	flush := func() {
		if len(current.Files) > 0 {
			merged = append(merged, current)
			current = ProjectChunk{}
		}
	}

	for _, label := range labels {
		files := smallDirs[label]
		var totalBytes int64
		for _, f := range files {
			totalBytes += f.Size
		}

		if len(current.Files) == 0 {
			current = ProjectChunk{Name: label, Files: files, Bytes: totalBytes}
			continue
		}

		// Merging past the base limit requires sharing a parent area; under it, always merge.
		combinedCount := len(current.Files) + len(files)
		canMerge := combinedCount <= limit
		if !canMerge && combinedCount <= tolerance {
			canMerge = shareParentArea(current.Name, label)
		}
		if canMerge {
			current.Name = current.Name + ", " + label
			current.Files = append(current.Files, files...)
			current.Bytes += totalBytes
		} else {
			flush()
			current = ProjectChunk{Name: label, Files: files, Bytes: totalBytes}
		}
	}
	flush()

	return merged
}

// shareParentArea reports whether two labels are sibling areas under the same
// parent; "(root)" merges freely (no directory affiliation).
func shareParentArea(a, b string) bool {
	if a == "(root)" || b == "(root)" {
		return true
	}

	topA := extractTopDir(a)
	topB := extractTopDir(b)
	return topA == topB
}

// extractTopDir returns the first path component.
func extractTopDir(path string) string {
	if i := strings.IndexByte(path, '/'); i > 0 {
		return path[:i]
	}
	return path
}

// discoverMaxFileSize caps one file: larger ones are skipped so a few bloated
// generated files can't force their own oversized chunks.
var discoverMaxFileSize int64 = 128 * 1024

const (
	// discoverProjectMDPath is where the merged map is stored; it is appended to
	// every prompt so a refreshed map becomes reusable knowledge for future sessions.
	discoverProjectMDPath = ".clanker/project.md"
)

// discoverToolsAllowlist is the read-only tool set each discover subagent gets:
// enough to explore, never enough to modify (orchestration tools are stripped anyway).
var discoverToolsAllowlist = []string{
	"system-read",
	"system-find",
	"system-list",
	"system-search_by_name",
}

// discoverIgnoreDirs: dependency/build directories skipped at any depth.
var discoverIgnoreDirs = map[string]bool{
	"node_modules": true, "deps": true, "vendor": true,
	".git": true, ".hg": true, ".svn": true,
	"dist": true, "build": true, "out": true, "target": true, "coverage": true,
	"__pycache__": true, ".next": true, ".nuxt": true,
	"bin": true, "obj": true,
}

// discoverSkipExts: known binary/generated extensions, a fast-path before the
// null-byte probe for unknown ones.
var discoverSkipExts = map[string]bool{
	// images
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true,
	".tiff": true, ".tif": true, ".webp": true, ".svg": true, ".ico": true,
	// media
	".mp3": true, ".mp4": true, ".wav": true, ".ogg": true, ".avi": true, ".mov": true,
	".wmv": true, ".flac": true, ".m4a": true, ".mkv": true,
	// fonts
	".ttf": true, ".otf": true, ".woff": true, ".woff2": true, ".eot": true,
	// archives and model weights
	".zip": true, ".tar": true, ".gz": true, ".tgz": true, ".bz2": true,
	".7z": true, ".rar": true,
	".gguf": true, ".safetensors": true, ".onnx": true, ".pt": true,
	".bin": true, ".ckpt": true,
	// compiled objects
	".o": true, ".a": true, ".so": true, ".dylib": true, ".dll": true,
	".exe": true, ".wasm": true,
}

const discoverBinaryProbeSize = 512

// discoverIsBinary checks if a file is binary by looking at its first bytes.
// Returns false (not binary) for empty files.
func discoverIsBinary(root string, relPath string) bool {
	path := filepath.Join(root, filepath.FromSlash(relPath))
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()

	buf := make([]byte, discoverBinaryProbeSize)
	n, err := io.ReadFull(f, buf)
	if n == 0 {
		return false
	}
	if err != nil && err != io.ErrUnexpectedEOF {
		return true
	}
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return true
		}
	}
	return false
}

// discoverSkipFiles: dependency lockfiles and generated bundles, matched case-insensitively.
var discoverSkipFiles = map[string]bool{
	"package-lock.json": true, "yarn.lock": true, "pnpm-lock.yaml": true,
	"cargo.lock": true, "go.sum": true, "composer.lock": true, "poetry.lock": true,
}

// ProjectFile is one scanned file: a relative (slash-separated) path and its
// size in bytes.
type ProjectFile struct {
	Path string
	Size int64
}

// ProjectChunk is a logical slice of the tree handed to one subagent: files
// from one directory area, capped by configured file count (split into part N/M
// when bigger).
type ProjectChunk struct {
	Name  string // e.g. "agent" or "frontend/src/components [2/3]"
	Files []ProjectFile
	Bytes int64
}

// ScanProjectTree walks root recursively and returns every scannable file as a
// relative path + size. Dependency/build directories are pruned wholesale; binary
// files are detected via null-byte probe on the first 512 bytes (with known binary
// extensions as a fast-path); files over discoverMaxFileSize are skipped.
// Symlinked entries are skipped (never followed). Depth is capped by config
// (default 32) to prevent runaway scans on deeply nested trees.
// Deterministic: results are sorted by path.
func ScanProjectTree(root string) ([]ProjectFile, error) {
	cfg := GetAppConfig()
	maxDepth := cfg.GetDiscoverMaxDepth()
	var files []ProjectFile

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		name := d.Name()

		relPath, _ := filepath.Rel(root, path)
		if relPath == "." {
			relPath = ""
		}
		depth := strings.Count(relPath, string(filepath.Separator))

		if d.IsDir() {
			if path != root && discoverIgnoreDirs[name] {
				return filepath.SkipDir
			}
			if depth >= maxDepth {
				return filepath.SkipDir
			}
			return nil
		}

		// Depth check for files: if the parent dir is already at maxDepth, skip
		if depth >= maxDepth {
			return nil
		}

		if isSymlink(d) || discoverSkipFile(name) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > discoverMaxFileSize {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(name))
		if ext == "" || !discoverSkipExts[ext] {
			if discoverIsBinary(root, filepath.ToSlash(relPath)) {
				return nil
			}
		}

		files = append(files, ProjectFile{Path: filepath.ToSlash(relPath), Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func isSymlink(d os.DirEntry) bool {
	info, err := d.Info()
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

// discoverSkipFile reports whether a name is a lockfile or known binary extension.
func discoverSkipFile(name string) bool {
	lower := strings.ToLower(name)
	if discoverSkipFiles[lower] {
		return true
	}
	return discoverSkipExts[strings.ToLower(filepath.Ext(lower))]
}

// DiscoverModel picks the discover driver: the discovery profile's model when set,
// else "" — SpawnSubagent then inherits the main runtime's loaded model (reusing
// its executor) and finally falls back to system.defaultModel.
func DiscoverModel() string {
	cfg := GetAppConfig()
	if cfg == nil {
		return ""
	}
	if name := cfg.GetDefaultDiscoverySubagent(); name != "" {
		if p := cfg.GetSubagentProfile(name); p != nil && p.Model != "" {
			return p.Model
		}
	}
	return ""
}

// resolveDiscoverProfile validates system.defaultDiscoverySubagent; an empty name
// applies built-in defaults, an unknown one is a hard error surfaced before any spawn.
func resolveDiscoverProfile() (string, *data.SubagentProfile, error) {
	cfg := GetAppConfig()
	if cfg == nil {
		return "", nil, nil
	}
	name := cfg.GetDefaultDiscoverySubagent()
	if name == "" {
		return "", nil, nil
	}
	p := cfg.GetSubagentProfile(name)
	if p == nil {
		return "", nil, fmt.Errorf("system.defaultDiscoverySubagent references unknown subagent profile %q (see the \"subagents\" block of .clanker/config.json)", name)
	}
	return name, p, nil
}

// buildDiscoverSpawnArgs assembles one chunk's spawn arguments. The configured
// profile acts as a launch preset: its tools/deadline are forwarded when set,
// otherwise discover's built-in read-only allowlist is used and no deadline is
// sent — the spawner then applies the standard subagent deadline. Task and
// stealth mode are always discover's own.
func buildDiscoverSpawnArgs(profileName string, profile *data.SubagentProfile, chunk ProjectChunk) map[string]any {
	args := map[string]any{
		"task": BuildDiscoverChunkTaskPrompt(chunk),
		"mode": "stealth",
	}
	// spawn args carry tools as []any (buildSubagentSpec type-asserts that shape).
	toolsFrom := func(names []string) []any {
		out := make([]any, 0, len(names))
		for _, n := range names {
			out = append(out, n)
		}
		return out
	}
	if profile == nil || len(profile.AvailableTools) == 0 {
		args["tools"] = toolsFrom(discoverToolsAllowlist)
	} else {
		args["tools"] = toolsFrom(profile.AvailableTools)
	}
	if profile != nil && profile.DeadlineSeconds > 0 {
		args["deadlineSeconds"] = profile.DeadlineSeconds
	}
	if profile != nil {
		args["profile"] = profileName
	}
	return args
}

// discoverSpawner is the seam tests use to stub/observe spawn calls; production
// registers under "main" so the main agent's system-await_subagent collects results.
var discoverSpawner = func(callerID, model string, args map[string]any) (string, string, error) {
	return SpawnSubagent(callerID, model, args)
}

// discoverMainTaskFn hands the merge task to the main agent; tests replace it to
// capture the task without a live stream.
var discoverMainTaskFn = func(channel chan any, task, model string) context.CancelFunc {
	return PromptWithContext(nil, channel, task, model)
}

// BuildDiscoverChunkTaskPrompt is the self-contained task one subagent receives:
// explore its chunk and finalize a compact markdown summary; it must stand alone.
func BuildDiscoverChunkTaskPrompt(chunk ProjectChunk) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "You are a read-only code-exploration subagent. Your job is to map ONE chunk of a project so its structure becomes reusable knowledge.\n\n")
	sb.WriteString("Your chunk: ")
	sb.WriteString(chunk.Name)
	fmt.Fprintf(&sb, " (%d file(s), ~%d KB). File list:\n", len(chunk.Files), (chunk.Bytes+1023)/1024)
	for _, f := range chunk.Files {
		fmt.Fprintf(&sb, "- %s (%d bytes)\n", f.Path, f.Size)
	}

	sb.WriteString(`
Explore the files above with your read-only tools (start with the largest/most central ones; skim headers, type declarations, exported functions — you do NOT need to read every line). Never attempt any modification.

Then call system-subagent_finalize ONCE with a compact markdown summary that covers:
1. **Where things live**: what logic is in which directories/files (one bullet per meaningful area, naming the key files).
2. **Design approach**: how this part of the code is structured and fits together (patterns, data flow, notable abstractions).
3. **Kind of project code**: what this chunk IS (e.g. TUI layer, LLM engine, config plumbing) in one sentence.

Keep it under ~200 lines. No preamble, no "here is your summary".`)
	return sb.String()
}

// buildDiscoverMergePrompt is the task handed to the MAIN agent after spawning:
// collect each subagent's summary (summaries only — the main context never sees
// raw code) and merge them into the marked section of .clanker/project.md,
// replacing ONLY that section on re-runs.
func buildDiscoverMergePrompt(spawns []subagentRef, model string, deadlineSec int) string {
	var sb strings.Builder
	sb.WriteString("/discover has launched background subagents to map this project's structure (model: ")
	sb.WriteString(model)
	sb.WriteString("). They are running right now.\n\nSubagents by ID:\n")
	for _, s := range spawns {
		fmt.Fprintf(&sb, "- %s → chunk %q\n", s.ID, s.chunkName)
	}

	sb.WriteString(`
Do the following, in order:
1. Collect each summary with system-await_subagent (pass the id and timeoutSeconds `)
	sb.WriteString(fmt.Sprintf("%d", deadlineSec))
	sb.WriteString(`). If an await returns status "pending", poll with system-subagent_status and await again until it reaches a terminal state. For any subagent that fails/times out, note its chunk name as unmapped and continue — do not block the whole merge on it.
2. You now have one markdown summary per chunk. Merge them into a single coherent project map covering: (a) what logic lives in which directories, (b) the overall code design approach across the project, (c) what kind of project code each folder contains. De-duplicate and order top-level areas consistently; keep it tight — this file is loaded into every future prompt as reusable knowledge ("where to look for what"), so prefer pointers over prose. Additionally it is possible that the previous project mapping contains stale or outdated information and therefore must not be regarded as a source of truth and must instead be approached with skepticism.
3. Update `)
	sb.WriteString(discoverProjectMDPath)
	sb.WriteString(` with the merged map:
   - FIRST read the file (system-read) — a write without a fresh read is forbidden.
   - If it contains the markers `)
	sb.WriteString(data.ProjectMapStartMarker)
	sb.WriteString(` and `)
	sb.WriteString(data.ProjectMapEndMarker)
	sb.WriteString(`: use system-modify with the anchor shape to replace ONLY the section between them — set 'start' to the start marker line and 'end' to the end marker line (the region is inclusive of both), and put '<start marker>\n<new map>\n<end marker>' in 'new'. Everything outside the markers must stay byte-identical.
   - If the markers are absent: keep ALL existing content and append the new section at the end of the file, wrapped in those two marker lines (use system-write with the full new content).
4. Finish with a one-line console report: how many chunks merged, which (if any) failed, and that `)
	sb.WriteString(discoverProjectMDPath)
	sb.WriteString(` was updated.

Do not start other subagents for this task, and do not modify any code files — the only file you edit is `)
	sb.WriteString(discoverProjectMDPath)
	sb.WriteString(`.`)
	return sb.String()
}

// subagentRef pairs a spawned id with the chunk it covers.
type subagentRef struct {
	ID        string
	chunkName string
}

// RunDiscoverTask kicks off a full /discover run against the current working
// directory: scan → chunk → spawn one read-only subagent per chunk → hand the
// main agent the collection+merge task.
//
// It RETURNS IMMEDIATELY: the whole pipeline (including the merge turn) runs on
// a background goroutine and streams through resp exactly like an interactive
// turn. Callers may therefore launch it from a UI update loop without stalling
// keystroke or message processing — the scan/spawn work and its console events
// all happen off-caller, never on the goroutine that invoked this function.
// The returned CancelFunc stops the main-agent part of the run (already-spawned
// subagents finish on their own; /subagents lists them). If it fires before the
// merge turn has started (during scan/spawn), it is deferred and takes effect
// as soon as that turn begins; it never blocks the caller.
func RunDiscoverTask(resp chan any, model string) context.CancelFunc {
	cancelCh := make(chan context.CancelFunc, 1)
	go func() {
		root, err := os.Getwd()
		var cf context.CancelFunc
		if err != nil {
			consoleError(resp, "/discover: cannot determine project root: "+err.Error())
			cf = func() {}
		} else {
			cf = runDiscoverWithRoot(root, resp, model)
		}
		cancelCh <- cf
	}()

	return func() {
		go func() {
			if cf, ok := <-cancelCh; ok && cf != nil {
				cf()
			}
		}()
	}
}

// runDiscoverWithRoot is the testable core of RunDiscoverTask.
func runDiscoverWithRoot(root string, resp chan any, model string) context.CancelFunc {
	profileName, profile, perr := resolveDiscoverProfile()
	if perr != nil {
		consoleError(resp, "/discover: "+perr.Error())
		return func() {}
	}

	files, err := ScanProjectTree(root)
	if err != nil {
		consoleError(resp, "/discover: scan failed: "+err.Error())
		return func() {}
	}
	if len(files) == 0 {
		consoleError(resp, "/discover: no scannable project files found (everything ignored?)")
		return func() {}
	}

	chunks := ChunkTree(files)
	spawns := make([]subagentRef, 0, len(chunks))
	deadlineSec := int(defaultSubagentDeadline / time.Second)
	if profile != nil && profile.DeadlineSeconds > 0 {
		deadlineSec = profile.DeadlineSeconds
	}
	discoverModel := DiscoverModel()
	if discoverModel == "" {
		discoverModel = model
	}
	for _, chunk := range chunks {
		id, spawnModel, err := discoverSpawner("main", discoverModel, buildDiscoverSpawnArgs(profileName, profile, chunk))
		if err != nil {
			consoleError(resp, "/discover: failed to spawn subagent for chunk "+chunk.Name+": "+err.Error())
			return func() {}
		}
		spawns = append(spawns, subagentRef{ID: id, chunkName: chunk.Name})
		log.Printf("/discover: chunk %q → %s (model=%s)", chunk.Name, id, spawnModel)
	}

	maxAgents := GetAppConfig().GetDiscoverMaxAgents()
	resp <- data.Response{
		Type:    "console",
		Message: fmt.Sprintf("/discover: %d file(s) in %d chunk(s) — %d subagent(s) launched (up to %d run at once), merging into %s when done.", len(files), len(chunks), len(spawns), maxAgents, discoverProjectMDPath),
	}

	task := buildDiscoverMergePrompt(spawns, model, deadlineSec)
	return discoverMainTaskFn(resp, task, model)
}

func consoleError(resp chan any, msg string) {
	resp <- data.Response{Type: "console", Message: msg}
	log.Println(msg)
}
