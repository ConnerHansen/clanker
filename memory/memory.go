package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// --- Data Structures ---
type Entity struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"` // e.g., "file", "func", "module"
	Confidence float64 `json:"confidence"`
}

type Relationship struct {
	Source string  `json:"source"`
	Target string  `json:"target"`
	Type   string  `json:"type"`
	Weight float64 `json:"weight"` // 0.0-1.0 confidence/strength
}

type ExtractionResult struct {
	RawText       string         `json:"raw_text"`
	Entities      []Entity       `json:"entities"`
	Relationships []Relationship `json:"relationships"`
	// Embedding is generated locally, not sent over the wire in JSON
	Embedding []float32 `json:"-"`
}

// --- Database Configuration ---

const (
	MemgraphURI        = "bolt://localhost:7687"
	ChromaDBURL        = "http://localhost:8000"
	LlamaEmbeddingsURL = "http://localhost:8081"
	EmbeddingModel     = "nomic-embed-text" // Highly recommended for local semantic search
	ChromaTenantName   = "default"
	ChromaDBName       = "default"
	ChromaCollName     = "agent_memory"
)

// --- Implementation ---

type MemoryManager struct {
	GraphDriver neo4j.DriverWithContext
	HTTPClient  *http.Client
}

// TestConnections checks if both backends are reachable using v2 endpoints
func (mm *MemoryManager) TestConnections(ctx context.Context) error {
	// 1. Test Memgraph
	fmt.Println("🔍 Testing Memgraph connection...")
	session := mm.GraphDriver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	_, err := session.Run(ctx, "RETURN 1", nil)
	if err != nil {
		return fmt.Errorf("memgraph query failed: %w", err)
	}
	fmt.Println("✅ Memgraph is reachable.")

	// 2. Test ChromaDB using v2 heartbeat
	fmt.Println("🔍 Testing ChromaDB (v2) connection...")
	resp, err := mm.HTTPClient.Get(ChromaDBURL + "/api/v2/heartbeat")
	if err != nil {
		return fmt.Errorf("chromadb connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("chromadb (v2) returned non-200 status: %d", resp.StatusCode)
	}
	fmt.Println("✅ ChromaDB (v2) is reachable.")

	return nil
}

// UpdateGraph performs the Cypher MERGE operations
func (mm *MemoryManager) UpdateGraph(ctx context.Context, data ExtractionResult) error {
	fmt.Printf("Graph: Processing %d entities and %d relationships...\n", len(data.Entities), len(data.Relationships))

	session := mm.GraphDriver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)

	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		// 1. Upsert Entities
		for _, ent := range data.Entities {
			query := `MERGE (e:Entity {name: $name}) SET e.type = $type RETURN e`
			params := map[string]any{"name": ent.Name, "type": ent.Type}
			if _, err := tx.Run(ctx, query, params); err != nil {
				return nil, err
			}
		}

		// 2. Upsert Relationships
		for _, rel := range data.Relationships {
			query := `
				MATCH (s:Entity {name: $source})
				MATCH (t:Entity {name: $target})
				MERGE (s)-[r:RELATION {type: $relType}]->(t)
				RETURN r`
			params := map[string]any{
				"source":  rel.Source,
				"target":  rel.Target,
				"relType": rel.Type,
			}
			if _, err := tx.Run(ctx, query, params); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})

	return err
}

// ensureTenant creates the tenant if it doesn't exist.
func (mm *MemoryManager) ensureTenant(ctx context.Context) error {
	url := fmt.Sprintf("%s/api/v2/tenants", ChromaDBURL)
	payload := map[string]string{"name": ChromaTenantName}
	jsonPayload, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := mm.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create tenant: %d, %s", resp.StatusCode, string(body))
	}
	fmt.Println("✅ Tenant verified/created.")
	return nil
}

// ensureDatabase creates the database under the tenant if it doesn't exist.
func (mm *MemoryManager) ensureDatabase(ctx context.Context) error {
	url := fmt.Sprintf("%s/api/v2/tenants/%s/databases", ChromaDBURL, ChromaTenantName)
	payload := map[string]string{"name": ChromaDBName}
	jsonPayload, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := mm.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create database: %d, %s", resp.StatusCode, string(body))
	}
	fmt.Println("✅ Database verified/created.")
	return nil
}

