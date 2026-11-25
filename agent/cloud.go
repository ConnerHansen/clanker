package agent

import (
	"clankerv2/data"
	"clankerv2/memory"
	"context"
	"fmt"
	"log"
)

type Cloud struct {
	ModelConfig *data.ModelConfig
}

func (c *Cloud) Init() {}

func (c *Cloud) ExtractEmbeddings(ctx context.Context, prompt string) ([]float32, error) {
	// Not implemented for now as per instructions focusing on chat/stream engine.
	return nil, nil
}

func (c *Cloud) ExtractNER(ctx context.Context, prompt string) (memory.ExtractionResult, error) {
	return memory.ExtractionResult{}, nil
}

func (c *Cloud) ListModels() ([]data.ModelConfig, error) {
	// Implementation will return models with executor 'cloud' from config via GetAppConfig().
	cfg := GetAppConfig()
	if cfg == nil {
		return nil, fmt.Errorf("app config not loaded")
	}
	return cfg.GetModelsByExecutor("cloud"), nil
}

// Stream drives the OpenAI-compatible turn loop against an already-provisioned
// endpoint. Executor provisioning happens in RunAgent (via
// EnsureExecutorForRun), so Cloud models share the exact same launch
// path as local servers — including background runs like subagents and the
// summarizer. Only the completion path differs from OpenAI (no /v1 prefix).
func (c *Cloud) Stream(ctx context.Context, rt *AgentRuntime, url string, headers map[string]string, payload map[string]any, channel chan any) (string, error) {
	log.Printf("Streaming via Cloud: %s", rt.ModelID)

	fullURL := url + "/chat/completions"
	return runTurnLoop(ctx, fullURL, rt, channel, c, payload)
}

func (c *Cloud) processLine(line *string) engineResponse {
	return parseOpenAICompatibleLine(line)
}
