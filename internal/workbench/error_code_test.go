package workbench

import (
	"errors"
	"regexp"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

var errTextCJK = regexp.MustCompile(`\p{Han}`)

func hasCode(err error, code string) bool {
	got, _, ok := i18n.CodeOf(err)
	return ok && got == code
}

// assertCoded: err carries the catalog code, the named args, and an English log text
// (the sentence in the request language comes from the catalog, not from Error()).
func assertCoded(t *testing.T, err error, code string, wantArgs ...any) {
	t.Helper()
	got, args, ok := i18n.CodeOf(err)
	if !ok || got != code {
		t.Fatalf("err = %v, want code %q (got %q, coded=%v)", err, code, got, ok)
	}
	for i := 0; i+1 < len(wantArgs); i += 2 {
		found := false
		for j := 0; j+1 < len(args); j += 2 {
			if args[j] == wantArgs[i] && args[j+1] == wantArgs[i+1] {
				found = true
			}
		}
		if !found {
			t.Errorf("code %s args = %v, want %v=%v", code, args, wantArgs[i], wantArgs[i+1])
		}
	}
	if errTextCJK.MatchString(err.Error()) {
		t.Errorf("Error() of %s = %q: log text must be English", code, err.Error())
	}
	if !i18n.HasKey(i18n.ZhCN, "err."+code) || !i18n.HasKey(i18n.EN, "err."+code) {
		t.Errorf("err.%s is missing from a language catalog", code)
	}
}

// TestRelayDiffAndAgentLessReviewErrorsAreCoded covers the coded errors no other
// fixture reaches: a relay thread has no job diff, and each keeps its sentinel.
func TestRelayDiffAndAgentLessReviewErrorsAreCoded(t *testing.T) {
	service := NewService(openWorkbenchStore(t), nil, nil)
	_, err := service.Diff("r:some-relay")
	if !errors.Is(err, ErrNotResumable) {
		t.Fatalf("relay diff err = %v, want ErrNotResumable", err)
	}
	assertCoded(t, err, "diff_relay_no_job")
}
