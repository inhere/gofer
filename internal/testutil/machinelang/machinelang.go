// Package machinelang pins the machine locale a test binary sees.
//
// An unconfigured language is "auto" — the language of the machine running gofer
// (design 2026-10-27-i18n-design.md, decided 2026-10-30) — so the output of a test that
// builds a config without `language:` depends on the developer's or CI's LANG. The
// packages whose assertions were written against the Chinese text call PinZh from
// TestMain: their machine is a Chinese one, and a test that exercises English or the
// auto chain sets its own variables (t.Setenv).
package machinelang

import "os"

// PinZh makes the process look like a zh_CN machine to i18n.FromSystem: LC_ALL names
// zh-CN and the lower-priority variables are cleared.
func PinZh() {
	_ = os.Setenv("LC_ALL", "zh_CN.UTF-8")
	_ = os.Unsetenv("LC_MESSAGES")
	_ = os.Unsetenv("LANG")
	_ = os.Unsetenv("LANGUAGE")
}
