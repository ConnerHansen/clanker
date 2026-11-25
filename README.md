# Clankerv2

Clankerv2 is an AI-powered agent framework with a hybrid TUI/GUI interface. It allows an LLM to interact directly with a local codebase, executing tools and modifying files through a secure, permission-based system.

## How it Works

Clankerv2 operates as a loop between an LLM (via OpenAI-compatible APIs) and a set of local tools.

- **Tool Execution**: The agent can read files, search the codebase, and apply precise changes.
- **Permission System**: File modifications (`system-write`, `system-modify`, `system-delete`) and command executions (`bash-cmd`, `go-cmd`, and any tool flagged `requestPermission` in its `tool.json`) are intercepted by a TUI/GUI permission prompt. This creates a blocking request in the agent loop that requires explicit user approval via the interface before the change is committed to disk. The prompt's descriptor and coloration come from the operation: creating files renders blue (info), overwriting/updating gold (warning), deleting red (danger). Executing a command renders with the gold warning treatment (same as an overwrite) and shows the fully assembled command in the content pane for review before approval.
- **Hybrid Interface**: It provides both a rich desktop GUI via Wails and a streamlined TUI for power users.
- **Validation**: It includes a validation hook that, after file-modifying turns, injects a build/test check (e.g. `go build`) so AI-generated changes don't break the project's build integrity.

## Setup

1. Install dependencies using `mise`:

   ```bash
   mise install
   ```

2. Ubuntu-specific dependencies:

   ```bash
   sudo apt install libwebkit2gtk-4.1-dev
   ```

3. Install Wails CLI:

   ```bash
   go install github.com/wailsapp/wails/v2/cmd/wails@latest
   ```

4. (Optional, for managed local models) Start the backing services — Memgraph and ChromaDB via `make start.env` — then configure and start your llama.cpp model servers. Model definitions live in `.clanker/models.json` (see `examples/models.json`).

## Makefile Targets

The project uses a `Makefile` to simplify common development tasks:

| Target           | Description                                             |
|------------------|---------------------------------------------------------|
| `make dev`       | Start the Wails desktop app in live dev mode.           |
| `make dev.cli`   | Start the app in CLI-only (TUI) mode for development.   |
| `make build`     | Build a production binary for Linux (amd64).            |
| `make tests`     | Run the full test suite.                                |
| `make clean`     | Remove the build output directory.                      |
| `make install`   | Build, then copy the binary to `~/.local/bin`.          |
| `make env.start` | Start Docker Compose services (Memgraph + ChromaDB).    |
| `make env.stop`  | Stops Docker Compose services                           |

## Live Development

To run in live development mode, run `make dev`. This launches the Wails desktop app with hot reloading enabled.

If you prefer CLI-only mode, use `make dev.cli` to launch the TUI.

## Building

To build a production-ready desktop package:

```bash
make build
```

This compiles a redistributable Linux (amd64) binary using Wails.

## Testing

To run the project's test suite and ensure that tool logic (like file modification and diffing) is working correctly, run:

```bash
make tests
```

This will execute all unit tests across the codebase, including the critical simulation and diffing logic in the `agent` package.

## Configuration

### Supported Tools

The following tools are currently supported and available for the agent to use:

