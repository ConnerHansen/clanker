package data

import (
	"testing"
)

func TestNormalizeContextSize(t *testing.T) {
	tests := []struct {
		name   string
		tokens int
		want   int
	}{
		{"16k exact", 16384, 16384},
		{"32k exact", 32768, 32768},
		{"64k exact", 65536, 65536},
		{"128k exact", 131072, 131072},
		{"256k exact (default)", DefaultContextSize, DefaultContextSize},
		{"unset zero falls back to default", 0, DefaultContextSize},
		{"negative falls back to default", -1, DefaultContextSize},
		{"near 16k rounds down", 16400, 16384},
		{"halfway between 16k and 32k", (16384 + 32768) / 2, 16384},
		{"far above max clamps to default", 999999, DefaultContextSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeContextSize(tt.tokens); got != tt.want {
				t.Errorf("NormalizeContextSize(%d) = %d, want %d", tt.tokens, got, tt.want)
			}
		})
	}
}

func TestFormatContextSize(t *testing.T) {
	tests := []struct {
		tokens int
		want   string
	}{
		{16384, "16k"},
		{32768, "32k"},
		{65536, "64k"},
		{131072, "128k"},
		{DefaultContextSize, "256k"},
		{1234, "1234 tokens"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := FormatContextSize(tt.tokens); got != tt.want {
				t.Errorf("FormatContextSize(%d) = %q, want %q", tt.tokens, got, tt.want)
			}
		})
	}
}

func TestContextSizeOptionsContainDefault(t *testing.T) {
	found := false
	for _, opt := range ContextSizeOptions {
		if opt == DefaultContextSize {
			found = true
		}
	}
	if !found {
		t.Fatalf("ContextSizeOptions must include the default %d", DefaultContextSize)
	}

	// Options should be sorted ascending so the TUI list reads bottom-up.
	for i := 1; i < len(ContextSizeOptions); i++ {
		if ContextSizeOptions[i] <= ContextSizeOptions[i-1] {
			t.Fatalf("ContextSizeOptions not sorted: %v", ContextSizeOptions)
		}
	}
}
