package data

import "strings"

// CanonicalToolName maps a tool or skill name to its OpenAI-conformant form:
// dotted names (system.read) become dashed (system-read). Idempotent — names
// without dots pass through unchanged.
func CanonicalToolName(name string) string {
	if !strings.Contains(name, ".") {
		return name
	}
	return strings.ReplaceAll(name, ".", "-")
}
