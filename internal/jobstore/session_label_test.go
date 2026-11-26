package jobstore

import "testing"

func TestSessionLabel(t *testing.T) {
	cases := map[string]string{
		"job-20261126-101010-ab12cd34":         "job-20261126-101010-ab12cd34",
		"52155a55-0d5b-4c0e-9a77-3b1f0c2d4e66": "52155a55",
		"s-1":                                  "s-1",
		"":                                     "",
	}
	for in, want := range cases {
		if got := SessionLabel(in); got != want {
			t.Errorf("SessionLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
