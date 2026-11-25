package tools

import (
	"clankerv2/agent/structs"
	"clankerv2/data"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// placeholderArgKey is the one tool-argument name that substitutes VERBATIM as
// a single argv entry (no whitespace splitting): it exists so shell-script
// tools (bash -c) can pass a multi-word script through intact.
const placeholderArgKey = "script"

// paramPlaceholderRe matches $NAME and ${NAME} placeholders in run-block
// param entries. NAME is a tool-argument name; any configured parameter can be
// substituted (not just "parameters").
var paramPlaceholderRe = regexp.MustCompile(`(?:\$\{([A-Za-z][A-Za-z0-9_]*)\}|\$([A-Za-z][A-Za-z0-9_]*))`)

// scriptSentinel marks an EXACT placeholder entry whose value was missing:
// resolveRun swaps in this string and the caller fails closed, because a
// silently-dropped script would make `bash -c ""` exit 0 with no output.
var scriptSentinel = "\x00clanker-script-missing\x00"

// runCommandArgs executes a tool call that has NO built-in handler: any
// configured tool with a run block (custom script runners, plus the
// consolidated system-build / system-tests). A config flag of
// requestPermission gates MODEL invocations on user approval, showing the
// FULLY assembled command (parameters expanded) in the prompt; hook injections
// skip the gate (see HandleInjectedToolCall). Fail closed when a flagged tool
// runs without a requester.
func runCommandArgs(ctx *HandlerCtx, name string) structs.ToolCallResult {
	run, err := runForTool(name, ctx.args)
	if err != nil {
		return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "unknown tool"}}
	}
	if !ctx.injected && permissionGated != nil && permissionGated(name) {
		if ctx.requestPermission == nil {
			return structs.ToolCallResult{Success: false, Result: map[string]any{"error": fmt.Sprintf("tool %s requires user approval but no UI is connected", name)}}
		}
		cmd := commandDisplay(run.Command, run.Params)
		// The assembled command goes to the content pane (it can be very long),
		// not the prompt label: the label stays short and descriptive.
		permResult := ctx.requestPermission(ctx.channel, name, "", "execute",
			name+" requests permission to run a command",
			&data.PermissionViewport{Kind: "script", Title: name + " command", Content: cmd})
		if strings.Contains(permResult, "denied") {
			return structs.ToolCallResult{Success: false, Result: map[string]any{"result": permResult}}
		}
	}
	// 0 = inherit the default CommandTimeout; a run-block timeout_seconds
	// (e.g. the tests tool's 30s cap) overrides it per tool.
	var timeout time.Duration
	if run.TimeoutSeconds > 0 {
		timeout = time.Duration(run.TimeoutSeconds) * time.Second
	}
	// A missing/blank script would make `bash -c ""` exit 0 with no output —
	// fail closed so the model sees the error instead of a silent success.
	for _, param := range run.Params {
		if param == scriptSentinel {
			return structs.ToolCallResult{Success: false, Result: map[string]any{"error": "tool requires a non-empty `script` argument (the full shell command to run); none was provided"}}
		}
	}
	result := RunCommandArgs(run.Command, run.Params, ".", timeout)
	return structs.ToolCallResult{Success: result.Status == "success", Result: toResultMap(result)}
}

// runForTool resolves the argv invocation a command-style tool will execute:
// from the merged config's run block when available, or from the built-in
// defaults when no config is loaded (tests, headless). The returned spec keeps
// TimeoutSeconds so the caller can apply per-tool caps.
func runForTool(name string, args map[string]any) (*data.ToolRun, error) {
	var run *data.ToolRun
	if commandSource != nil {
		run = commandSource(name)
	}
	if run == nil {
		// No config loaded (tests, headless): built-in defaults so the tools
		// stay usable without a config.
		switch name {
		case "system-build":
			run = &data.ToolRun{Command: "make", Params: []string{"build"}}
		case "system-tests":
			// 30s cap preserved from the pre-consolidation handler so a hanging
			// suite fails fast even with no config loaded.
			run = &data.ToolRun{Command: "go", Params: []string{"test", "-v", "${PARAMETERS}"}, TimeoutSeconds: 30}
		default:
			return nil, fmt.Errorf("no run block is configured for %s", name)
		}
	}
	return resolveRun(name, run, args)
}

