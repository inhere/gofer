package xfer

import (
	"regexp"
	"strings"
	"testing"
)

// TestNewIDShape pins the XFER-02 short id: `xf-` + 8 lowercase hex chars, with
// enough entropy that two ids in a row differ.
func TestNewIDShape(t *testing.T) {
	re := regexp.MustCompile(`^xf-[0-9a-f]{8}$`)
	seen := make(map[string]struct{}, 256)
	for range 256 {
		id := NewID()
		if !re.MatchString(id) {
			t.Fatalf("NewID() = %q, want xf-<8 hex>", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("NewID() repeated %q within 256 draws", id)
		}
		seen[id] = struct{}{}
	}
	if len(NewID()) != 11 {
		t.Fatalf("id length = %d, want 11 (the `tool xfer ls` column width)", len(NewID()))
	}
	if strings.HasPrefix(NewID(), "xf-") == false {
		t.Fatal("id must be prefixed xf-")
	}
}
