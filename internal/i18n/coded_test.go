package i18n

import (
	"errors"
	"fmt"
	"testing"
)

func TestCodedKeepsSentinelAndCode(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("pkg: thing failed")
	err := fmt.Errorf("outer: %w", Coded(sentinel, "some_code", "english text", "n", 2))
	if !errors.Is(err, sentinel) {
		t.Fatal("errors.Is must still see the sentinel")
	}
	if got := err.Error(); got != "outer: pkg: thing failed: english text" {
		t.Errorf("Error() = %q", got)
	}
	code, args, ok := CodeOf(err)
	if !ok || code != "some_code" || len(args) != 2 || args[0] != "n" || args[1] != 2 {
		t.Errorf("CodeOf = %q %v %v", code, args, ok)
	}
	if _, _, ok := CodeOf(sentinel); ok {
		t.Error("a plain error has no code")
	}
	if got := Coded(nil, "c", "only text").Error(); got != "only text" {
		t.Errorf("nil sentinel Error() = %q", got)
	}
}
