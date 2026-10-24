package httpapi

import "testing"

// The config view echoes MCP server URLs and the update source URLs; a credential in them
// (user:pass@ or a ?token= query value) must never reach the page.
func TestRedactConfigURL(t *testing.T) {
	cases := map[string]string{
		"":                                      "",
		"https://mcp.example.com/sse":           "https://mcp.example.com/sse",
		"https://user:pass@mcp.example.com/sse": "https://%2A%2A%2A@mcp.example.com/sse",
		"https://mcp.example.com/sse?key=abc&x=1":  "https://mcp.example.com/sse?key=%2A%2A%2A&x=%2A%2A%2A",
		"https://dl.example.com/gofer-{os}-{arch}": "https://dl.example.com/gofer-{os}-{arch}",
		"not a url": "(configured)",
	}
	for in, want := range cases {
		if got := redactConfigURL(in); got != want {
			t.Errorf("redactConfigURL(%q) = %q, want %q", in, got, want)
		}
	}
}
