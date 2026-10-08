package tracker

import "testing"

func TestRepoCwd(t *testing.T) {
	roots := []string{"/work/proj", `D:\work\proj`}
	for _, c := range []struct {
		path, want string
		ok         bool
	}{
		{"/work/proj", ".", true},
		{"/work/proj/sub/repo", "sub/repo", true},
		{"d:/work/proj/a", "a", true},
		{`D:\work\proj\a\b`, "a/b", true},
		{"/work/project2", "", false},
		{"/other", "", false},
		{"", "", false},
	} {
		got, ok := RepoCwd(c.path, roots)
		if got != c.want || ok != c.ok {
			t.Fatalf("RepoCwd(%q) = %q,%v want %q,%v", c.path, got, ok, c.want, c.ok)
		}
	}
}
