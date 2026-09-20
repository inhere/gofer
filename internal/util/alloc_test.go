package util

import (
	"math"
	"testing"
)

func TestCapSum(t *testing.T) {
	cases := []struct {
		name  string
		parts []int
		want  int
	}{
		{"no parts", nil, 0},
		{"typical env merge", []int{120, 7}, 127},
		{"non-positive parts add nothing", []int{-5, 3, 0, 4}, 7},
		{"exactly at the cap", []int{MaxHint}, MaxHint},
		{"one part over the cap clamps", []int{MaxHint + 1}, MaxHint},
		{"sum over the cap clamps", []int{MaxHint - 1, 2}, MaxHint},
		{"huge parts cannot wrap", []int{math.MaxInt, math.MaxInt}, MaxHint},
		{"negative wraparound input stays sane", []int{math.MinInt, 3}, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CapSum(tc.parts...); got != tc.want {
				t.Fatalf("CapSum(%v) = %d, want %d", tc.parts, got, tc.want)
			}
		})
	}
}

func TestCapMul(t *testing.T) {
	cases := []struct {
		name string
		n, k int
		want int
	}{
		{"two argv elements per key", 5, 2, 10},
		{"zero factor", 0, 2, 0},
		{"negative factor", 5, -2, 0},
		{"both factors zero", 0, 0, 0},
		{"exactly at the cap", MaxHint / 2, 2, MaxHint},
		{"over the cap clamps", MaxHint/2 + 1, 2, MaxHint},
		{"huge factors cannot wrap", math.MaxInt, math.MaxInt, MaxHint},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CapMul(tc.n, tc.k); got != tc.want {
				t.Fatalf("CapMul(%d, %d) = %d, want %d", tc.n, tc.k, got, tc.want)
			}
		})
	}
}

// TestCapHintsAreAlwaysLegalCapacities is the contract that matters: whatever the
// caller feeds in (including values that would overflow an unguarded
// len(a)+len(b) or len(m)*2), the hint is a capacity make() accepts. A wrapped
// hint would otherwise panic with "makeslice: cap out of range".
func TestCapHintsAreAlwaysLegalCapacities(t *testing.T) {
	hints := []int{
		CapSum(math.MaxInt, math.MaxInt, math.MaxInt),
		CapSum(math.MinInt, math.MinInt),
		CapMul(math.MaxInt, math.MaxInt),
		CapMul(math.MinInt, math.MinInt),
		CapSum(), CapMul(0, 0),
	}
	for _, n := range hints {
		if n < 0 || n > MaxHint {
			t.Fatalf("hint %d out of range [0, %d]", n, MaxHint)
		}
		if got := make([]string, 0, n); cap(got) != n {
			t.Fatalf("make([]string, 0, %d) cap = %d", n, cap(got))
		}
		if got := make(map[string]string, n); got == nil {
			t.Fatalf("make(map[string]string, %d) = nil", n)
		}
	}
}
