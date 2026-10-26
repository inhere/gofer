//go:build !windows

package i18n

// systemLanguages are the machine's language preferences, highest priority
// first: the POSIX locale variables (a non-Windows machine has no UI language
// separate from them).
func systemLanguages() []Lang {
	return envLanguages()
}
