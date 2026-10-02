package httpapi

import (
	"os"
	"strings"
	"testing"
)

func TestResidentMessengerUsesStreamJSONAndScrubsClaudeEnv(t *testing.T) {
	source := readSessionMessengerSource(t)
	for _, want := range []string{"--input-format", "stream-json", "--output-format", "stream-json", "CLAUDE", `"type":`, "json:\"type\""} {
		if !strings.Contains(source, want) {
			t.Fatalf("resident messenger source missing %q", want)
		}
	}
}

func readSessionMessengerSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("session_inject.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
