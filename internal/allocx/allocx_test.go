package allocx

import (
	"math"
	"testing"
)

func TestSum(t *testing.T) {
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
			if got := Sum(tc.parts...); got != tc.want {
				t.Fatalf("Sum(%v) = %d, want %d", tc.parts, got, tc.want)
			}
		})
	}
}

func TestMul(t *testing.T) {
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
			if got := Mul(tc.n, tc.k); got != tc.want {
				t.Fatalf("Mul(%d, %d) = %d, want %d", tc.n, tc.k, got, tc.want)
			}
		})
	}
}

// TestHintsAreAlwaysLegalCapacities is the contract that matters: whatever the
// caller feeds in (including values that would overflow an unguarded
// len(a)+len(b) or len(m)*2), the hint is a capacity make() accepts. A wrapped
// hint would otherwise panic with "makeslice: cap out of range".
func TestHintsAreAlwaysLegalCapacities(t *testing.T) {
	hints := []int{
		Sum(math.MaxInt, math.MaxInt, math.MaxInt),
		Sum(math.MinInt, math.MinInt),
		Mul(math.MaxInt, math.MaxInt),
		Mul(math.MinInt, math.MinInt),
		Sum(), Mul(0, 0),
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
