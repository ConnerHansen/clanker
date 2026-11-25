package agent

import (
	"clankerv2/data"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- /discover tests ---

func mkDiscoverFile(path string, size int64) ProjectFile {
	return ProjectFile{Path: path, Size: size}
}

// writeTree creates root/rel (with parent dirs) containing content.
func writeTree(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanProjectTreeIgnoresAndSkips(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "agent/executor.go", "package agent\n")
	writeTree(t, root, "tui/tui.go", "package tui\n")
	writeTree(t, root, "node_modules/pkg/index.js", "// dep\n")
	writeTree(t, root, "deps/lib.py", "# dep\n")
	writeTree(t, root, "dist/bundle.js", "// built\n")
	writeTree(t, root, ".git/config", "[core]\n")
	writeTree(t, root, "data/icon.png", "PNGDATA")
	writeTree(t, root, "go.sum", "h1:sum\n")
	writeTree(t, root, "README.md", "# readme\n")
	writeTree(t, root, "data/big.dat", strings.Repeat("x", 200*1024)) // over the 128kB per-file gate
	writeTree(t, root, ".clanker/config.json", "{}\n")

	files, err := ScanProjectTree(root)
	if err != nil {
		t.Fatal(err)
	}

	got := make(map[string]bool, len(files))
	for _, f := range files {
		got[f.Path] = true
	}

	for _, want := range []string{"agent/executor.go", "tui/tui.go", "README.md", ".clanker/config.json"} {
		if !got[want] {
			t.Errorf("expected %q in scan, got %v", want, keys(got))
		}
	}
	for _, absent := range []string{
		"node_modules/pkg/index.js", "deps/lib.py", "dist/bundle.js",
		".git/config", "data/icon.png", "go.sum",
		"data/big.dat", // over the 128kB per-file size gate
	} {
		if got[absent] {
			t.Errorf("expected %q to be skipped, scan returned it", absent)
		}
	}

	// Sorted output.
	for i := 1; i < len(files); i++ {
		if files[i-1].Path >= files[i].Path {
			t.Fatalf("scan output not sorted: %v before %v", files[i-1].Path, files[i].Path)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestChunkTreeGroupsByDirAndLanguage(t *testing.T) {
	files := []ProjectFile{
		mkDiscoverFile("agent/a.go", 100),
		mkDiscoverFile("agent/b.md", 50),
		mkDiscoverFile("frontend/x.ts", 80),
		mkDiscoverFile("main.go", 40),
	}

	chunks := ChunkTree(files)
	// All four directories are tiny (1 file each) so the merge pass folds them
	// into one chunk: fewer subagents, each covering more of the tree.
	if len(chunks) != 1 {
		t.Fatalf("want the 4 tiny groups merged into 1 chunk, got %d: %v", len(chunks), chunkNames(chunks))
	}

	// Directories sorted by path; merged names join with ", ".
	for _, p := range []string{"main.go", "agent/a.go", "agent/b.md", "frontend/x.ts"} {
		if !containsPath(chunks[0].Files, p) {
			t.Errorf("merged chunk missing %s", p)
		}
	}
	if chunks[0].Bytes != 270 {
		t.Errorf("merged bytes = %d, want 270", chunks[0].Bytes)
	}

	// Determinism: same input twice → identical output.
	again := ChunkTree(files)
	if len(again) != len(chunks) || !sameChunks(again, chunks) {
		t.Error("ChunkTree is not deterministic")
	}
}

// TestChunkTreeSplitsOversizedLeaf tests that a leaf directory with more
// files than the configured limit splits into consecutive parts.
func TestChunkTreeSplitsOversizedLeaf(t *testing.T) {
	setTestAppConfig(t, &data.AppConfig{
		System: &data.SystemSettings{DiscoverFilesPerChunk: 3},
	})

	// 4 files in "big" directory with limit=3 → two parts [1/2], [2/2]
	files := []ProjectFile{
		mkDiscoverFile("big/a.go", 100),
		mkDiscoverFile("big/b.go", 100),
		mkDiscoverFile("big/c.go", 100),
		mkDiscoverFile("big/d.go", 100),
	}

	chunks := ChunkTree(files)
	if len(chunks) != 2 {
		t.Fatalf("want 2 parts, got %d: %v", len(chunks), chunkNames(chunks))
	}
	// Each part should have ≤ 3 files (the configured limit)
	for _, c := range chunks {
		if len(c.Files) > 3 {
			t.Errorf("chunk %s has %d files, want ≤ 3", c.Name, len(c.Files))
		}
	}
	total := 0
	for _, c := range chunks {
		total += len(c.Files)
	}
	if total != 4 {
		t.Errorf("parts lost files: have %d of 4", total)
	}
}

// TestChunkTreeOversizedDescendsSubdirs tests that an oversized directory
// with subdirectories recurses deeper before splitting.
func TestChunkTreeOversizedDescendsSubdirs(t *testing.T) {
	setTestAppConfig(t, &data.AppConfig{
		System: &data.SystemSettings{DiscoverFilesPerChunk: 2},
	})

	// "big" has 6 files total across 3 subdirs — limit=2.
	// Each subdir (a/, b/, c/) has exactly 2 files → each fits in one chunk.
	files := []ProjectFile{
		mkDiscoverFile("big/a/1.go", 10),
		mkDiscoverFile("big/a/2.go", 10),
		mkDiscoverFile("big/b/1.go", 10),
		mkDiscoverFile("big/b/2.go", 10),
		mkDiscoverFile("big/c/1.go", 10),
		mkDiscoverFile("big/c/2.go", 10),
	}

	chunks := ChunkTree(files)
	// Should descend into a/, b/, c/ and produce one chunk each (2 files ≤ limit=2)
	if len(chunks) != 3 {
		t.Fatalf("want 3 chunks (one per subdir), got %d: %v", len(chunks), chunkNames(chunks))
	}

	// Each chunk should have exactly 2 files
	for i, c := range chunks {
		if len(c.Files) != 2 {
			t.Errorf("chunk[%d] %q has %d files, want 2", i, c.Name, len(c.Files))
		}
	}
}

// TestChunkTreeConfigurableLimit tests that the configured DiscoverFilesPerChunk
// controls chunk boundaries.
func TestChunkTreeConfigurableLimit(t *testing.T) {
	setTestAppConfig(t, &data.AppConfig{
		System: &data.SystemSettings{DiscoverFilesPerChunk: 5},
	})

	// 10 files across 2 dirs; limit=5 → each dir fits in one chunk
	files := []ProjectFile{}
	for i := 0; i < 3; i++ {
		files = append(files, mkDiscoverFile(fmt.Sprintf("alpha/%d.go", i), 10))
	}
	for i := 0; i < 4; i++ {
		files = append(files, mkDiscoverFile(fmt.Sprintf("beta/%d.go", i), 10))
	}

	chunks := ChunkTree(files)
	if len(chunks) != 2 {
		t.Fatalf("want 2 chunks, got %d: %v", len(chunks), chunkNames(chunks))
	}
}

func TestChunkTreeEmpty(t *testing.T) {
	if got := ChunkTree(nil); len(got) != 0 {
		t.Fatalf("want no chunks, got %v", chunkNames(got))
	}
}

func chunkNames(cs []ProjectChunk) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func sameChunks(a, b []ProjectChunk) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || len(a[i].Files) != len(b[i].Files) {
			return false
		}
		for j := range a[i].Files {
			if a[i].Files[j].Path != b[i].Files[j].Path {
				return false
			}
		}
	}
	return true
}

func containsPath(files []ProjectFile, p string) bool {
	for _, f := range files {
		if f.Path == p {
			return true
		}
	}
	return false
}

// --- model + profile resolution ---

// TestDiscoverModelPrecedence pins the model chain: the discovery subagent
// profile's model (when it sets one), else "" — the caller falls back to the
// currently selected model. The summarizer must never be consulted here.
func TestDiscoverModelPrecedence(t *testing.T) {
	cfg := &data.AppConfig{
		Models: []data.ModelConfig{
			modelCfg("sum", "openai", "http://a"),
			modelCfg("disc", "openai", "http://b"),
			modelCfg("chat", "openai", "http://c"),
		},
		System: &data.SystemSettings{DefaultSummarizer: "sum"},
		Subagents: map[string]data.SubagentProfile{
			"discprofile": {Name: "discprofile", Model: "disc"},
		},
	}

	// No profile set → "" (caller falls back to the currently selected model);
	// defaultSummarizer must not win.
	setTestAppConfig(t, &data.AppConfig{Models: cfg.Models, System: &data.SystemSettings{DefaultSummarizer: "sum"}})
	if got := DiscoverModel(); got != "" {
		t.Errorf("no profile: got %q, want empty", got)
	}

	// Profile with a model → it wins over defaultSummarizer.
	cfgWithDisc := *cfg
	cfgWithDisc.System.DefaultDiscoverySubagent = "discprofile"
	setTestAppConfig(t, &cfgWithDisc)
	if got := DiscoverModel(); got != "disc" {
		t.Errorf("profile model: got %q, want disc", got)
	}

	// Profile without a model → "" (falls back to the currently selected model,
	// never the summarizer).
	cfg2 := *cfg
	cfg2.Subagents = map[string]data.SubagentProfile{
		"nomodel": {Name: "nomodel"}, // no Model field
	}
	cfg2.System.DefaultDiscoverySubagent = "nomodel"
	setTestAppConfig(t, &cfg2)
	if got := DiscoverModel(); got != "" {
		t.Errorf("profile w/o model: got %q, want empty", got)
	}

	// Neither → "" (caller/SpawnSubagent inherits the main runtime's model).
	setTestAppConfig(t, &data.AppConfig{Models: cfg.Models})
	if got := DiscoverModel(); got != "" {
		t.Errorf("nothing configured: got %q, want empty", got)
	}

	// Unknown profile name is silently tolerated here (hard error lives in
	// resolveDiscoverProfile, checked pre-spawn).
	cfg3 := *cfg
	cfg3.System.DefaultDiscoverySubagent = "ghost"
	setTestAppConfig(t, &cfg3)
	if got := DiscoverModel(); got != "" {
		t.Errorf("unknown profile: got %q, want empty", got)
	}
}

func TestResolveDiscoverProfile(t *testing.T) {
	cfg := &data.AppConfig{
		System: &data.SystemSettings{DefaultDiscoverySubagent: "disc"},
		Subagents: map[string]data.SubagentProfile{
			"disc": {Name: "disc", Model: "m1"},
		},
	}

	setTestAppConfig(t, cfg)
	name, p, err := resolveDiscoverProfile()
	if err != nil || name != "disc" || p == nil || p.Model != "m1" {
		t.Fatalf("valid profile: name=%q p=%v err=%v", name, p, err)
	}

	setTestAppConfig(t, &data.AppConfig{System: &data.SystemSettings{DefaultDiscoverySubagent: "ghost"}})
	if _, _, err = resolveDiscoverProfile(); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("unknown profile should error naming it, got %v", err)
	}

	setTestAppConfig(t, &data.AppConfig{})
	name, p, err = resolveDiscoverProfile()
	if err != nil || name != "" || p != nil {
		t.Fatalf("unset profile: name=%q p=%v err=%v; want all empty", name, p, err)
	}
}

func TestBuildDiscoverSpawnArgs(t *testing.T) {
	chunk := ProjectChunk{Name: "agent (go)", Files: []ProjectFile{mkDiscoverFile("agent/a.go", 10)}, Bytes: 10}

	// No profile: built-ins everywhere.
	args := buildDiscoverSpawnArgs("", nil, chunk)
	if args["mode"] != "stealth" {
		t.Errorf("mode = %v, want stealth", args["mode"])
	}
	if tools, ok := args["tools"].([]any); !ok || len(tools) != 4 {
		t.Errorf("tools = %v, want the 4 built-in read-only tools", args["tools"])
	}
	if _, has := args["deadlineSeconds"]; has {
		t.Error("no profile: spawn args must not carry a deadline (spawner applies the standard subagent deadline)")
	}
	if _, has := args["profile"]; has {
		t.Error("no profile: spawn args must not carry a profile key")
	}
	if !strings.Contains(args["task"].(string), "agent (go)") || !strings.Contains(args["task"].(string), "agent/a.go") {
		t.Error("task prompt missing chunk name/file list")
	}

	// Profile preset: its tools + deadline win; profile key is passed so the
	// spec builder can resolve model/prompt too.
	p := &data.SubagentProfile{Name: "disc", AvailableTools: []string{"system-read"}, Model: "m1", DeadlineSeconds: 120}
	args = buildDiscoverSpawnArgs("disc", p, chunk)
	if args["profile"] != "disc" {
		t.Errorf("profile key = %v, want disc", args["profile"])
	}
	if tools, ok := args["tools"].([]any); !ok || len(tools) != 1 || tools[0] != "system-read" {
		t.Errorf("profile tools not honored: %v", args["tools"])
	}
	if d, _ := args["deadlineSeconds"].(int); d != 120 {
		t.Errorf("profile deadline not forwarded: %v, want 120", args["deadlineSeconds"])
	}
}

func TestBuildDiscoverMergePrompt(t *testing.T) {
	spawns := []subagentRef{
		{ID: "sub-7", chunkName: "agent (go)"},
		{ID: "sub-8", chunkName: "tui (go)"},
	}
	prompt := buildDiscoverMergePrompt(spawns, "gemma-4-12b", 3600)

	for _, want := range []string{
		"sub-7", "sub-8", "agent (go)", "tui (go)",
		data.ProjectMapStartMarker, data.ProjectMapEndMarker,
		discoverProjectMDPath,
		"timeoutSeconds 3600",
		"system-await_subagent",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("merge prompt missing %q", want)
		}
	}

	// Profile-set deadline flows into the await guidance.
	prompt2 := buildDiscoverMergePrompt(spawns, "m1", 120)
	if !strings.Contains(prompt2, "timeoutSeconds 120") {
		t.Error("merge prompt should use the profile's deadline for awaits")
	}
}