// ensureCollection creates the collection and returns its ID.
func (mm *MemoryManager) ensureCollection(ctx context.Context) (string, error) {
	url := fmt.Sprintf("%s/api/v2/tenants/%s/databases/%s/collections", ChromaDBURL, ChromaTenantName, ChromaDBName)
	payload := map[string]string{"name": ChromaCollName}
	jsonPayload, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := mm.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var response map[string]any
	if resp.StatusCode == http.StatusConflict {
		// If it already exists, find its ID by listing collections
		listURL := fmt.Sprintf("%s/api/v2/tenants/%s/databases/%s/collections", ChromaDBURL, ChromaTenantName, ChromaDBName)
		listReq, _ := http.NewRequestWithContext(ctx, "GET", listURL, nil)
		listResp, err := mm.HTTPClient.Do(listReq)
		if err != nil {
			return "", err
		}
		defer listResp.Body.Close()

		var collections []map[string]any
		if err := json.NewDecoder(listResp.Body).Decode(&collections); err != nil {
			return "", err
		}

		for _, col := range collections {
			if col["name"] == ChromaCollName {
				return col["id"].(string), nil
			}
		}
		return "", fmt.Errorf("collection '%s' not found after 409 conflict", ChromaCollName)
	}

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to create collection: %d, %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", err
	}

	collID, ok := response["id"].(string)
	if !ok {
		return "", fmt.Errorf("collection ID not found in response")
	}

	fmt.Println("✅ Collection verified/created.")
	return collID, nil
}

// generateEmbedding calls local llama to produce a semantic vector for the text
func (mm *MemoryManager) generateEmbedding(ctx context.Context, text string) ([]float32, error) {
	payload := map[string]any{"model": EmbeddingModel, "content": text}
	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", LlamaEmbeddingsURL+"/v1/embeddings", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := mm.HTTPClient.Do(req)
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

// UpdateVector orchestrates the v2 resource setup, embedding generation, and record addition
func (mm *MemoryManager) UpdateVector(ctx context.Context, data ExtractionResult) error {
	fmt.Println("Vector: Running v2 resource pipeline...")

	if err := mm.ensureTenant(ctx); err != nil {
		return fmt.Errorf("tenant setup failed: %w", err)
	}
	if err := mm.ensureDatabase(ctx); err != nil {
		return fmt.Errorf("database setup failed: %w", err)
	}

	collID, err := mm.ensureCollection(ctx)
	if err != nil {
		return fmt.Errorf("collection setup failed: %w", err)
	}

	// Generate semantic embedding using local llama model
	embedding, err := mm.generateEmbedding(ctx, data.RawText)
	if err != nil {
		return fmt.Errorf("embedding generation failed: %w", err)
	}

	uniqueID := fmt.Sprintf("id_%d", time.Now().UnixNano())
	// ChromaDB v2 strict payload format
	payload := map[string]any{
		"ids":        []string{uniqueID},
		"documents":  []string{data.RawText},
		"embeddings": []any{embedding}, // Array of arrays
		"metadatas": []map[string]string{
			{
				"context":        "extraction_event",
				"entities_count": fmt.Sprintf("%d", len(data.Entities)),
			},
		},
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/api/v2/tenants/%s/databases/%s/collections/%s/add",
		ChromaDBURL, ChromaTenantName, ChromaDBName, collID)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := mm.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("chromadb v2 /add failed: %d, %s", resp.StatusCode, string(body))
	}

	fmt.Println("✅ ChromaDB (v2): Record added successfully.")
	return nil
}

func RunSamples() {
	ctx := context.Background()

	driver, err := neo4j.NewDriverWithContext(MemgraphURI, neo4j.BasicAuth("", "", ""))
	if err != nil {
		panic(err)
	}
	defer driver.Close(ctx)

	manager := &MemoryManager{
		GraphDriver: driver,
		HTTPClient:  &http.Client{Timeout: 10 * time.Second},
	}

	if err := manager.TestConnections(ctx); err != nil {
		fmt.Printf("❌ Connection Test Failed: %v\n", err)
		return
	}

	incomingJSON := `{
		"raw_text": "We updated the ticketmaster fulfillment flow to include a new validation step.",
		"entities": [
			{"name": "ticketmaster", "type": "Project"},
			{"name": "fulfillment flow", "type": "Process"},
			{"name": "validation step", "type": "Module"}
		],
		"relationships": [
			{"source": "ticketmaster", "target": "fulfillment flow", "type": "contains"},
			{"source": "fulfillment flow", "target": "validation step", "type": "includes"}
		]
	}`

	var result ExtractionResult
	if err := json.Unmarshal([]byte(incomingJSON), &result); err != nil {
		fmt.Printf("❌ JSON Unmarshal Error: %v\n", err)
		return
	}
	result.RawText = "We updated the ticketmaster fulfillment flow to include a new validation step."

	fmt.Println("🚀 Starting Memory Update...")

	if err := manager.UpdateGraph(ctx, result); err != nil {
		fmt.Printf("❌ Graph Update Failed: %v\n", err)
	} else {
		fmt.Println("✅ Graph Update Successful.")
	}

	if err := manager.UpdateVector(ctx, result); err != nil {
		fmt.Printf("❌ Vector Update Failed: %v\n", err)
	} else {
		fmt.Println("✅ Vector Update Successful.")
	}

	fmt.Println("✨ Memory Sync Complete.")
}
