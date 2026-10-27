package ptyrelay

import "strings"

// StripANSI returns s with ANSI escape sequences removed, using the same machine
// the pty transcript is written through (Stripper): CSI / OSC / two-byte
// sequences and the charset introducers are dropped, CR is folded into LF and the
// cursor motion a TUI uses to place text comes back as whitespace.
//
// pty.txt is already written de-ANSI'd, but a log tail can also carry text a
// caller pasted or a partially flushed sequence, so every screen read runs it
// through here as well; text with no escape in it comes back unchanged.
func StripANSI(s string) string {
	if !strings.ContainsAny(s, "\x1b\r") {
		return s
	}
	var st Stripper
	out := st.Write([]byte(s))
	return string(out) + string(st.Flush())
}
