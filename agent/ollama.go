package agent

import (
	"clankerv2/data"
	"clankerv2/memory"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

type Ollama struct{}

func (ollama *Ollama) Init() {}

// ListModels returns model configs from the config (config-driven, not query-driven)
func (ollama *Ollama) ListModels() ([]data.ModelConfig, error) {
	cfg := GetAppConfig()
	if cfg == nil {
		return nil, fmt.Errorf("app config not loaded")
	}
	return cfg.GetModelsByExecutor("ollama"), nil
}

// Stream drives the Ollama-native turn loop against an already-provisioned
// endpoint. The base URL comes from the model config (or localhost:11434 for
// unmanaged models with no explicit URL) so subagents and other non-UI runs
// work identically to interactive prompts.
func (ollama *Ollama) Stream(ctx context.Context, rt *AgentRuntime, url string, headers map[string]string, payload map[string]any, channel chan any) (string, error) {
	log.Println("Streaming Ollama")

	base := url
	if base == "" {
		base = "http://localhost:11434"
	}
	return runTurnLoop(ctx, base+"/api/chat", rt, channel, ollama, payload)
}

func (ollama *Ollama) processLine(line *string) engineResponse {
	trimmed := strings.TrimSpace(*line)
	if trimmed == "" {
		return engineResponse{success: false}
	}

	var chunk map[string]any
	if err := json.Unmarshal([]byte(trimmed), &chunk); err != nil {
		log.Printf("stream parse error: %v", err)
		return engineResponse{success: false}
	}

	result := engineResponse{success: true}
	var messageBuilder strings.Builder
	var thinkingBuilder strings.Builder

	if msg, ok := chunk["message"].(map[string]any); ok {
		if thinking, ok := msg["thinking"].(string); ok && thinking != "" {
			thinkingBuilder.WriteString(thinking)
		} else if content, ok := msg["content"].(string); ok && content != "" {
			messageBuilder.WriteString(content)
		}
	}

	if messageBuilder.Len() > 0 {
		result.response = messageBuilder.String()
	}

	if thinkingBuilder.Len() > 0 {
		result.thinking = thinkingBuilder.String()
	}

	return result
}

func (ollama *Ollama) ExtractEmbeddings(ctx context.Context, prompt string) ([]float32, error) {
	return nil, fmt.Errorf("ollama embeddings not implemented")
}

func (ollama *Ollama) ExtractNER(ctx context.Context, prompt string) (memory.ExtractionResult, error) {
	return memory.ExtractionResult{}, fmt.Errorf("ollama NER not implemented")
}
