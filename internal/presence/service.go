// Package presence is the business layer for E36 driver-agent identity/mailbox
// (design §6.2/§9). It owns the协作主体 (driver agent) registry and the inbox
// messaging primitives on top of the neutral jobstore tables (agent_presence /
// messages): register/heartbeat, online/offline TTL judgement, addressed message
// fan-out (direct / role: / broadcast) and prune.
//
// Layering (G022): presence depends ONLY on jobstore (the data layer); it never
// imports the入口/编排 layers, and jobstore never imports presence (the records
// there are neutral structs). agent_id/agent_token are minted here, not by
// jobstore, so id/token policy lives in one place.
package presence

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// DefaultTTL is how long after its last heartbeat a driver agent is still counted
// online (design §12: 90s). last_seen_at older than now-TTL ⇒ offline (lazily
// computed at read time, never written back).
const DefaultTTL = 90 * time.Second

// DefaultMessageTTL bounds how long an undelivered/unread message lives before the
// prune sweeper may drop it (design §12: message TTL default). A read message is
// pruned regardless; this caps the unread tail so an agent that registers, gets
// messages, then never polls again does not pin rows forever.
const DefaultMessageTTL = 24 * time.Hour

// Addressing prefixes / sentinels for Post's `to` (design §9).
const (
	rolePrefix = "role:"
	// roleOnePrefix addresses a SINGLE online agent of a role (work-assignment), as
	// opposed to rolePrefix which fans out to ALL online agents of that role (notify).
	roleOnePrefix   = "role-one:"
	broadcastTarget = "broadcast"
)

// Message kinds (design §9). Kept as constants for callers; Post does not
// restrict kind (forward-compat with new kinds).
const (
	KindTask       = "task"
	KindNote       = "note"
	KindAnswer     = "answer"
	KindEscalation = "escalation"
)

// Status values surfaced by List (computed lazily from last_seen vs TTL).
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
)

var (
	// ErrUnknownAgent is returned when an agent_id is not in the registry.
	ErrUnknownAgent = errors.New("presence: unknown agent")
	// ErrUnauthorizedAgent is returned when the presented agent_token does not match
	// the registry (soft isolation, design §11 — not a cross-trust-domain auth).
	ErrUnauthorizedAgent = errors.New("presence: agent token mismatch")
)

// Service is the presence/mailbox business layer. nowFn/ttl/msgTTL are injectable
// so tests can pin the clock and the online/expiry windows.
type Service struct {
	store  *jobstore.Store
	nowFn  func() time.Time
	ttl    time.Duration
	msgTTL time.Duration
	newID  func() string
}

// NewService builds a Service over the shared jobstore with production defaults
// (real clock, DefaultTTL, DefaultMessageTTL, crypto/rand ids).
func NewService(store *jobstore.Store) *Service {
	return &Service{
		store:  store,
		nowFn:  time.Now,
		ttl:    DefaultTTL,
		msgTTL: DefaultMessageTTL,
		newID:  randomID,
	}
}

// Agent is the public projection of a registered driver agent (design §9). It
// deliberately omits agent_token (never returned by List/presence reads, §10).
// The snake_case json tags make it the single wire contract reused by both the
// httpapi handler (server projection) and internal/client (decode), mirroring how
// job.JobResult is shared across the transport.
type Agent struct {
	AgentID    string `json:"agent_id"`
	Name       string `json:"name"`
	Role       string `json:"role,omitempty"`
	ProjectKey string `json:"project_key,omitempty"`
	Client     string `json:"client,omitempty"`
	Status     string `json:"status"`
	LastSeenAt int64  `json:"last_seen_at"`
}

// RegisterInput carries the self-reported registration fields. CallerID/Client are
// stamped by the httpapi handler (provenance, E34), not by the agent.
type RegisterInput struct {
	Name       string
	Role       string
	ProjectKey string
	CallerID   string
	Client     string
	MetaJSON   string
}

