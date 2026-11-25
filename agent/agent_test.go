package agent

import (
	"encoding/json"
	"testing"
)

func TestStoredToolRow(t *testing.T) {
	tests := []struct {
		name       string
		id         string
		toolName   string
		success    bool
		paramsJSON string
		result     string
		wantParams any // What we expect the "params" field in the resulting JSON to be
	}{
		{
			name:       "valid json params",
			id:         "123",
			toolName:   "system-read",
			success:    true,
			paramsJSON: `{"path": "main.go"}`,
			result:     "file content",
			wantParams: map[string]any{"path": "main.go"},
		},
		{
			name:       "invalid json params",
			id:         "456",
			toolName:   "system-write",
			success:    false,
			paramsJSON: `not-json`,
			result:     "error occurred",
			wantParams: "not-json",
		},
		{
			name:       "empty params",
			id:         "789",
			toolName:   "list_dir",
			success:    true,
			paramsJSON: ``,
			result:     "dir list",
			wantParams: ``,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotJSON := storedToolRow(tt.id, tt.toolName, tt.success, tt.paramsJSON, tt.result)
			
			var got map[string]any
			if err := json.Unmarshal([]byte(gotJSON), &got); err != nil {
				t.Fatalf("storedToolRow() returned invalid JSON: %v", err)
			}

			if got["id"] != tt.id {
				t.Errorf("id: got %v, want %v", got["id"], tt.id)
			}
			if got["name"] != tt.toolName {
				t.Errorf("name: got %v, want %v", got["name"], tt.toolName)
			}
			if got["success"] != tt.success {
				t.Errorf("success: got %v, want %v", got["success"], tt.success)
			}
			if got["result"] != tt.result {
				t.Errorf("result: got %v, want %v", got["result"], tt.result)
			}

			// Compare params
			// Since we are dealing with map[string]any and potential raw strings, 
			// we convert both to JSON to compare equality easily.
			gotParamsJSON, _ := json.Marshal(got["params"])
			wantParamsJSON, _ := json.Marshal(tt.wantParams)
			
			if string(gotParamsJSON) != string(wantParamsJSON) {
				t.Errorf("params: got %s, want %s", string(gotParamsJSON), string(wantParamsJSON))
			}
		})
	}
}
