package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRewriteTopLevelModel(t *testing.T) {
	in := []byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-5.5-2026-04-23","choices":[]}`)
	out, upstream, changed := rewriteTopLevelModel(in, "gpt-5.5")
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if upstream != "gpt-5.5-2026-04-23" {
		t.Fatalf("expected upstream=gpt-5.5-2026-04-23, got %q", upstream)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("invalid json out: %v", err)
	}
	if obj["model"] != "gpt-5.5" {
		t.Fatalf("expected model rewritten to gpt-5.5, got %v", obj["model"])
	}
	if obj["id"] != "chatcmpl-1" {
		t.Fatalf("expected id preserved, got %v", obj["id"])
	}
}

func TestRewriteTopLevelModel_NoChangeWhenEqual(t *testing.T) {
	in := []byte(`{"model":"gpt-5.5"}`)
	_, upstream, changed := rewriteTopLevelModel(in, "gpt-5.5")
	if changed {
		t.Fatalf("expected changed=false when equal")
	}
	if upstream != "gpt-5.5" {
		t.Fatalf("expected upstream reported, got %q", upstream)
	}
}

func TestRewriteTopLevelModel_EmptyRequested(t *testing.T) {
	in := []byte(`{"model":"gpt-5.5-2026-04-23"}`)
	out, _, changed := rewriteTopLevelModel(in, "")
	if changed {
		t.Fatalf("expected no change when requested empty")
	}
	if string(out) != string(in) {
		t.Fatalf("expected bytes unchanged")
	}
}

func TestRewriteSSELineModel(t *testing.T) {
	line := []byte(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-5.5-2026-04-23","choices":[]}` + "\n")
	out, upstream, changed := rewriteSSELineModel(line, "gpt-5.5")
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if upstream != "gpt-5.5-2026-04-23" {
		t.Fatalf("expected upstream=gpt-5.5-2026-04-23, got %q", upstream)
	}
	if !strings.HasSuffix(string(out), "\n") {
		t.Fatalf("expected trailing newline preserved")
	}
	if !strings.Contains(string(out), `"gpt-5.5"`) || strings.Contains(string(out), `"gpt-5.5-2026-04-23"`) {
		t.Fatalf("expected model rewritten, got %s", out)
	}
}

func TestRewriteSSELineModel_NoModelUnchanged(t *testing.T) {
	line := []byte(`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"hi"}}]}` + "\n")
	out, _, changed := rewriteSSELineModel(line, "gpt-5.5")
	if changed {
		t.Fatalf("expected no change when no model field")
	}
	if string(out) != string(line) {
		t.Fatalf("expected line unchanged")
	}
}

func TestRewriteSSELineModel_DoneUnchanged(t *testing.T) {
	line := []byte("data: [DONE]\n")
	_, _, changed := rewriteSSELineModel(line, "gpt-5.5")
	if changed {
		t.Fatalf("expected no change for [DONE]")
	}
}

func TestUpstreamModelIfDifferent(t *testing.T) {
	if got := upstreamModelIfDifferent("gpt-5.5-2026-04-23", "gpt-5.5"); got != "gpt-5.5-2026-04-23" {
		t.Fatalf("expected upstream returned when different, got %q", got)
	}
	if got := upstreamModelIfDifferent("gpt-5.5", "gpt-5.5"); got != "" {
		t.Fatalf("expected empty when equal, got %q", got)
	}
	if got := upstreamModelIfDifferent("", "gpt-5.5"); got != "" {
		t.Fatalf("expected empty when upstream empty, got %q", got)
	}
}
