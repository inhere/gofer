package httpapi

// rules_handler.go is the JOB-06① rule-library HTTP surface (design §一.1): the entry
// layer's operations over rule.Store, which serve injects via SetRules. Handlers only
// resolve params, enforce can_admin on the two writes and encode responses — the store
// owns the name grammar and the file (G021: this layer validates the REQUEST, never
// re-implements the operation).
//
// The routes are always mounted and answer 503 while no library is wired (mcp / most
// tests), the same degradation /v1/skills and /v1/xfer use. Reads are open to any
// authenticated caller; PUT and DELETE are can_admin-gated, because a rule is text
// every job on the machine may be forced to obey.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/rule"
)

// maxRuleJSONBody bounds the JSON write body. A rule is capped at
// server.rules_max_bytes anyway (16KiB by default), so this only keeps a hostile body
// from being buffered before that check runs.
const maxRuleJSONBody = 1 << 20

// rulesListResp is GET /v1/rules: always an array (never null) so a console can map
// over it unconditionally.
type rulesListResp struct {
	Rules []rule.Rule `json:"rules"`
}

// ruleDetailResp is GET /v1/rules/{name}: the index entry plus the rule text, which is
// what the operator actually wants to read.
type ruleDetailResp struct {
	rule.Rule
	Content string `json:"content"`
}

// rulesUnavailable answers 503 when no rule library is wired (mcp / most tests): the
// routes are always mounted so the surface is uniform, exactly like /v1/skills.
func (s *Server) rulesUnavailable(c *rux.Context) bool {
	if s.rules == nil {
		writeError(c, http.StatusServiceUnavailable, "rules unavailable",
			"this server has no rule library wired; start it with `gofer serve`")
		return true
	}
	return false
}

// rulesWriteCaller runs the can_admin gate the two rule writes share — the same
// capability bit and the same 403 wording the skill and project writes use.
func (s *Server) rulesWriteCaller(c *rux.Context) (string, bool) {
	caller := callerFromCtx(c)
	if !s.callerMayAdmin(caller) {
		writeError(c, http.StatusForbidden, "admin not permitted for this caller", "caller lacks can_admin capability")
		return "", false
	}
	return caller, true
}

// writeRuleError maps a rule-store failure onto the HTTP contract: a missing rule is
// a 404, every refusal the store NAMES (bad name, empty body) is the caller's 400, and
// anything else is this server's 500.
func writeRuleError(c *rux.Context, err error, summary string) {
	switch {
	case errors.Is(err, rule.ErrNotFound):
		writeError(c, http.StatusNotFound, "unknown rule", err.Error())
	case errors.Is(err, rule.ErrInvalid):
		writeError(c, http.StatusBadRequest, summary, err.Error())
	default:
		writeError(c, http.StatusInternalServerError, summary, err.Error())
	}
}

// rulesMaxBytes is the per-rule byte cap: server.rules_max_bytes, the same number the
// submit-time total check uses, with config's default when unset. A single rule bigger
// than the whole injection budget could never be injected, so writing one is refused
// here rather than stored and rejected on every job that binds it.
func (s *Server) rulesMaxBytes() int {
	if s.cfg != nil && s.cfg.RulesMaxBytes > 0 {
		return s.cfg.RulesMaxBytes
	}
	return config.DefaultRulesMaxBytes
}

// handleListRules serves GET /v1/rules: the whole library, index entries only (the
// text stays on disk — a caller that wants it reads one rule).
func (s *Server) handleListRules(c *rux.Context) {
	if s.rulesUnavailable(c) {
		return
	}
	list, err := s.rules.List()
	if err != nil {
		writeRuleError(c, err, "list rules failed")
		return
	}
	out := make([]rule.Rule, 0, len(list))
	out = append(out, list...)
	c.JSON(http.StatusOK, rulesListResp{Rules: out})
}

// handleGetRule serves GET /v1/rules/{name}: the index entry plus the rule text
// (frontmatter included, exactly the file on disk).
func (s *Server) handleGetRule(c *rux.Context) {
	if s.rulesUnavailable(c) {
		return
	}
	name := c.Param("name")
	r, ok, err := s.rules.Get(name)
	if err != nil {
		writeRuleError(c, err, "read rule failed")
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown rule", "no rule named "+name)
		return
	}
	content, err := s.rules.Read(name)
	if err != nil {
		// The index and the file are written together (rule.Store.Set), so an
		// unreadable file here is a broken library, not a missing rule.
		writeRuleError(c, err, "read rule failed")
		return
	}
	c.JSON(http.StatusOK, ruleDetailResp{Rule: r, Content: content})
}

// handlePutRule serves PUT /v1/rules/{name} — the admin-only write that creates or
// replaces one rule. Two bodies are accepted:
//
//	application/json   {"content":"---\ndescription: …\n---\n\n…"}  (the CLI/console)
//	text/markdown      the rule itself, verbatim
func (s *Server) handlePutRule(c *rux.Context) {
	if s.rulesUnavailable(c) {
		return
	}
	caller, ok := s.rulesWriteCaller(c)
	if !ok {
		return
	}
	name := c.Param("name")
	content, status, err := s.rulePutBody(c)
	if err != nil {
		writeError(c, status, "invalid rule body", err.Error())
		return
	}
	if max := s.rulesMaxBytes(); max > 0 && len(content) > max {
		writeError(c, http.StatusBadRequest, "rule too large",
			fmt.Sprintf("rule %s is %d bytes, over server.rules_max_bytes (%d)", name, len(content), max))
		return
	}
	r, err := s.rules.Set(name, content, caller)
	if err != nil {
		writeRuleError(c, err, "write rule failed")
		return
	}
	c.JSON(http.StatusOK, ruleDetailResp{Rule: r, Content: string(content)})
}

// rulePutBody resolves the request body into the rule text. status is 0 unless err is
// set.
func (s *Server) rulePutBody(c *rux.Context) ([]byte, int, error) {
	body, err := io.ReadAll(io.LimitReader(c.Req.Body, maxRuleJSONBody+1))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if strings.HasPrefix(c.Req.Header.Get("Content-Type"), "application/json") {
		var req struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf(`body must be {"content":"…"} or text/markdown: %w`, err)
		}
		body = []byte(req.Content)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, http.StatusBadRequest, errors.New("the rule body is empty")
	}
	return body, 0, nil
}

// handleDeleteRule serves DELETE /v1/rules/{name} (204). A missing rule is a 404, not
// a silent success: the caller asked to remove something specific.
func (s *Server) handleDeleteRule(c *rux.Context) {
	if s.rulesUnavailable(c) {
		return
	}
	if _, ok := s.rulesWriteCaller(c); !ok {
		return
	}
	name := c.Param("name")
	if err := s.rules.Remove(name); err != nil {
		writeRuleError(c, err, "delete rule failed")
		return
	}
	c.SetStatus(http.StatusNoContent)
}