| Tool | Description |
|------|-------------|
| `system-ask` | Used for asking the user questions about the current session |
| `system-read` | Reads file contents with line-number prefixes and line-range extraction |
| `system-find` | Searches codebase using regular expressions |
| `system-list` | Lists files and directories within a specified path |
| `system-search_by_name` | Finds files by name or extension using glob patterns |
| `system-write` | Creates or overwrites a file with provided content |
| `system-delete` | Deletes a single file (danger-level red permission prompt; directories rejected) |
| `system-modify` | Applies content-anchored edits via two shapes: `replace` (find exact block once, swap) and `anchor` (grab everything from start to end block inclusive, replace with new); whitespace-tolerant matching, no line numbers |
| `system-register_plan` | Commits an agreed-upon execution plan for completion verification at turn end |
| `system-check_plan` | Internal post-step (not in the model's tool set) — serves the plan-check hook |
| `system-compact` / `system-register_compaction` | Context compaction: `system-compact` begins an in-flight compaction (manual, or auto-injected when usage crosses 4/5 of the window); `system-register_compaction` finalizes it with the summary text and re-seeds the conversation |
| `system-skills` | Loads a skill's SKILL.md instructions by name; skills flagged `subagent: true` instead spawn a background stealth subagent and block until it finishes |
| `system-spawn_subagent` | Launches a background subagent (non-blocking; returns an id to track) |
| `system-await_subagent` | Blocks until a subagent finishes and returns its output or death report |
| `system-subagent_status` | Lists your subagents: status, elapsed time, last activity, model, session |
| `system-kill_subagent` | Aborts a queued or running subagent; reports killed with last activity |
| `system-subagent_finalize` | Always available to subagents; ends the run and delivers a final summary |

Project-defined **command tools** (see below) are also passed to the agent when present in the active config layer:

| Tool | Description |
|------|-------------|
| `bash-cmd` | Runs an arbitrary Bash command via `bash -c` (permission-gated by default in most projects) |
| `go-cmd` | Runs an arbitrary Go command, argv words passed verbatim (permission-gated by default in most projects) |
| `go-fmt` | Runs the Golang formatter on file/package paths |
| `system-build` | Runs the project's build command (run block, e.g. `make build`). Ungated and uncapped by default; opt into `requestPermission` and `timeout_seconds` in its `tool.json` |
| `system-tests` | Runs the project's test command (e.g. `go test -v ${PARAMETERS}`, 30s cap in the local project). Gated only if `requestPermission` is set in its `tool.json` |

### Subagents

The main agent can delegate work to background **subagents** that run concurrently on their own conversations (own messages, tools, and optionally their own persisted session). Key behaviors:

- **Concurrency caps**: subagents are capped separately by model class, so a blocked managed run never starves unmanaged ones. `system.subagentMaxManaged` (local/own-process models) defaults to **1**; `system.subagentMaxUnmanaged` (remote/hosted) defaults to **5**. Spawns never fail when at the cap — extra subagents queue FIFO and promote as slots free up. The fixed deadline timer starts at *execution*, not queuing.
- **Model inheritance**: omitting `model` (or passing null) inherits the calling agent's currently-selected model. A subagent whose model is already running **joins that executor** — no second model server ever starts for an already-live model; a different model gets its own private executor, torn down when it finishes.
- **Tool filtering**: each subagent gets an explicit tool allowlist (a profile or `tools` argument); calls to anything outside the set are rejected. Orchestration tools (spawn/await/status/kill) are never granted to children; `system-subagent_finalize` is always available so every subagent can end its run and deliver a summary.
- **Profiles**: named presets under `"subagents"` in `.clanker/config.json` set default tools, prompt, model, and deadline:

  ```json
  "subagents": {
    "readonly": {
      "availableTools": ["system-read", "system-find", "system-list", "system-search_by_name"],
      "prompt": "You are a read-only research subagent. Never attempt modifications.",
      "model": null,
      "deadlineSeconds": 600
    }
  }
  ```

  `spawn_subagent {profile: "readonly"}` inherits the preset; explicit spawn arguments override it. A null/empty `model` in a profile inherits the caller's model the same way.
- **Persistence**: `mode: "persistent"` creates a real session row linked to the calling conversation via `parent_session_id`, so subagent work appears in the session list and audit trail; `stealth` (default) writes nothing to the DB.
- **The summarizer** reuses the same machinery: `system.defaultSummarizer` may be null/empty, in which case it falls back to the model currently selected in the main conversation (reusing its warm executor), then to `system.defaultModel`.
- **`/discover`** walks the project tree (skipping dependency/build dirs like `node_modules`, `deps`, `dist`) and splits it into logical chunks by top-level directory + language family; tiny adjacent groups are merged up to a ~5MB budget so each subagent covers more of the tree, and oversized groups split into part N/M. One read-only stealth subagent per chunk summarizes what logic lives where and how the code is designed. The main agent then collects the summaries via `system-await_subagent` — seeing only summaries, never raw code — and merges them into a marked section (`<!-- project-map:start/end -->`) of `.clanker/project.md`, replacing only that section on re-runs so hand-written knowledge survives. That file is always appended to the effective system prompt after `system.md`. Subagent launch uses the `system.defaultDiscoverySubagent` profile (tools/prompt/model/deadline) when set; its model, then `system.defaultSummarizer`, then the main conversation's model decide which model runs it.

  ```json
  "subagents": {
    "discovery": {
      "availableTools": ["system-read", "system-find", "system-list", "system-search_by_name"],
      "prompt": "You are a read-only code-exploration subagent. Never attempt modifications.",
      "model": "qwen3.6-35b-mtp",
      "deadlineSeconds": 3600
    }
  },
  "system": { "defaultDiscoverySubagent": "discovery" }
  ```
- **Config tunables** under `system` (in `.clanker/config.json`): `subagentMaxManaged` (default 1), `subagentMaxUnmanaged` (default 5), `discoverFilesPerChunk` (default 100), `discoverMaxAgents` (default 4), `discoverMaxDepth` (default 32), and `read.numbered` (default true) — plus `defaultModel`, `defaultSummarizer`, `defaultDiscoverySubagent`.

### Configuration (layered)

Clanker reads configuration from two layers, **global first**, with the project-local layer clobbering or appending on top:

| Piece | Global | Local | Merge rule |
|-------|--------|-------|------------|
| App config + subagent profiles | `~/.clanker/config.json` (or `$CLANKER_HOME/config.json`) | `.clanker/config.json` | Shallow: a top-level key present locally replaces the whole global section; absent sections inherit |
| Models | `models.json` in each layer | same | Identity-merged by `id`: colliding ids are replaced wholesale, new ids append |
| Tools | `tools.json` (bare array) + `tools/**/tool.json` dirs | same | Identity-merged by tool name: local definitions replace global ones of the same name; others append |
| Skills | `skills/**/SKILL.md` in each layer | same | Identity-merged by skill name: local skills replace global ones of the same name |
| System prompt | `system.md` in each layer | `.clanker/system.md` | Local file REPLACES global entirely when present (no concatenation) |
| Project extras + project map | — | `.clanker/project.md` | Always APPENDED after the effective system prompt; `/discover` rewrites only its marked section |

Each `tools/**/tool.json` is one OpenAI-style function schema plus a `run` block (argv, executed without a shell):

```json
{
  "type": "function",
  "function": {
    "name": "some.tool",
    "description": "Some tool"
  },
  "run": {
    "command": "python",
    "params": ["script.py"]
  }
}
```

`${NAME}` entries in `params` resolve against the caller's arguments at call time: an array argument passes each element verbatim as its own argv word; a scalar string is whitespace-split (legacy behavior), except a parameter literally named `script`, which substitutes verbatim as one argv entry — that is the shell-script slot (`bash -c`). An exact-placeholder entry whose argument is missing is dropped from the argv entirely (so a static `make build` spec runs with its static params only); `script` instead swaps in a sentinel so the caller fails closed rather than running an empty command. An optional `"timeout_seconds"` in the run block caps one execution (absent/0 = the default, 5 minutes).

A top-level `"requestPermission": true` in a tool's JSON gates that tool on a user approval prompt per run — the assembled, fully-expanded command is shown before approval. A `"timeout_seconds"` in the run block caps one execution.

All files are optional; missing layers simply contribute nothing. Malformed JSON in a PRESENT file is fatal at boot.

### Project Configuration

Project configuration lives in `.clanker/`: `config.json` (models, subagents, system defaults), `models.json`, `tools.json` + `tools/**`, `skills/**`, `contexts.json` + `contexts/**` (prompt bundles), `system.md`, and `project.md`. The `wails.json` file is App-level metadata (window title, frontend build steps), not the app's project configuration — see https://wails.io/docs/reference/project-config for its schema.

## Getting Started

Globally shared configurations live in `~/.clanker/` — `config.json` and `models.json` will be loaded by default. Any `.clanker/config.json` or `.clanker/models.json` changes will be layered on top.

The local `.clanker/tools` and `.clanker/skills` folders will get read in at runtime. Tools will be passed to the agent, and skills will be made available both to the agent (via `system-skills`) and to the user as invocable slash commands (`/<skill-name>`). Skills with `subagent: true` in their frontmatter run in a background stealth subagent instead of inline on the invoking agent's context.
