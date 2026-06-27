package commands

import "testing"

// TestPadColAsciiAndCJK verifies the job-list TITLE column pads/truncates by
// DISPLAY width: ASCII pads 1/col, CJK counts as 2, over-long is …-truncated, and
// every result occupies exactly jobListTitleWidth display columns (so following
// columns stay aligned).
func TestPadCol(t *testing.T) {
	cases := []struct {
		in       string
		wantDisp int // expected display width of the result (== jobListTitleWidth)
	}{
		{"", jobListTitleWidth},
		{"TITLE", jobListTitleWidth},
		{"记住幸运数字 7777", jobListTitleWidth},                       // CJK, fits
		{"验证--cwd解析(SIP pwd/branch) 超长超长超长超长", jobListTitleWidth}, // truncated
	}
	for _, c := range cases {
		got := padCol(c.in, jobListTitleWidth)
		if w := dispWidth(got); w != c.wantDisp {
			t.Fatalf("padCol(%q) display width = %d, want %d (got %q)", c.in, w, c.wantDisp, got)
		}
	}
	// A short ASCII title is left-justified then space-padded (no truncation marker).
	if got := padCol("ok", jobListTitleWidth); got != "ok"+spaces(jobListTitleWidth-2) {
		t.Fatalf("padCol short ascii = %q", got)
	}
	// An over-long title ends with the … marker.
	long := padCol("abcdefghijklmnopqrstuvwxyz0123456789", jobListTitleWidth)
	if r := []rune(long); r[len(r)-1] != '…' {
		t.Fatalf("padCol over-long should end with …, got %q", long)
	}
}

func spaces(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += " "
	}
	return s
}

func TestRuneWidth(t *testing.T) {
	if runeWidth('a') != 1 {
		t.Fatalf("ascii width != 1")
	}
	if runeWidth('记') != 2 {
		t.Fatalf("CJK width != 2")
	}
}
