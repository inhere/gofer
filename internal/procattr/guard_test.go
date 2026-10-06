package procattr

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// execAllow lists the exec.Command call sites that must NOT call Background, keyed by
// slash-relative file path (repo root = ../..), optionally "#FuncName", to a reason. Whole directories end in "/".
var execAllow = map[string]string{
	"internal/daemon/":      "detached daemon start: DETACHED_PROCESS is mutually exclusive with CREATE_NO_WINDOW",
	"internal/pty/":         "pty session: ConPTY owns the console, never opens a window; unix has no windows",
	"internal/testutil/":    "test tooling",
	"internal/acp/acptest/": "test tooling",
	"scripts/":              "developer smoke scripts, run from a terminal",
	"internal/commands/config.go#runConfigEdit":     "interactive editor shares the user's console on purpose",
	"internal/commands/steward.go#stewardEditNotes": "interactive editor shares the user's console on purpose",
}

// TestExecCallsUseBackground keeps new background children from reintroducing the
// Windows console-window flash: every exec.Command / exec.CommandContext call in
// non-test source must sit in a function that also calls procattr.Background, or be
// allow-listed above.
func TestExecCallsUseBackground(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var bad []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "tmp", "web", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		for p := range execAllow {
			if rel == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(rel, p)) {
				return nil
			}
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if _, allowed := execAllow[rel+"#"+fn.Name.Name]; allowed {
				continue
			}
			var execCalls []token.Pos
			hasBG := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				x, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch {
				case x.Name == "exec" && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext"):
					execCalls = append(execCalls, call.Pos())
				case x.Name == "procattr" && sel.Sel.Name == "Background":
					hasBG = true
				}
				return true
			})
			if len(execCalls) > 0 && !hasBG {
				for _, pos := range execCalls {
					bad = append(bad, fset.Position(pos).String())
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("exec.Command without procattr.Background in the same function (add it, or allow-list with a reason in execAllow):\n  %s", strings.Join(bad, "\n  "))
	}
}
