package inbox

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/today"
)

// notInboxCardKinds names the today card kinds that deliberately never reach the
// inbox, each with the reason. Empty today: every card kind has an inbox kind.
var notInboxCardKinds = map[string]string{}

// todayCardKinds reads every string constant named Kind* declared in the today
// package, so a card kind added later is picked up without editing this test.
func todayCardKinds(t *testing.T) []string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	dir := filepath.Join(filepath.Dir(self), "..", "today")
	pkgs, err := parser.ParseDir(token.NewFileSet(), dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, sp := range gd.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if !strings.HasPrefix(n.Name, "Kind") || i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						if v, err := strconv.Unquote(lit.Value); err == nil {
							out = append(out, v)
						}
					}
				}
			}
		}
	}
	return out
}

// Every card kind the today package can produce is an inbox kind or is excluded with a
// reason, so a newly added kind cannot silently miss /v1/inbox (acp_reply once did).
func TestTodayCardKindsReachInbox(t *testing.T) {
	kinds := todayCardKinds(t)
	if !slices.Contains(kinds, today.KindACPReply) || len(kinds) < 10 {
		t.Fatalf("kind scan found %v, expected the today card kinds", kinds)
	}
	inboxKinds := config.InboxKinds()
	for _, k := range kinds {
		reason, excluded := notInboxCardKinds[k]
		switch {
		case !slices.Contains(inboxKinds, k) && !excluded:
			t.Errorf("today card kind %q is not in config.InboxKinds and not in notInboxCardKinds", k)
		case slices.Contains(inboxKinds, k) && excluded:
			t.Errorf("today card kind %q is an inbox kind but also whitelisted (%s)", k, reason)
		}
	}
}

// An acp_reply card is projected into the action column and announced by the same
// event as a terminal relay turn.
func TestACPReplyCardLandsInActionColumn(t *testing.T) {
	if got := config.InboxDefaultColumn(today.KindACPReply); got != "action" {
		t.Fatalf("acp_reply default column = %q, want action", got)
	}
	if got := config.InboxKindEvents(today.KindACPReply); !slices.Equal(got, []string{"session.awaiting_reply"}) {
		t.Fatalf("acp_reply events = %v", got)
	}
	if got := Column(today.Card{Key: "acp_reply:j1", Kind: today.KindACPReply}, nil); got != ColumnAction {
		t.Fatalf("acp_reply card column = %q, want %q", got, ColumnAction)
	}
}
