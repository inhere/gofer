package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	acpproto "github.com/inhere/gofer/internal/acp"
)

func TestErrorKindSuffixReadsPromptErrorData(t *testing.T) {
	t.Parallel()
	rpc := &acpproto.RPCError{Code: -32603, Message: "usage limit", Data: json.RawMessage(`{"error_kind":"provider_quota","reset_at":"2026-10-19T00:00:00Z"}`)}
	got := errorKindSuffix(fmt.Errorf("wrapped: %w", rpc))
	if want := " [error_kind=provider_quota reset_at=2026-10-19T00:00:00Z]"; got != want {
		t.Fatalf("suffix = %q, want %q", got, want)
	}
	if got := errorKindSuffix(&acpproto.RPCError{Code: -32602, Message: "cwd differs"}); got != "" {
		t.Fatalf("no data, suffix = %q", got)
	}
	if got := errorKindSuffix(errors.New("plain")); got != "" {
		t.Fatalf("non-RPC error, suffix = %q", got)
	}
}
