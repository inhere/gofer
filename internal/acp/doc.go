// Package acp implements a minimal Agent Client Protocol (ACP) client: gofer
// spawns an ACP agent as a child process and drives one prompt turn over its
// stdio (JSON-RPC 2.0, one message per line).
//
// It is the transport half of the `acp-agent` type (see
// docs/design/2026-09-17-acp-agent-and-approval-gate-design.md §一): initialize →
// session/new → session/prompt, with session/update notifications delivered to a
// Handler and agent→client requests (session/request_permission) answered by it.
// The package knows nothing about jobs, config or logs — internal/runner/acp maps
// its events onto a job's stdout/acp.jsonl.
//
// Scope (S0): one process per client, one session, one prompt turn. Results
// declare no fs/terminal capability, so an agent's fs/* and terminal/* requests
// are refused with JSON-RPC -32601 (method not found) like any unknown method.
package acp
