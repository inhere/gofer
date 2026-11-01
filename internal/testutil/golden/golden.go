// Package golden is the shared check of the i18n "characterization" golden files: the
// zh-CN file under testdata/i18n_golden is the text recorded from the code BEFORE it
// moved into the catalog (it must exist and never changes); the en file is the English
// rewrite, written when missing or when GOFER_GOLDEN_UPDATE is set, and must carry no CJK.
package golden

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

var han = regexp.MustCompile(`\p{Han}`)

// Check compares got with testdata/i18n_golden/<name>.<lang>.txt (relative to the test's
// package directory).
func Check(t *testing.T, lang i18n.Lang, name, got string) {
	t.Helper()
	dir := filepath.Join("testdata", "i18n_golden")
	file := filepath.Join(dir, name+"."+string(lang)+".txt")
	raw, err := os.ReadFile(file)
	if err != nil && lang == i18n.ZhCN {
		t.Fatalf("%s: the zh-CN golden is the recorded original and must exist: %v", file, err)
	}
	if err != nil || (lang != i18n.ZhCN && os.Getenv("GOFER_GOLDEN_UPDATE") != "") {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			t.Fatal(mkErr)
		}
		if werr := os.WriteFile(file, []byte(got), 0o644); werr != nil {
			t.Fatal(werr)
		}
		if err != nil {
			t.Errorf("%s did not exist: written with the current text, review and commit it", file)
		}
	} else if got != string(raw) {
		t.Errorf("%s differs from its golden file\n--- got ---\n%s\n--- want ---\n%s", file, got, raw)
	}
	if lang == i18n.EN && han.MatchString(got) {
		t.Errorf("%s: the English text carries CJK:\n%s", name, got)
	}
}

// Record writes the zh-CN golden from the current text (used once, against the original
// code, by a throwaway recording test).
func Record(t *testing.T, name, got string) {
	t.Helper()
	dir := filepath.Join("testdata", "i18n_golden")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".zh-CN.txt"), []byte(got), 0o644); err != nil {
		t.Fatal(err)
	}
}
