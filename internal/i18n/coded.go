package i18n

import (
	"errors"
	"fmt"
)

// codedError is an error that carries a stable catalog code and the values of its
// placeholders next to its English log text. The business layer returns it (wrapping
// its own sentinel, so errors.Is keeps working); the entry layer that answers a request
// recognises it with CodeOf and renders the sentence in the request language — the
// business layer never imports the HTTP layer and never picks a language for it.
type codedError struct {
	err  error
	text string
	code string
	args []any
}

// Coded wraps sentinel (may be nil) into an error whose Error() is
// "<sentinel>: <text>" (English, for logs and the CLI) and whose code / args name the
// catalog sentence err.<code> in the language of whoever shows it. args are name, value
// pairs, as for T.
func Coded(sentinel error, code, text string, args ...any) error {
	return &codedError{err: sentinel, text: text, code: code, args: args}
}

func (e *codedError) Error() string {
	if e.err == nil {
		return e.text
	}
	return fmt.Sprintf("%v: %s", e.err, e.text)
}

func (e *codedError) Unwrap() error { return e.err }

// CodeOf is the catalog code and the placeholder args of the first coded error in err's
// chain; ok is false when there is none.
func CodeOf(err error) (code string, args []any, ok bool) {
	var ce *codedError
	if errors.As(err, &ce) {
		return ce.code, ce.args, true
	}
	return "", nil, false
}
