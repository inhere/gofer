package waitgate

import "testing"

func TestRipeAtTheGraceBoundary(t *testing.T) {
	const asked = 1000
	if ripe, at := Ripe(asked+GraceSec-1, asked); ripe || at != asked+GraceSec {
		t.Fatalf("one second early: ripe=%v recheck=%d", ripe, at)
	}
	if ripe, at := Ripe(asked+GraceSec, asked); !ripe || at != 0 {
		t.Fatalf("on the boundary: ripe=%v recheck=%d", ripe, at)
	}
}