// RegisterResult is what register returns: the public address (agent_id) and the
// private capability handle (agent_token) the agent presents on poll/deregister.
// json tags make it the wire shape returned by the register endpoint.
type RegisterResult struct {
	AgentID    string `json:"agent_id"`
	AgentToken string `json:"agent_token"`
}

// Message is the public projection of an inbox message (design §9), omitting
// recipient/status bookkeeping the caller does not need. The snake_case json tags
// make it the single wire contract shared by httpapi and internal/client.
type Message struct {
	ID        string `json:"id"`
	FromAgent string `json:"from_agent"`
	ToSpec    string `json:"to_spec,omitempty"`
	Kind      string `json:"kind"`
	Body      string `json:"body,omitempty"`
	Ref       string `json:"ref,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// Register registers a new driver agent or renews an existing one. Idempotency
// (design §10): if an agent with the same name+caller already exists its agent_id
// (and agent_token) are reused and last_seen refreshed (续约); otherwise a fresh
// uuid + token are minted. The token is only ever returned here.
func (s *Service) Register(in RegisterInput) (RegisterResult, error) {
	if strings.TrimSpace(in.Name) == "" {
		return RegisterResult{}, errors.New("presence: register: empty name")
	}
	now := s.nowFn().Unix()

	existing, err := s.findByNameCaller(in.Name, in.CallerID)
	if err != nil {
		return RegisterResult{}, err
	}

	rec := jobstore.PresenceRecord{
		Name:       in.Name,
		Role:       in.Role,
		ProjectKey: in.ProjectKey,
		CallerID:   in.CallerID,
		Client:     in.Client,
		Status:     StatusOnline,
		LastSeenAt: now,
		MetaJSON:   in.MetaJSON,
	}
	if existing != nil {
		// 续约: keep the same address/handle and original registered_at.
		rec.AgentID = existing.AgentID
		rec.AgentToken = existing.AgentToken
		rec.RegisteredAt = existing.RegisteredAt
	} else {
		rec.AgentID = s.newID()
		rec.AgentToken = s.newID()
		rec.RegisteredAt = now
	}
	if err := s.store.UpsertPresence(rec); err != nil {
		return RegisterResult{}, err
	}
	return RegisterResult{AgentID: rec.AgentID, AgentToken: rec.AgentToken}, nil
}

// Poll returns the agent's unread messages, refreshing its heartbeat. The
// agent_token is verified (soft isolation); a mismatch yields ErrUnauthorizedAgent
// and an unknown id yields ErrUnknownAgent. When ack is true the returned messages
// are marked read (consumed); when false they are left unread (peek).
func (s *Service) Poll(agentID, token string, ack bool) ([]Message, error) {
	rec, err := s.authAgent(agentID, token)
	if err != nil {
		return nil, err
	}
	now := s.nowFn().Unix()
	if err := s.store.TouchPresence(rec.AgentID, now); err != nil {
		return nil, err
	}
	rows, err := s.store.ListInbox(agentID, false)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, Message{
			ID:        r.ID,
			FromAgent: r.FromAgent,
			ToSpec:    r.ToSpec,
			Kind:      r.Kind,
			Body:      r.Body,
			Ref:       r.Ref,
			CreatedAt: r.CreatedAt,
		})
		ids = append(ids, r.ID)
	}
	if ack && len(ids) > 0 {
		if err := s.store.MarkRead(ids, now); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Post delivers a message addressed by `to` and returns how many inbox rows it
// created (fan-out, design §9). Addressing forms:
//   - direct agent_id → 1 row, stored in that agent's inbox even when offline
//     (store-and-forward up to message TTL); unknown id → 0.
//   - "role:<name>" → 1 row PER online agent of that role (fan-out / notify-all).
//   - "role-one:<name>" → 1 row to ONE random online agent of that role
//     (work-assignment); no online match → 0.
//   - "broadcast" → 1 row per online agent.
// With no reachable recipient nothing is stored and delivered=0 is returned, so the
// sender (or supervisor) can retry — best-effort by design (§9/§12; role/broadcast
// queue-on-online is explicitly out of scope). from is the sender's agent_id (or
// "system"/"job:<id>"), stamped by the caller.
func (s *Service) Post(from, to, kind, body, ref string) (delivered int, err error) {
	if strings.TrimSpace(to) == "" {
		return 0, errors.New("presence: post: empty to")
	}
	if strings.TrimSpace(kind) == "" {
		return 0, errors.New("presence: post: empty kind")
	}
	recipients, err := s.resolveRecipients(to)
	if err != nil {
		return 0, err
	}
	if len(recipients) == 0 {
		return 0, nil
	}
	now := s.nowFn().Unix()
	expires := now + int64(s.msgTTL/time.Second)
	recs := make([]jobstore.MessageRecord, 0, len(recipients))
	for _, agentID := range recipients {
		recs = append(recs, jobstore.MessageRecord{
			ID:        s.newID(),
			ToAgent:   agentID,
			FromAgent: from,
			ToSpec:    to,
			Kind:      kind,
			Body:      body,
			Ref:       ref,
			Status:    jobstore.MessageUnread,
			CreatedAt: now,
			ExpiresAt: expires,
		})
	}
	if err := s.store.InsertMessages(recs); err != nil {
		return 0, err
	}
	return len(recs), nil
}

// List returns the registry, lazily computing online/offline from last_seen vs
// TTL (never written back). roleFilter/projectFilter, when non-empty, restrict the
// result; agent_token is never included (design §10).
func (s *Service) List(roleFilter, projectFilter string) ([]Agent, error) {
	rows, err := s.store.ListPresence()
	if err != nil {
		return nil, err
	}
	cutoff := s.nowFn().Unix() - int64(s.ttl/time.Second)
	out := make([]Agent, 0, len(rows))
	for _, r := range rows {
		if roleFilter != "" && r.Role != roleFilter {
			continue
		}
		if projectFilter != "" && r.ProjectKey != projectFilter {
			continue
		}
		out = append(out, Agent{
			AgentID:    r.AgentID,
			Name:       r.Name,
			Role:       r.Role,
			ProjectKey: r.ProjectKey,
			Client:     r.Client,
			Status:     statusFor(r.LastSeenAt, cutoff),
			LastSeenAt: r.LastSeenAt,
		})
	}
	return out, nil
}

// Deregister actively removes an agent from the registry after verifying its
// token. It is idempotent: an unknown agent is treated as already gone (nil).
func (s *Service) Deregister(agentID, token string) error {
	rec, found, err := s.store.GetPresence(agentID)
	if err != nil {
		return err
	}
	if !found {
		return nil // already gone
	}
	if rec.AgentToken != token {
		return ErrUnauthorizedAgent
	}
	return s.store.DeletePresence(agentID)
}

// Prune is the serve sweeper entry point (design §9): drop presence rows offline
// past the TTL window and messages that are read or past their TTL. Returns the
// counts removed.
func (s *Service) Prune() (presenceN, msgN int, err error) {
	now := s.nowFn().Unix()
	cutoff := now - int64(s.ttl/time.Second)
	presenceN, err = s.store.PrunePresence(cutoff)
	if err != nil {
		return 0, 0, err
	}
	msgN, err = s.store.PruneMessages(now)
	if err != nil {
		return presenceN, 0, err
	}
	return presenceN, msgN, nil
}

// authAgent loads the agent and verifies its token, mapping the two failure modes
// to the package sentinels (httpapi maps ErrUnauthorizedAgent→403, others→404).
func (s *Service) authAgent(agentID, token string) (jobstore.PresenceRecord, error) {
	rec, found, err := s.store.GetPresence(agentID)
	if err != nil {
		return jobstore.PresenceRecord{}, err
	}
	if !found {
		return jobstore.PresenceRecord{}, ErrUnknownAgent
	}
	if rec.AgentToken != token {
		return jobstore.PresenceRecord{}, ErrUnauthorizedAgent
	}
	return rec, nil
}

// findByNameCaller returns the existing presence row for the register-idempotency
// key (same name + caller), or nil when none. The registry is small (driver
// agents per host), so a linear scan over ListPresence is fine.
func (s *Service) findByNameCaller(name, caller string) (*jobstore.PresenceRecord, error) {
	rows, err := s.store.ListPresence()
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Name == name && rows[i].CallerID == caller {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// resolveRecipients expands `to` into the concrete agent_ids that receive a row:
// "broadcast" → every online agent; "role:<name>" → every online agent with that
// role; otherwise a direct agent_id → that agent if it exists (else none).
func (s *Service) resolveRecipients(to string) ([]string, error) {
	if to == broadcastTarget {
		return s.onlineMatching(func(jobstore.PresenceRecord) bool { return true })
	}
	// role-one:<name> — work-assignment to ONE online agent of that role (a single
	// row, not fan-out). Picks uniformly at random among the online matches for
	// approximate load balancing; no online match ⇒ empty (delivered=0, best-effort,
	// same semantics as role:/broadcast — design §9). Checked before rolePrefix; the
	// two prefixes are disjoint ("role-one:" never HasPrefix "role:").
	if strings.HasPrefix(to, roleOnePrefix) {
		role := strings.TrimPrefix(to, roleOnePrefix)
		ids, err := s.onlineMatching(func(r jobstore.PresenceRecord) bool { return r.Role == role })
		if err != nil || len(ids) == 0 {
			return nil, err
		}
		return []string{ids[pickIndex(len(ids))]}, nil
	}
	if strings.HasPrefix(to, rolePrefix) {
		role := strings.TrimPrefix(to, rolePrefix)
		return s.onlineMatching(func(r jobstore.PresenceRecord) bool { return r.Role == role })
	}
	// Direct address: deliver iff the agent_id is known (online or not — it is
	// explicitly addressed and the row waits in its inbox until polled).
	_, found, err := s.store.GetPresence(to)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return []string{to}, nil
}

// onlineMatching returns the agent_ids of currently-online agents passing pred.
func (s *Service) onlineMatching(pred func(jobstore.PresenceRecord) bool) ([]string, error) {
	rows, err := s.store.ListPresence()
	if err != nil {
		return nil, err
	}
	cutoff := s.nowFn().Unix() - int64(s.ttl/time.Second)
	var ids []string
	for _, r := range rows {
		if r.LastSeenAt < cutoff {
			continue // offline
		}
		if pred(r) {
			ids = append(ids, r.AgentID)
		}
	}
	return ids, nil
}

// pickIndex returns a uniformly-random index in [0,n) using crypto/rand (the
// package's rand). Used by role-one:<name> to pick one online agent. n must be > 0;
// a rand failure (never in practice) degrades deterministically to index 0. The
// modulo bias is negligible for the handful of agents a role ever has.
func pickIndex(n int) int {
	if n <= 1 {
		return 0
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return int(binary.BigEndian.Uint64(b[:]) % uint64(n))
}

// statusFor reports online/offline for a last_seen against the precomputed cutoff.
func statusFor(lastSeen, cutoff int64) string {
	if lastSeen < cutoff {
		return StatusOffline
	}
	return StatusOnline
}

// randomID returns 32 lowercase hex chars (16 crypto/rand bytes) — used for both
// agent_id and agent_token. On a (practically impossible) RNG failure it falls
// back to a nanosecond-derived value so a non-empty id is always produced.
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		ns := uint64(time.Now().UnixNano())
		for i := range b {
			b[i] = byte(ns >> (8 * (uint(i) % 8)))
		}
	}
	return hex.EncodeToString(b[:])
}
