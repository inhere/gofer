// Package cjkscan is the shared body of the "no hardcoded Chinese" guard tests of the
// Go packages that write text for an agent or a person (design 2026-10-27-i18n-design.md
// §7), the Go counterpart of web/src/i18n/noHardcodedCjk.test.ts.
//
// A guard scans every non-test .go file of one package directory for string literals
// carrying a Han character (comments are not text an agent reads, so they never count).
// A file with such literals must be listed in the package's exemption table with the
// reason and the number of literals; the table is a ratchet:
//
//   - a file NOT in the table must have none — new Chinese turns the guard red;
//   - a file in the table may not gain literals (its count is a ceiling);
//   - a file whose count dropped, or that has none left, must lower or delete its
//     entry — so the table only ever gets shorter.
package cjkscan

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var han = regexp.MustCompile(`[\p{Han}]`)

// Exempt is one file allowed to keep Chinese string literals.
type Exempt struct {
	// Max is how many string literals carrying CJK the file has today.
	Max int
	// Why says why they stay (what they are and where they get migrated).
	Why string
	// Class is the category of the exemption (only the tree-wide table in
	// guard_all_test.go fills it; the per-directory Check ignores it).
	Class string
}

// Hit is one string literal carrying CJK.
type Hit struct {
	Line int
	Text string
}

// Hits returns the CJK string literals of every non-test .go file in dir, by file name.
func Hits(t *testing.T, dir string) map[string][]Hit {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]Hit{}
	for _, path := range files {
		name := filepath.Base(path)
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if ok && lit.Kind == token.STRING && han.MatchString(lit.Value) {
				out[name] = append(out[name], Hit{Line: fset.Position(lit.Pos()).Line, Text: lit.Value})
			}
			return true
		})
	}
	return out
}

// TreeHits returns the CJK string literals of every non-test .go file under root,
// keyed by the slash-separated path relative to root ("job/model.go"). Directories
// named in skip (relative to root, slash-separated, whole subtree) and testdata /
// hidden directories are not scanned.
func TreeHits(t *testing.T, root string, skip ...string) map[string][]Hit {
	t.Helper()
	skipSet := map[string]bool{}
	for _, s := range skip {
		skipSet[s] = true
	}
	out := map[string][]Hit{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if skipSet[rel] || (rel != "." && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), "."))) {
			return filepath.SkipDir
		}
		for name, hs := range Hits(t, path) {
			key := name
			if rel != "." {
				key = rel + "/" + name
			}
			out[key] = hs
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Check runs the guard for the package in dir against its exemption table.
func Check(t *testing.T, dir string, exempt map[string]Exempt) {
	t.Helper()
	CheckHits(t, Hits(t, dir), exempt)
}

// CheckHits compares scan results with an exemption table (see the package comment).
func CheckHits(t *testing.T, hits map[string][]Hit, exempt map[string]Exempt) {
	t.Helper()
	names := make([]string, 0, len(hits))
	for name := range hits {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		got := hits[name]
		ex, listed := exempt[name]
		switch {
		case !listed:
			for _, h := range got {
				t.Errorf("%s:%d: string literal carries CJK — move the text into internal/i18n (catalog or template), "+
					"or list the file in the exemption table with a reason: %s", name, h.Line, trim(h.Text))
			}
		case len(got) > ex.Max:
			t.Errorf("%s has %d CJK string literals, its exemption allows %d (%s): new Chinese text belongs in internal/i18n",
				name, len(got), ex.Max, ex.Why)
		case len(got) < ex.Max:
			t.Errorf("%s has %d CJK string literals but its exemption still says %d: lower Max (or delete the entry at 0)",
				name, len(got), ex.Max)
		}
	}
	keys := make([]string, 0, len(exempt))
	for name := range exempt {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		if exempt[name].Why == "" {
			t.Errorf("%s: an exemption needs a reason", name)
		}
		if _, ok := hits[name]; !ok {
			t.Errorf("%s is listed as exempt but has no CJK string literal (or no longer exists): delete the entry", name)
		}
	}
}

func trim(s string) string {
	r := []rune(s)
	if len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}