// --- end-to-end run (stubbed spawner + main task seam) ---

type discoverSpawnCall struct {
	callerID string
	model    string
	args     map[string]any
}

func TestRunDiscoverEndToEnd(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "agent/a.go", "package agent\n")
	writeTree(t, root, "agent/b.go", "package agent\n")
	writeTree(t, root, "tui/tui.go", "package tui\n")
	writeTree(t, root, "node_modules/x/y.js", "// dep\n")

	// No profile → built-in spawn args.
	setTestAppConfig(t, &data.AppConfig{Models: []data.ModelConfig{modelCfg("m1", "openai", "http://a")}})

	var spawns []discoverSpawnCall
	prevSpawner := discoverSpawner
	discoverSpawner = func(callerID, model string, args map[string]any) (string, string, error) {
		spawns = append(spawns, discoverSpawnCall{callerID: callerID, model: model, args: args})
		return fmt.Sprintf("sub-test-%d", len(spawns)), model, nil
	}
	defer func() { discoverSpawner = prevSpawner }()

	var task, taskModel string
	prevTaskFn := discoverMainTaskFn
	discoverMainTaskFn = func(channel chan any, t, model string) context.CancelFunc {
		task, taskModel = t, model
		channel <- data.Response{Type: "console", Message: "merge task dispatched"}
		return func() {}
	}
	defer func() { discoverMainTaskFn = prevTaskFn }()

	resp := make(chan any, 16)
	cancel := runDiscoverWithRoot(root, resp, "m1")
	if cancel == nil {
		t.Fatal("expected a non-nil cancel func")
	}

	// Exactly two console messages are expected: the launch summary and the
	// stub task-dispatch line. Drain with a bounded timeout.
	msgs := make([]string, 0, 2)
	for len(msgs) < 2 {
		select {
		case r := <-resp:
			msgs = append(msgs, mustRespMessage(r))
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out draining console messages; have %v", msgs)
		}
	}

	chunks := ChunkTree(func() []ProjectFile {
		f, _ := ScanProjectTree(root)
		return f
	}())
	if len(spawns) != len(chunks) {
		t.Fatalf("spawned %d subagents for %d chunks", len(spawns), len(chunks))
	}

	for i, s := range spawns {
		if s.callerID != "main" {
			t.Errorf("spawn[%d] caller = %q, want main (so the LLM can await it)", i, s.callerID)
		}
		if s.model != "m1" {
			t.Errorf("spawn[%d] model = %q, want m1", i, s.model)
		}
		args := s.args
		if args["mode"] != "stealth" {
			t.Errorf("spawn[%d] mode = %v, want stealth", i, args["mode"])
		}
		if _, has := args["deadlineSeconds"]; has {
			t.Errorf("spawn[%d] must not set a deadline (none configured): %v", i, args["deadlineSeconds"])
		}
		if tools, ok := args["tools"].([]any); !ok || len(tools) != 4 {
			t.Errorf("spawn[%d] tools = %v, want built-in read-only set", i, args["tools"])
		}
		if _, hasProfile := args["profile"]; hasProfile {
			t.Errorf("spawn[%d] should not reference a profile (none configured)", i)
		}
	}

	firstID := fmt.Sprintf("sub-test-%d", 1)
	lastID := fmt.Sprintf("sub-test-%d", len(spawns))
	if !strings.Contains(task, firstID) || !strings.Contains(task, lastID) {
		t.Errorf("merge task missing spawned ids:\n%q", task)
	}
	if !strings.Contains(task, data.ProjectMapStartMarker) {
		t.Error("merge task missing start marker")
	}
	if taskModel != "m1" {
		t.Errorf("task model = %q, want m1", taskModel)
	}

	foundLaunch := false
	for _, msg := range msgs {
		if strings.Contains(msg, "subagent(s) launched") {
			foundLaunch = true
		}
	}
	if !foundLaunch {
		t.Errorf("no launch summary console message: %v", msgs)
	}
}

