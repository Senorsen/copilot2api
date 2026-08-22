package reqctx

import (
	"context"
	"testing"
)

func TestClientID(t *testing.T) {
	if got := GetClientID(context.Background()); got != DefaultClientID {
		t.Fatalf("GetClientID() = %q, want %q", got, DefaultClientID)
	}
	if got := GetClientID(WithClientID(context.Background(), "alice")); got != "alice" {
		t.Fatalf("GetClientID() = %q, want alice", got)
	}
}