// resolveRun expands a tool's argv spec into the exact command+args to exec.
// A $NAME / ${NAME} param entry resolves against the tool's arguments (any
// parameter name, case-insensitive fallback). Value shapes:
//   - array  -> each element becomes its own argv word, verbatim, as given;
//     scalar elements are stringified (numbers, booleans)
//   - string -> whitespace-split into argv words (legacy behavior), EXCEPT a
//     parameter literally named `script`, which substitutes VERBATIM as one
//     argv entry — that is the shell-script slot (bash -c) and its value is
//     parsed by the target interpreter, never by the harness
//
// An exact-placeholder entry whose argument is missing drops out of the argv
// (so a plain `make build` spec runs with its static params only), except when
// the parameter is `script`: it swaps in scriptSentinel so the caller can fail
// closed. Embedded placeholders pass through untouched when their argument is
// missing. There is no shell between harness and target (direct execve), so no
// escaping is applied anywhere. TimeoutSeconds passes through.
func resolveRun(tool string, run *data.ToolRun, args map[string]any) (*data.ToolRun, error) {
	if run == nil || run.Command == "" {
		return nil, fmt.Errorf("no run block is configured for %s", tool)
	}

	outParams := make([]string, 0, len(run.Params))
	for _, p := range run.Params {
		matches := paramPlaceholderRe.FindAllStringSubmatchIndex(p, -1)
		if len(matches) == 0 {
			outParams = append(outParams, p)
			continue
		}
		isExact := len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(p)
		if isExact {
			name := placeholderName(p, matches[0])
			v, ok := args[name]
			if !ok {
				for k, val := range args {
					if strings.EqualFold(k, name) {
						v, ok = val, true
						break
					}
				}
			}
			if !ok {
				if strings.EqualFold(name, placeholderArgKey) {
					outParams = append(outParams, scriptSentinel)
				} // else: drop the entry (legacy no-params behavior)
				continue
			}
			switch val := v.(type) {
			case []any:
				if len(val) == 0 {
					continue // an empty array contributes nothing; drop the entry
				}
				for _, e := range val {
					outParams = append(outParams, scalarString(e))
				}
			case string:
				if strings.EqualFold(name, placeholderArgKey) {
					if strings.TrimSpace(val) == "" {
						outParams = append(outParams, scriptSentinel)
					} else {
						outParams = append(outParams, val) // verbatim: it IS the script
					}
				} else if strings.TrimSpace(val) == "" {
					continue // no caller parameters: drop the placeholder entry
				} else {
					outParams = append(outParams, strings.Fields(val)...)
				}
			default:
				s := scalarString(v)
				if s != "" {
					outParams = append(outParams, s)
				}
			}
			continue
		}

		// Embedded placeholder(s): substituted in place; the entry stays a
		// single argv element. A missing argument leaves the entry untouched.
		present := true
		for _, m := range matches {
			name := placeholderName(p, m)
			if !argHasValue(args, name) {
				present = false
				break
			}
		}
		if !present {
			outParams = append(outParams, p)
			continue
		}
		var b strings.Builder
		last := 0
		for _, m := range matches {
			b.WriteString(p[last:m[0]])
			name := placeholderName(p, m)
			v, _ := argString(args, name)
			if strings.EqualFold(name, placeholderArgKey) {
				b.WriteString(v) // verbatim script slot
			} else {
				b.WriteString(strings.Join(strings.Fields(v), " "))
			}
			last = m[1]
		}
		b.WriteString(p[last:])
		outParams = append(outParams, b.String())
	}
	return &data.ToolRun{Command: run.Command, Params: outParams, TimeoutSeconds: run.TimeoutSeconds}, nil
}

// placeholderName extracts the argument name from one regex match.
func placeholderName(p string, m []int) string {
	if n := p[m[2]:m[3]]; n != "" {
		return n
	}
	return p[m[4]:m[5]]
}

// argHasValue reports whether the named argument (case-insensitive fallback)
// is present and non-empty.
func argHasValue(args map[string]any, name string) bool {
	v, ok := argString(args, name)
	return ok && strings.TrimSpace(v) != ""
}

// argString returns the named argument as a string (case-insensitive fallback).
func argString(args map[string]any, name string) (string, bool) {
	if v, ok := args[name].(string); ok {
		return v, true
	}
	for k, val := range args {
		if strings.EqualFold(k, name) {
			if s, ok := val.(string); ok {
				return s, true
			}
		}
	}
	return "", false
}

// scalarString renders a JSON array element as its argv word: strings pass
// through, numbers and booleans stringify, anything else is rejected by the
// caller's schema (defensive empty string here).
func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	default:
		return ""
	}
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
		return 0, false
	}
	return 0, false
}

// strField reads an optional string field from a parsed change object: JSON
// null and absence both yield "" (the empty-string = file-edge convention for
// anchor blocks). A present non-string value is a shape error.
func strField(c map[string]any, key string) (string, bool) {
	v, ok := c[key]
	if !ok || v == nil {
		return "", true
	}
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func toResultMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{"error": err.Error()}
	}
	return m
}
