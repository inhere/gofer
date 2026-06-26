package job

import (
	"regexp"
	"testing"
)

// uuidV4Re matches a canonical RFC 4122 version-4 UUID (version nibble 4, variant
// nibble one of 8/9/a/b).
var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// TestNewUUIDIsValidV4 proves newUUID emits a syntactically valid v4 UUID
// (claude's --session-id requires a legal UUID).
func TestNewUUIDIsValidV4(t *testing.T) {
	for i := 0; i < 100; i++ {
		u := newUUID()
		if !uuidV4Re.MatchString(u) {
			t.Fatalf("newUUID() = %q is not a valid v4 UUID", u)
		}
	}
}

// TestNewUUIDIsUnique proves two calls do not collide (random source).
func TestNewUUIDIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		u := newUUID()
		if _, dup := seen[u]; dup {
			t.Fatalf("newUUID() produced a duplicate: %q", u)
		}
		seen[u] = struct{}{}
	}
}
