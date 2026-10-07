package util

import (
	"errors"
	"regexp"
	"strings"
)

var versionANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)
var versionLabel = regexp.MustCompile(`(?i)version:?\s*(\S+)`)

// ParseVersionOutput removes terminal color and returns the version token
// printed by Gofer's --version. Worker and managed server upgrades share it.
func ParseVersionOutput(raw string) (string, error) {
	text := strings.TrimSpace(versionANSI.ReplaceAllString(raw, ""))
	if text == "" {
		return "", errors.New("--version printed nothing")
	}
	if m := versionLabel.FindStringSubmatch(text); m != nil {
		return m[1], nil
	}
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimSpace(line), nil
}