func TestRunDiscoverWithProfile(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "agent/a.go", "package agent\n")

	cfg := &data.AppConfig{
		Models: []data.ModelConfig{modelCfg("m1", "openai", "http://a"), modelCfg("msmall", "openai", "http://b")},
		System: &data.SystemSettings{DefaultDiscoverySubagent: "disc"},
		Subagents: map[string]data.SubagentProfile{
			"disc": {Name: "disc", AvailableTools: []string{"system-read"}, Model: "msmall", DeadlineSeconds: 90},
		},
	}
	setTestAppConfig(t, cfg)

	var spawns []discoverSpawnCall
	prevSpawner := discoverSpawner
	discoverSpawner = func(callerID, model string, args map[string]any) (string, string, error) {
		spawns = append(spawns, discoverSpawnCall{callerID: callerID, model: model, args: args})
		return "sub-p1", model, nil
	}
	defer func() { discoverSpawner = prevSpawner }()

	prevTaskFn := discoverMainTaskFn
	discoverMainTaskFn = func(channel chan any, t, model string) context.CancelFunc {
		channel <- data.Response{Type: "console", Message: t}
		return func() {}
	}
	defer func() { discoverMainTaskFn = prevTaskFn }()

	resp := make(chan any, 16)
	runDiscoverWithRoot(root, resp, DiscoverModel())

	if len(spawns) != 1 {
		t.Fatalf("want 1 spawn, got %d", len(spawns))
	}
	args := spawns[0].args
	if args["profile"] != "disc" {
		t.Errorf("profile = %v, want disc", args["profile"])
	}
	if d, _ := args["deadlineSeconds"].(int); d != 90 {
		t.Errorf("deadline = %d, want profile's 90", d)
	}
	if tools, ok := args["tools"].([]any); !ok || len(tools) != 1 || tools[0] != "system-read" {
		t.Errorf("profile tools not used: %v", args["tools"])
	}

	// The merge guidance awaits with the profile's deadline. Two messages are
	// queued: the launch summary, then the merge task.
	var taskMsg string
	for i := 0; i < 2; i++ {
		select {
		case r := <-resp:
			if m := mustRespMessage(r); strings.Contains(m, "timeoutSeconds") {
				taskMsg = m
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out draining; task so far: %q", taskMsg)
		}
	}
	if !strings.Contains(taskMsg, "timeoutSeconds 90") {
		t.Errorf("merge task should reference the profile deadline for awaits: %q", taskMsg)
	}
}

