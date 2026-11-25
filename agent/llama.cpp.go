package agent

import (
	"bytes"
	"clankerv2/data"
	"clankerv2/memory"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
)

const (
	LlamaEmbeddingsURL = "http://localhost:8081"
	LlamaNerURL        = "http://localhost:8082"
)

type LlamaCpp struct {
	// Config-driven model definitions
	ModelConfig *data.ModelConfig
}

type model struct {
	ID string `json:"id"`
}

type listModelsResponse struct {
	Data []model `json:"data"`
}

func (llama *LlamaCpp) Init() {
	if llama.ModelConfig == nil {
		log.Println("No model config for llama.cpp, skipping Init")
		return
	}

	args := make([]string, 0, len(llama.ModelConfig.Params))
	args = append(args, llama.ModelConfig.Params...)

	log.Printf("Starting llama.cpp model %q with: %s %v", llama.ModelConfig.ID, llama.ModelConfig.Command, args)

	cmd := exec.Command(llama.ModelConfig.Command, args...)
	cmd.Stdout = nil // redirect or capture as needed
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		log.Printf("Failed to start llama.cpp server: %v", err)
	}
}

func (llama *LlamaCpp) ExtractEmbeddings(ctx context.Context, prompt string) ([]float32, error) {
	payload := map[string]any{"model": "nomic-ai/nomic-embed-text-v2-moe-GGUF", "content": prompt}
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", LlamaEmbeddingsURL+"/v1/embeddings", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("llama embeddings failed: %d, %s", resp.StatusCode, string(body))
	}

	var result []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	embInterface, ok := result[0]["embedding"]
	if !ok {
		return nil, fmt.Errorf("no embedding in response")
	}

	embFloats, ok := embInterface.([]any)
	if !ok {
		return nil, fmt.Errorf("embedding type mismatch")
	}

	embedding := make([]float32, len(embFloats))
	for i, v := range embFloats {
		if f, ok := v.(float64); ok {
			embedding[i] = float32(f)
		}
	}
	return embedding, nil
}

func (llama *LlamaCpp) ExtractNER(ctx context.Context, prompt string) (memory.ExtractionResult, error) {
	messages := make([]map[string]string, 0)

	messages = append(messages, map[string]string{"role": "system", "content": `Extract entities and relationships from the text. Output ONLY valid JSON matching this schema:
{"entities": [{"name": str, "type": str, "confidence": float}], "relationships": [{"source": str, "target": str, "type": str, "weight": float}]}. 
Do not add markdown or explanations.`})

	messages = append(messages, map[string]string{"role": "user", "content": prompt})

	payload := map[string]any{
		"model":           "unsloth/Qwen3.6-35B-A3B-GGUF",
		"messages":        messages,
		"stream":          false,
		"temperature":     0.1,
		"response_format": map[string]string{"type": "json_object"},
	}

	req, _ := http.NewRequestWithContext(ctx, "POST", LlamaNerURL+"/v1/chat/completions", bytes.NewBuffer(mustMarshal(payload)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return memory.ExtractionResult{}, err
	}
	defer resp.Body.Close()

	var llmResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&llmResp); err != nil {
		return memory.ExtractionResult{}, err
	}

	log.Println(string(mustMarshal(llmResp)))

	var out memory.ExtractionResult
	if err := json.Unmarshal([]byte(llmResp.Choices[0].Message.Content), &out); err != nil {
		return memory.ExtractionResult{}, fmt.Errorf("failed to parse LLM JSON: %w", err)
	}
	return out, nil
}

// ListModels returns model configs from the config (config-driven, not query-driven)
func (llama *LlamaCpp) ListModels() ([]data.ModelConfig, error) {
	cfg := GetAppConfig()
	if cfg == nil {
		return nil, fmt.Errorf("app config not loaded")
	}

	return cfg.GetModelsByExecutor("llama.cpp"), nil
}

// GetPort extracts the port from model config params
func (llama *LlamaCpp) GetPort() int {
	if llama.ModelConfig == nil {
		return 8080
	}
	for i, p := range llama.ModelConfig.Params {
		if p == "--port" && i+1 < len(llama.ModelConfig.Params) {
			var port int
			fmt.Sscanf(llama.ModelConfig.Params[i+1], "%d", &port)
			return port
		}
	}
	return 8080
}

// Stream drives the OpenAI-compatible turn loop against an already-provisioned
// llama-server. Executor provisioning happens in RunAgent (via
// EnsureExecutorForRun), which is what makes managed local servers usable from
// subagents and background runs with the same code path as interactive prompts.
func (llama *LlamaCpp) Stream(ctx context.Context, rt *AgentRuntime, url string, headers map[string]string, payload map[string]any, channel chan any) (string, error) {
	log.Println("Streaming llama.cpp")

	if url == "" {
		return "", fmt.Errorf("Oh no!")
	}

	log.Printf("Sending to url: %s", url)
	fullURL := url + "/v1/chat/completions"

	return runTurnLoop(ctx, fullURL, rt, channel, llama, payload)
}

func (llama *LlamaCpp) processLine(line *string) engineResponse {
	return parseOpenAICompatibleLine(line)
}

func processProgress(progress map[string]any) {
	processed := progress["processed"].(int32)
	total := progress["total"].(int32)
	timeMs := progress["time_ms"].(int32)

	percent := float64(processed) / float64(total)

	remaining := (float64(timeMs) / percent) - float64(timeMs)

	log.Printf("Processed %.2f, in %dms (est %.2f seconds remaining)", percent, timeMs, remaining)
}

func mustMarshal(v any) []byte { b, _ := json.Marshal(v); return b }
