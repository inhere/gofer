// Package answerguard is the派生作答白名单闸 (监督分层升级路由 P3.1, design §8.5/§10).
// It堵 the "any driver may answer any interaction" security gap: a通用 supervisor
// (role=supervisor) answering an interaction it did NOT launch must pass the SAME
// whitelist a L0 auto-answer would (type=choice + enumerable options + prompt matches
// allow_prompt_regex); anything高危 (confirmation / free-text / non-whitelisted) is
// refused so the interaction stays pending and escalates to a human (L3).
//
// Owner (responder==job.origin_agent) and人 (no driver identity) are放行 unconditionally
// — they hold the plan context / are human-authorized, so they answer等同人 (design §8.5).
//
// Layering (G021/G022): this package is deliberately self-contained — it imports NEITHER
// internal/job NOR internal/supervisor, so it can be the shared seam both the answer path
// (job.Service, via the job.AnswerGuard interface it satisfies structurally) and any future
// caller reuse without a dependency cycle. The role lookup is injected through the narrow
// RoleLookup interface (satisfied by presence.Service.Role), so answerguard does not import
// presence either.
package answerguard

import (
	"errors"
	"fmt"
	"regexp"
)

// choiceType mirrors job.InteractionTypeChoice. Kept as a literal (like jobstore's
// terminal-status literals) so answerguard never imports internal/job.
const choiceType = "choice"

// roleSupervisor is the E35 role-preset name of a通用 sup driver. Only a responder
// carrying THIS presence role is gated; every other driver (and the owner / human) is
// treated as trusted. Mirrors supervisor.roleSupervisor.
const roleSupervisor = "supervisor"

// ErrNotAllowed is returned (wrapped) by Check when a supervisor driver may not answer
// the interaction. The answer path maps it to a refusal that keeps the interaction
// pending (and surfaces a 403 over HTTP).
var ErrNotAllowed = errors.New("answer not allowed")

// RoleLookup resolves a driver agent_id to its presence role. The bool is false for an
// unknown/expired agent_id (treated as a non-supervisor → not gated). presence.Service
// satisfies it structurally via its Role method.
type RoleLookup interface {
	Role(agentID string) (string, bool)
}

// Guard holds the compiled prompt whitelist + the role lookup seam. The zero value is
// not usable; build with New.
type Guard struct {
	patterns []*regexp.Regexp
	roles    RoleLookup
}

// New compiles allowRegex into the prompt whitelist and binds the role lookup. An invalid
// pattern is dropped (so it never silently widens the gate) — mirroring supervisor.NewService.
// roles may be nil (role lookup then always misses → only owner/human are放行, every other
// responder is treated as non-supervisor and放行; a nil-roles guard therefore only enforces
// the owner/human distinction, which is the conservative degraded mode).
func New(allowRegex []string, roles RoleLookup) *Guard {
	var pats []*regexp.Regexp
	for _, p := range allowRegex {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		pats = append(pats, re)
	}
	return &Guard{patterns: pats, roles: roles}
}

// Check decides whether responder may answer the interaction. It returns nil to allow,
// or an ErrNotAllowed-wrapped error to refuse (caller keeps the interaction pending).
//
// Source grading (design §8.5):
//   - responder=="" → human (web/CLI, no driver identity) → allow.
//   - responder==originAgent → owner (L1, holds plan context) → allow.
//   - presence role != supervisor → another trusted driver → allow (not gated).
//   - presence role == supervisor (and not the owner) → L2 sup → gate: only a whitelisted
//     choice with enumerable options passes; confirmation / free-text / no-options /
//     non-whitelisted prompt → refuse.
func (g *Guard) Check(responder, originAgent, itType string, hasOptions bool, prompt string) error {
	if responder == "" {
		return nil // human — full trust, equals a person
	}
	if responder == originAgent {
		return nil // owner — holds plan context, equals a person (design §8.5)
	}
	role := ""
	if g.roles != nil {
		role, _ = g.roles.Role(responder)
	}
	if role != roleSupervisor {
		return nil // not a supervisor driver → not gated
	}
	// L2通用 sup: only a whitelisted choice with enumerable options is safe to auto-decide.
	if itType != choiceType || !hasOptions {
		return fmt.Errorf("%w: supervisor may only answer a whitelisted choice (type=%q, options=%t)", ErrNotAllowed, itType, hasOptions)
	}
	if !g.matchWhitelist(prompt) {
		return fmt.Errorf("%w: prompt not whitelisted for a supervisor derived answer", ErrNotAllowed)
	}
	return nil
}

// matchWhitelist reports whether prompt matches any configured pattern. No patterns ⇒
// nothing whitelisted (conservative: a supervisor then cannot derive-answer anything).
func (g *Guard) matchWhitelist(prompt string) bool {
	for _, re := range g.patterns {
		if re.MatchString(prompt) {
			return true
		}
	}
	return false
}