func TestRunDiscoverUnknownProfileFailsFast(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "agent/a.go", "package agent\n")

	setTestAppConfig(t, &data.AppConfig{
		System: &data.SystemSettings{DefaultDiscoverySubagent: "ghost"},
	})

	spawned := 0
	prevSpawner := discoverSpawner
	discoverSpawner = func(callerID, model string, args map[string]any) (string, string, error) {
		spawned++
		return "", "", nil
	}
	defer func() { discoverSpawner = prevSpawner }()

	resp := make(chan any, 4)
	cancel := runDiscoverWithRoot(root, resp, "m1")
	if cancel == nil {
		t.Fatal("expected a cancel func even on config error")
	}
	if spawned != 0 {
		t.Fatalf("no subagents may spawn for an unknown profile, spawned %d", spawned)
	}
	msg := mustRespMessage(<-resp)
	if !strings.Contains(msg, "ghost") {
		t.Errorf("error message should name the bad profile: %q", msg)
	}
}

func TestRunDiscoverNoFiles(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "node_modules/x/y.js", "// only deps\n")

	resp := make(chan any, 4)
	runDiscoverWithRoot(root, resp, "m1")
	msg := mustRespMessage(<-resp)
	if !strings.Contains(msg, "no scannable") {
		t.Errorf("expected no-files console error, got %q", msg)
	}
}

