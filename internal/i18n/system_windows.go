//go:build windows

package i18n

import "golang.org/x/sys/windows"

// systemLanguages are the machine's language preferences, highest priority
// first: the POSIX locale variables when the process carries them (a Git Bash or
// MSYS shell exports LANG), then the user's preferred Windows UI language.
func systemLanguages() []Lang {
	out := envLanguages()
	if langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME); err == nil {
		for _, name := range langs {
			if l, ok := Normalize(name); ok {
				out = append(out, l)
			}
		}
	}
	return out
}
