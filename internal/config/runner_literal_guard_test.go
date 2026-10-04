package config

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// G043 guard: the built-in runner has two spellings ("local" canonical, "server"
// the CLI's), so a comparison of a runner label against ONE of them silently
// misses the other. This test scans the non-test Go sources for such comparisons
// — a runner-ish expression compared (==, != or a switch case) with the literal
// "local"/"server" or with the canonical-key constants — and fails on any that is
// not in the allowlist below.
//
// To pass: route the label through NormalizeRunnerName / IsLocalRunnerName
// instead of comparing it raw. Only when the
// compared expression is ALREADY canonical (it just came out of a normalizer, or
// it is a stored job's runner that Submit normalized) add it to the allowlist,
// with the reason.
var runnerCompareAllowlist = map[string]string{
	"internal/httpapi/session_inject.go: runnerKey == config.BuiltinLocalRunner":                     "messengerJobChannel takes the canonical key",
	"internal/httpapi/session_inject.go: runnerKeyForSession(runner) == runnerLocalKey":              "result of the normalizer",
	"internal/job/config.go: runnerKey == builtinLocalRunner":                                        "runnerKey is canonical (config.NormalizeRunnerName at every input boundary)",
	"internal/job/ndjson.go: runnerName != builtinLocalRunner":                                       "runnerName is the already-selected canonical runner key",
	"internal/job/session_stream.go: runnerName != builtinLocalRunner":                               "runnerName is the already-selected canonical runner key",
	"internal/job/submit.go: req.Runner != builtinLocalRunner":                                       "req.Runner was normalized at the top of Submit",
	"internal/job/resume.go: config.NormalizeRunnerName(src.Runner) == config.BuiltinLocalRunner":    "stored label normalized first",
	"internal/messenger/agents.go: runner != config.BuiltinLocalRunner":                              "runner was normalized on the line above",
	"internal/messenger/resident.go: runner != config.BuiltinLocalRunner":                            "runner was normalized on the line above",
	"internal/xfer/dispatch.go: config.NormalizeRunnerName(rec.Runner) == config.BuiltinLocalRunner": "normalizer result",
}

var runnerConstNames = map[string]bool{
	"BuiltinLocalRunner": true, "BuiltinLocalRunnerAlias": true,
	"builtinLocalRunner": true, "runnerLocalKey": true, "localRunnerKey": true,
}

// constSide reports whether e is the literal "local"/"server" or a canonical-key constant.
func constSide(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING && (v.Value == `"local"` || v.Value == `"server"`)
	case *ast.Ident:
		return runnerConstNames[v.Name]
	case *ast.SelectorExpr:
		return runnerConstNames[v.Sel.Name]
	}
	return false
}

func exprText(fset *token.FileSet, e ast.Expr) string {
	var b bytes.Buffer
	_ = printer.Fprint(&b, fset, e)
	return b.String()
}

func runnerish(s string) bool { return strings.Contains(strings.ToLower(s), "runner") }

func TestNoRawBuiltinRunnerSpellingComparisons(t *testing.T) {
	root := ".." // internal/
	var found []string
	scan := func(base string) {
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			slash := filepath.ToSlash(path)
			rel := "internal/" + strings.TrimPrefix(slash, "../") // scanning ../ = internal/
			if strings.HasPrefix(slash, "../../") {
				rel = strings.TrimPrefix(slash, "../../")
			}
			if rel == "internal/config/model.go" { // defines the helpers
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				t.Errorf("parse %s: %v", path, perr)
				return nil
			}
			add := func(expr string) { found = append(found, rel+": "+expr) }
			ast.Inspect(f, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.BinaryExpr:
					if v.Op != token.EQL && v.Op != token.NEQ {
						return true
					}
					switch {
					case constSide(v.Y) && !constSide(v.X) && runnerish(exprText(fset, v.X)):
						add(exprText(fset, v.X) + " " + v.Op.String() + " " + exprText(fset, v.Y))
					case constSide(v.X) && !constSide(v.Y) && runnerish(exprText(fset, v.Y)):
						add(exprText(fset, v.Y) + " " + v.Op.String() + " " + exprText(fset, v.X))
					}
				case *ast.SwitchStmt:
					if v.Tag == nil || !runnerish(exprText(fset, v.Tag)) {
						return true
					}
					for _, st := range v.Body.List {
						cc, ok := st.(*ast.CaseClause)
						if !ok {
							continue
						}
						for _, e := range cc.List {
							if constSide(e) {
								add("switch " + exprText(fset, v.Tag) + " case " + exprText(fset, e))
							}
						}
					}
				}
				return true
			})
			return nil
		})
	}
	scan(root)
	scan("../../cmd")

	seen := map[string]bool{}
	var bad []string
	for _, f := range found {
		key := f
		seen[key] = true
		if _, ok := runnerCompareAllowlist[key]; !ok && !allowlistedEq(key) {
			bad = append(bad, key)
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("raw comparison of a runner label with the built-in runner's spelling (G043) — normalize first "+
			"(config.NormalizeRunnerName / config.IsLocalRunnerName), or allowlist it with a reason:\n  %s",
			strings.Join(bad, "\n  "))
	}
	for k := range runnerCompareAllowlist {
		if !seen[k] && !seenEq(seen, k) {
			t.Errorf("stale allowlist entry (the comparison is gone): %s", k)
		}
	}
}

// allowlistedEq / seenEq treat `==` and `!=` spellings of the same operands alike.
func allowlistedEq(key string) bool {
	_, ok := runnerCompareAllowlist[strings.Replace(key, " != ", " == ", 1)]
	if ok {
		return true
	}
	_, ok = runnerCompareAllowlist[strings.Replace(key, " == ", " != ", 1)]
	return ok
}

func seenEq(seen map[string]bool, key string) bool {
	return seen[strings.Replace(key, " != ", " == ", 1)] || seen[strings.Replace(key, " == ", " != ", 1)]
}