func TestBuildDiscoverChunkTaskPrompt(t *testing.T) {
	chunk := ProjectChunk{
		Name:  "agent (go)",
		Files: []ProjectFile{mkDiscoverFile("agent/a.go", 1234), mkDiscoverFile("agent/b.go", 5)},
		Bytes: 1239,
	}
	prompt := BuildDiscoverChunkTaskPrompt(chunk)
	for _, want := range []string{
		"agent (go)", "agent/a.go (1234 bytes)", "agent/b.go (5 bytes)",
		"read-only", "system-subagent_finalize", "Where things live", "Design approach", "Kind of project code",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("chunk prompt missing %q", want)
		}
	}
}

// TestRunDiscoverTaskReturnsBeforeDispatch pins the contract that prevents
// the TUI freeze: RunDiscoverTask must return IMMEDIATELY (the whole pipeline,
// scan -> spawn -> merge task, runs on a background goroutine) even when the
// spawner is mid-flight and no one is reading resp. The old synchronous call
// did its work on the caller's goroutine; with the TUI that was bubbletea's
// single Update loop, so the first blocking send deadlocked every keystroke.
func TestRunDiscoverTaskReturnsBeforeDispatch(t *testing.T) {
	setTestAppConfig(t, &data.AppConfig{Models: []data.ModelConfig{modelCfg("m1", "openai", "http://a")}})

	released := make(chan struct{})
	dispatched := 0
	prevSpawner := discoverSpawner
	discoverSpawner = func(callerID, model string, args map[string]any) (string, string, error) {
		<-released // block here: if the caller is synchronous it never returns
		dispatched++
		return fmt.Sprintf("sub-nb-%d", dispatched), model, nil
	}
	defer func() { discoverSpawner = prevSpawner }()

	prevTaskFn := discoverMainTaskFn
	discoverMainTaskFn = func(channel chan any, task, model string) context.CancelFunc {
		channel <- data.Response{Type: "console", Message: "merge task dispatched"}
		return func() {}
	}
	defer func() { discoverMainTaskFn = prevTaskFn }()

	resp := make(chan any, 16)
	done := make(chan struct{})
	cancel := func() context.CancelFunc {
		cf := RunDiscoverTask(resp, "m1")
		close(done)
		return cf
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunDiscoverTask did not return while the spawner was blocked - it is doing discover work on the caller's goroutine")
	}
	if cancel == nil {
		t.Fatal("expected a non-nil cancel func")
	}

	close(released)
	msgs := 0
	deadline := time.After(2 * time.Second)
	for msgs < 2 {
		select {
		case <-resp:
			msgs++
		case <-deadline:
			t.Fatalf("timed out draining post-dispatch messages, have %d", msgs)
		}
	}
	// RunDiscoverTask scans the real CWD (the repo root), so compare against
	// that tree's chunk count - one spawn per chunk.
	files, err := ScanProjectTree(".")
	if err != nil {
		t.Fatalf("scan for expected-chunk count: %v", err)
	}
	wantDispatches := len(ChunkTree(files))
	if wantDispatches < 1 {
		t.Fatal("expected at least one chunk in the CWD")
	}
	if dispatched != wantDispatches {
		t.Errorf("expected %d chunk subagent dispatch(es), got %d", wantDispatches, dispatched)
	}
}

func mustRespMessage(v any) string {
	r, ok := v.(data.Response)
	if !ok {
		panic(fmt.Sprintf("expected data.Response, got %T", v))
	}
	return r.Message
}

