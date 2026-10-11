package acp

import (
	"encoding/json"
	"testing"
)

// The stdio entry keeps its historical shape (no type field); an http entry carries
// type/url/headers (MCP-06); mcpCapabilities decodes the http flag.
func TestMCPServerWireShapes(t *testing.T) {
	stdio, _ := json.Marshal(MCPServer{Name: "a", Command: "c", Args: []string{"x"}})
	if string(stdio) != `{"name":"a","command":"c","args":["x"]}` {
		t.Fatalf("stdio = %s", stdio)
	}
	http, _ := json.Marshal(MCPServer{Name: "b", Type: "http", URL: "https://u", Headers: []HTTPHeader{{Name: "Authorization", Value: "t"}}})
	if string(http) != `{"type":"http","name":"b","url":"https://u","headers":[{"name":"Authorization","value":"t"}]}` {
		t.Fatalf("http = %s", http)
	}
	bare, _ := json.Marshal(MCPServer{Name: "c", Type: "http", URL: "https://u"})
	if string(bare) != `{"type":"http","name":"c","url":"https://u","headers":[]}` {
		t.Fatalf("http without headers = %s", bare)
	}
	caps := AgentCapabilities{MCPCapabilities: json.RawMessage(`{"http":true,"sse":false}`)}
	if !caps.MCP().HTTP || caps.MCP().SSE {
		t.Fatalf("caps = %+v", caps.MCP())
	}
	if (AgentCapabilities{}).MCP().HTTP {
		t.Fatal("absent caps must read as no http")
	}
}
