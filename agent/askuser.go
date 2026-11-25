package agent

import (
	"clankerv2/data"
	"encoding/json"
	"fmt"
	"log"
)

// AskUser sends a system-ask form to the UI and blocks until it is answered or
// cancelled. The returned string is the LLM-facing tool result JSON:
//   - {"status":"success","answers":{"<question id>":"<value>"}}
//   - {"status":"cancelled"}
//   - {"status":"error","error":"..."} when validation fails before display
//
// Validation stays here (the question schema is ask-specific); the emit, the
// block, and the resolve/cancel exits all run through the shared prompt
// registry (prompts.go), the same mechanism permission requests use.
func AskUser(channel chan any, rawQuestions []any) string {
	if len(rawQuestions) == 0 {
		return askErrorJSON("questions must be a non-empty array")
	}

	questions := make([]data.AskUserQuestion, 0, len(rawQuestions))
	for i, raw := range rawQuestions {
		q, ok := raw.(map[string]any)
		if !ok {
			return askErrorJSON(fmt.Sprintf("question %d is not an object", i+1))
		}

		id, _ := q["id"].(string)
		qtype, _ := q["type"].(string)
		prompt, _ := q["prompt"].(string)
		if id == "" || prompt == "" {
			return askErrorJSON(fmt.Sprintf("question %d is missing required field 'id' or 'prompt'", i+1))
		}

		var options []data.AskOption
		if rawOpts, present := q["options"]; present && rawOpts != nil {
			arr, ok := rawOpts.([]any)
			if !ok {
				return askErrorJSON(fmt.Sprintf("question %q: 'options' must be an array", id))
			}
			options = make([]data.AskOption, 0, len(arr))
			for j, ro := range arr {
				obj, ok := ro.(map[string]any)
				if !ok {
					return askErrorJSON(fmt.Sprintf("question %q: option %d is not an object", id, j+1))
				}
				label, _ := obj["label"].(string)
				value, _ := obj["value"].(string)
				style, _ := obj["style"].(string)
				if label == "" || value == "" {
					return askErrorJSON(fmt.Sprintf("question %q: option %d requires 'label' and 'value'", id, j+1))
				}
				options = append(options, data.AskOption{Label: label, Value: value, Style: style})
			}
		}

		switch qtype {
		case "confirm", "select", "multi-select":
			if len(options) == 0 {
				return askErrorJSON(fmt.Sprintf("question %q: type %q requires at least one option", id, qtype))
			}
		case "free-response":
			// No options needed; placeholder may be empty.
		default:
			return askErrorJSON(fmt.Sprintf("question %q: unknown type %q (want confirm, select, multi-select, or free-response)", id, qtype))
		}

		context, _ := q["context"].(string)
		placeholder, _ := q["placeholder"].(string)
		// allow_free_form defaults to TRUE: every select / multi-select
		// question gets the extra free-text row unless the model EXPLICITLY
		// opts out (present key with a non-true value, e.g. false or null).
		allowFreeForm := true
		if rawFF, present := q["allow_free_form"]; present {
			b, _ := rawFF.(bool)
			allowFreeForm = b
		}

		questions = append(questions, data.AskUserQuestion{
			ID:            id,
			Type:          qtype,
			Prompt:        prompt,
			Context:       context,
			Placeholder:   placeholder,
			Options:       options,
			AllowFreeForm: allowFreeForm,
		})
	}

	if promptPendingCount(PromptAsk) > 0 {
		return askErrorJSON("another system-ask form is already waiting for a response")
	}

	id := nextPromptID(PromptAsk)

	log.Printf("system-ask: presenting %d question(s) as %s", len(questions), id)

	// Emit + block on the shared registry until the UI resolves or cancels.
	resultCh := presentPrompt(PromptAsk, id, channel, data.AskUserRequest{ID: id, Questions: questions})
	raw := <-resultCh
	respondState(channel, "executing")
	result, _ := raw.(string)
	return result
}

// askErrorJSON builds a compact error result for the LLM.
func askErrorJSON(msg string) string {
	b, err := json.Marshal(map[string]any{"status": "error", "error": msg})
	if err != nil {
		return `{"status":"error","error":"internal error"}`
	}
	return string(b)
}
