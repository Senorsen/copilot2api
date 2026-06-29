package anthropic

import (
	"testing"

	"github.com/whtsky/copilot2api/internal/models"
)

func TestModelSupportsEndpoint_NormalizedV1Prefix(t *testing.T) {
	info := &models.Info{SupportedEndpoints: []string{"/messages", "/responses"}}

	if !modelSupportsEndpoint(info, "/v1/messages") {
		t.Fatal("expected /v1/messages to match /messages")
	}

	if !modelSupportsEndpoint(info, "/responses") {
		t.Fatal("expected /responses to be supported")
	}

	if modelSupportsEndpoint(info, "/v1/chat/completions") {
		t.Fatal("did not expect /v1/chat/completions to be supported")
	}
}

func TestResolveModelAlias(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Hyphen-separated versions are normalized to dots
		{"claude-opus-4-6", "claude-opus-4.6"},
		{"claude-opus-4-6-fast", "claude-opus-4.6-fast"},
		{"claude-sonnet-4-6", "claude-sonnet-4.6"},
		{"claude-haiku-4-5", "claude-haiku-4.5"},
		{"claude-opus-4-5", "claude-opus-4.5"},
		{"claude-sonnet-4-5", "claude-sonnet-4.5"},

		// Date suffixes are stripped, then version normalization applied
		{"claude-haiku-4-5-20251001", "claude-haiku-4.5"},
		{"claude-haiku-4.5-20251001", "claude-haiku-4.5"},
		{"claude-sonnet-4-20250514", "claude-sonnet-4"},
		{"claude-opus-4-6-20250514", "claude-opus-4.6"},
		{"claude-opus-4.6-20250514", "claude-opus-4.6"},
		{"claude-sonnet-4-6-20250514", "claude-sonnet-4.6"},
		{"claude-sonnet-4.6-20250514", "claude-sonnet-4.6"},
		{"claude-sonnet-4-5-20250514", "claude-sonnet-4.5"},
		{"claude-opus-4-5-20250514", "claude-opus-4.5"},
		{"claude-opus-4.5-20250514", "claude-opus-4.5"},

		// Non-obvious mapping via explicit alias
		{"claude-opus-4-20250514", "claude-opus-4.5"},

		// Future models: should work automatically without new explicit aliases
		{"claude-opus-4-7-20260101", "claude-opus-4.7"},
		{"claude-sonnet-5-0-20260601", "claude-sonnet-5.0"},

		// Generic normalizer: unknown model with hyphen version
		{"claude-sonnet-4-6-fast", "claude-sonnet-4.6-fast"},

		// Already canonical — no change
		{"claude-opus-4.6", "claude-opus-4.6"},
		{"claude-sonnet-4", "claude-sonnet-4"},

		// No version numbers to normalize
		{"claude-sonnet", "claude-sonnet"},

		// Hyphenated dates must NOT be corrupted
		{"claude-sonnet-4-2025-04-14", "claude-sonnet-4-2025-04-14"},
		{"claude-3-5-sonnet-2025-04-14", "claude-3.5-sonnet-2025-04-14"},

		// Non-Claude models pass through unchanged
		{"gpt-5.3-codex", "gpt-5.3-codex"},
		{"gpt-5.4", "gpt-5.4"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := resolveModelAlias(tt.input)
			if got != tt.want {
				t.Errorf("resolveModelAlias(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestForceContext1M(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"claude-opus-4.6", true},
		{"claude-sonnet-4.6", true},
		{"claude-opus-4.7", true},
		{"claude-opus-4.10", true},
		{"claude-opus-4.6-1m", true},
		{"claude-opus-5.0", true},
		{"claude-sonnet-5.1", true},
		{"claude-opus-4.5", false},
		{"claude-sonnet-4", false},
		{"claude-haiku-4.6", false},
		{"claude-3-5-sonnet", false},
		{"gpt-5.4", false},
	}
	for _, tt := range tests {
		if got := forceContext1M(tt.model); got != tt.want {
			t.Errorf("forceContext1M(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestContext1MHeaders(t *testing.T) {
	if h := context1mHeaders("claude-opus-4.6"); h["anthropic-beta"] != Context1MBeta {
		t.Errorf("expected beta header for opus-4.6, got %v", h)
	}
	if h := context1mHeaders("claude-opus-4.5"); h != nil {
		t.Errorf("expected nil headers for opus-4.5, got %v", h)
	}
}

func TestMergeContext1MBeta(t *testing.T) {
	tests := []struct {
		model      string
		clientBeta string
		want       string // "" means nil header
	}{
		{"claude-opus-4.6", "", Context1MBeta},
		{"claude-opus-4.5", "", ""},
		{"claude-opus-4.5", "oauth-2025-04-20", "oauth-2025-04-20"},
		{"claude-opus-4.6", "oauth-2025-04-20", "oauth-2025-04-20," + Context1MBeta},
		{"claude-opus-4.6", "context-1m-2025-08-07", "context-1m-2025-08-07"},
	}
	for _, tt := range tests {
		got := mergeContext1MBeta(tt.model, tt.clientBeta)
		if tt.want == "" {
			if got != nil {
				t.Errorf("mergeContext1MBeta(%q,%q) = %v, want nil", tt.model, tt.clientBeta, got)
			}
			continue
		}
		if got["anthropic-beta"] != tt.want {
			t.Errorf("mergeContext1MBeta(%q,%q) = %q, want %q", tt.model, tt.clientBeta, got["anthropic-beta"], tt.want)
		}
	}
}
