package agent

import (
	"clankerv2/agent/structs"
	"clankerv2/data"
	"clankerv2/memory"
	"context"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

type OpenAIEngine struct {
	ModelConfig *data.ModelConfig
}

func (o *OpenAIEngine) Init() {}

func (o *OpenAIEngine) ExtractEmbeddings(ctx context.Context, prompt string) ([]float32, error) {
	// Not implemented for now as per instructions focusing on chat/stream engine.
	return nil, nil
}

func (o *OpenAIEngine) ExtractNER(ctx context.Context, prompt string) (memory.ExtractionResult, error) {
	return memory.ExtractionResult{}, nil
}

// ListModels returns model configs from the config (config-driven, not query-driven)
func (o *OpenAIEngine) ListModels() ([]data.ModelConfig, error) {
	cfg := GetAppConfig()
	if cfg == nil {
		return nil, fmt.Errorf("app config not loaded")
	}
	return cfg.GetModelsByExecutor("openai"), nil
}

// Stream drives the OpenAI-compatible turn loop against an already-provisioned
// endpoint. Executor provisioning happens in RunAgent (via
// EnsureExecutorForRun), so every engine — and any future one — shares the
// same launch path; this method only handles the dialect-specific stream.
func (o *OpenAIEngine) Stream(ctx context.Context, rt *AgentRuntime, url string, headers map[string]string, payload map[string]any, channel chan any) (string, error) {
	return runTurnLoop(ctx, url+"/v1/chat/completions", rt, channel, o, payload)
}

func (o *OpenAIEngine) processLine(line *string) engineResponse {
	return parseOpenAICompatibleLine(line)
}

// parseOpenAICompatibleLine handles the standard OpenAI SSE format used by multiple engines.
func parseOpenAICompatibleLine(line *string) engineResponse {
	if line == nil || *line == ":" || *line == "" {
		return engineResponse{success: false}
	}

	trimmed := strings.TrimPrefix(*line, "data:")
	result := engineResponse{success: true}

	// Parse usage metrics if present in the chunk
	usageJSON := gjson.Get(trimmed, "usage")
	if usageJSON.Exists() {
		result.usage = &data.Usage{
			PromptTokens:     int(usageJSON.Get("prompt_tokens").Int()),
			CompletionTokens: int(usageJSON.Get("completion_tokens").Int()),
			TotalTokens:      int(usageJSON.Get("total_tokens").Int()),
		}
	}

	var messageBuilder strings.Builder
	var thinkingBuilder strings.Builder

	deltas := gjson.Get(trimmed, "choices.#.delta")
	deltas.ForEach(func(_, value gjson.Result) bool {
		content := value.Get("content")
		if content.Exists() {
			messageBuilder.WriteString(content.String())
		}

		// Some providers use reasoning_content for thinking/reasoning models (like DeepSeek)
		content = value.Get("reasoning_content")
		if content.Exists() {
			thinkingBuilder.WriteString(content.String())
		}

		// Process tool calls delta
		toolCalls := value.Get("tool_calls")
		if toolCalls.Exists() {
			toolCalls.ForEach(func(_, tc gjson.Result) bool {
				index := tc.Get("index").Int()
				id := tc.Get("id").String()
				tcType := tc.Get("type").String()
				fnName := tc.Get("function.name").String()
				fnArgs := tc.Get("function.arguments").String()

				result.toolCallChunk = &structs.IncomingToolCall{
					Index:     int(index),
					ID:        id,
					Type:      tcType,
					Name:      fnName,
					Arguments: fnArgs,
				}

				return true
			})
		}

		return true
	})

	if messageBuilder.Len() > 0 {
		result.response = messageBuilder.String()
	}

	if thinkingBuilder.Len() > 0 {
		result.thinking = thinkingBuilder.String()
	}

	return result
}
