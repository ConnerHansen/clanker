// Package tools implements the agent's built-in tool set (file reads and
// writes, codebase search, content-anchored edits, command execution) plus the
// registry dispatch that routes every model- or hook-requested call to the
// right handler.
//
// Layout:
//   - handler.go      — public entry points (HandleToolCall / HandleInjectedToolCall) and registry dispatch
//   - registry.go     — HandlerCtx + ToolHandler registration
//   - injection.go    — interfaces and DI seams provided by the agent package
//   - tool_*.go       — one file per built-in tool's handler
//   - command_exec.go — fallback executor for configured (run-block) tools
//   - read.go / find.go / list.go / search_name.go / modify.go / write.go /
//     diff.go / command.go — the underlying implementations and result types
//   - constants.go    — safety caps shared across tools
package tools
