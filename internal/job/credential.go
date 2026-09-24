package job

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/util"
)

// SEC-01 job-scoped credentials.
//
// A job process used to inherit the whole serve process environment, which included
// its bearer token (`server.token_env`) — so an agent inside a job could call the
// API as the human who started the server. The v0.57 leader field trial made that
// concrete: a leader job with no gofer MCP fell back to the inherited server token,
// reviewed work as a user and released a todo through a path the leader gate does not
// police (design §背景).
//
// The fix is two-sided:
//
//   - the job environment no longer inherits the server's token — util.EnvironWithout
//     plus the deny list below, applied by every runner — and
//   - the job gets a credential of its own, narrow in both TIME and SCOPE: minted
//     when the job starts executing, dead the moment it reaches a terminal state (or
//     at a fallback deadline), and accepted by the API only for the actions the
//     design's permission table allows a job caller.
const (
	// JobTokenPrefix marks a job credential on the wire. authMiddleware only consults
	// the job_tokens table for a bearer token with this prefix, so the overwhelmingly
	// common (user/worker) path keeps its single constant-time scan.
	JobTokenPrefix = "gjt_"

	// EnvJobToken is the variable the credential is injected as. The CLI and the gofer
	// MCP default their bearer token to it, so `gofer job comment …` typed by an agent
	// inside a job authenticates as that job. The name lives in config because the
	// dotenv loader keys off it too (a process carrying it is a JOB, and the .env it
	// would otherwise load is the server's).
	EnvJobToken = config.EnvJobToken
	// EnvServerAddr tells a job which hub to talk to. A hub-local job gets the address
	// this serve listens on; a dispatched job gets the hub address its worker is
	// connected to. It exists because `GOFER_SERVER_ADDR` (the CLI's own default) is a
	// client-node setting that a job cannot be expected to inherit.
	EnvServerAddr = "GOFER_SERVER_ADDR"
	// EnvJobBin is the absolute path of the gofer executable RUNNING the job, exported
	// alongside the PATH entry that makes `gofer` resolve to it (see jobBinaryEnv).
	EnvJobBin = "GOFER_BIN"
)

// DefaultJobEnvDeny is the baseline denylist of inherited environment keys: the
// credentials gofer's own processes carry, plus the pointer to the config dir they are
// read from. `server.job_env_denylist` ADDS to it (a deployment may name more), and a
// project's `job_env_allow` can re-admit any of them deliberately.
//
// GOFER_CONFIG_DIR is here because it is a credential pointer, not a setting: a job
// that inherits it hands the next `gofer` invocation the SERVER's <config-dir>/.env,
// which holds the operator's own token. That is not theoretical — it is leak 1 of the
// v0.60 field trial (design §v0.60 真机验收与 F10): the host's 0.53.1 CLI (which knows
// nothing about job credentials) loaded that file and called the API as the user.
var DefaultJobEnvDeny = []string{"GOFER_TOKEN", "GOFER_SERVER_TOKEN", "GOFER_WORKER_TOKEN", config.EnvConfigDir}

// jobTokenRandomBytes is the entropy of a job token's random half (32 hex chars).
const jobTokenRandomBytes = 16

// jobTokenFallbackGrace is how long a credential outlives its job's own deadline when
// the terminal path never runs. A job that is killed with the whole process — a
// crashed hub, a `kill -9` — leaves no chance to revoke, so the row carries an
// expiry instead of living forever.
const jobTokenFallbackGrace = 10 * time.Minute

// newJobToken mints a credential for jobID: `gjt_<job_id>_<32 hex>`. The job id is
// embedded so an operator reading a token in a log line (they do get printed by
// careless agents) can tell WHICH job leaked it without a database lookup; it is not
// used for resolution — that is the hash.
func newJobToken(jobID string) (string, error) {
	buf := make([]byte, jobTokenRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("job credential: read random: %w", err)
	}
	return JobTokenPrefix + jobID + "_" + hex.EncodeToString(buf), nil
}

// hashJobToken returns the hex sha256 the store keeps. The plaintext token is never
// persisted.
func hashJobToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// JobTokenLooksLikeCredential reports whether a presented bearer token is shaped like
// a job credential. The HTTP layer uses it to decide whether the job_tokens table is
// worth consulting at all.
func JobTokenLooksLikeCredential(token string) bool {
	return strings.HasPrefix(token, JobTokenPrefix)
}

// jobCredentialIdentity classifies the credential a job gets: a leader job (it leads
// a plan) is a leader credential scoped to that plan; every other job is a member
// credential. A member job that happens to be ATTACHED to a plan is still a member —
// attachment groups work, it does not widen rights (design §一.3).
func jobCredentialIdentity(req JobRequest) (kind, planID string) {
	if req.LeaderOfPlan != "" {
		return jobstore.JobCredentialLeader, req.LeaderOfPlan
	}
	return jobstore.JobCredentialMember, req.PlanID
}

// JobTokenLookup is the resolved identity of a presented job credential: which job
// it is, and which half of the permission table applies to it.
type JobTokenLookup struct {
	JobID  string
	Kind   string // jobstore.JobCredentialMember | JobCredentialLeader
	PlanID string
}

// issueJobToken mints, stores and returns the credential for a job that is about to
// start executing. ttl is the job's own deadline; the row expires 10 minutes later as
// the fallback (see jobTokenFallbackGrace).
//
// The plaintext token is returned to the caller and never stored. Failure is NOT
// fatal to the job: a job without a credential is exactly the pre-SEC-01 state for
// that job (it simply cannot call the API), which is a far better outcome than
// refusing to run the work because a bookkeeping row could not be written.
func (s *Service) issueJobToken(jobID, kind, planID string, timeout time.Duration) (string, error) {
	token, err := newJobToken(jobID)
	if err != nil {
		return "", err
	}
	now := s.nowFn().Unix()
	rec := jobstore.JobTokenRecord{
		JobID:     jobID,
		TokenHash: hashJobToken(token),
		Kind:      kind,
		PlanID:    planID,
		ExpiresAt: now + int64(timeout.Seconds()) + int64(jobTokenFallbackGrace.Seconds()),
		CreatedAt: now,
	}
	if err := s.meta.UpsertJobToken(rec); err != nil {
		return "", err
	}
	return token, nil
}

// revokeJobToken kills a job's credential at the end of its execution. It is
// best-effort and idempotent: a job that never had one (an old worker's dispatch, a
// submit that failed before issuance) is a silent no-op, and a second call after the
// terminal race is too.
//
// It records NO job event: the revocation is a necessary consequence of the terminal
// state the job just reached, so an event for it would be pure noise on every job —
// and, worse, it would break the invariant the whole event stream rests on, that
// `job.terminal` / `job.needs_review` is a job's LAST event (SSE subscribers, the
// review console and `job watch` all stop reading at it; v0.44's 8e03d6e fixed the
// order once). The audit trace a leaked token needs is the `revoked_at` column on the
// row that already carries the credential's lifetime — "it was live from created_at
// until revoked_at" — which is durable, queryable and off the event stream. The
// debug line is for the operator watching a live serve, not for the record.
func (s *Service) revokeJobToken(jobID string) {
	if jobID == "" {
		return
	}
	ok, err := s.meta.RevokeJobToken(jobID, s.nowFn().Unix())
	if err != nil {
		slog.Warn("job credential: revoke", "job_id", jobID, "err", err)
		return
	}
	if ok {
		slog.Debug("job credential revoked", "job_id", jobID)
	}
}

// LookupJobToken resolves a presented credential to its job and kind, and reports
// whether it is still usable. It is the whole of the server-side credential check:
// unknown (no row), revoked, or past its fallback deadline all answer false, and the
// HTTP layer turns that into the same 401 an unknown bearer token gets — a job token
// that stopped working must not be distinguishable from one that never existed.
func (s *Service) LookupJobToken(token string) (JobTokenLookup, bool) {
	if !JobTokenLooksLikeCredential(token) {
		return JobTokenLookup{}, false
	}
	rec, ok, err := s.meta.GetJobTokenByHash(hashJobToken(token))
	if err != nil {
		slog.Warn("job credential: lookup", "err", err)
		return JobTokenLookup{}, false
	}
	if !ok || rec.RevokedAt > 0 || rec.ExpiresAt <= s.nowFn().Unix() {
		return JobTokenLookup{}, false
	}
	return JobTokenLookup{JobID: rec.JobID, Kind: rec.Kind, PlanID: rec.PlanID}, true
}

// effectiveJobEnvDeny is the denylist applied to a job's inherited environment: the
// built-in credential keys plus whatever this deployment named in
// server.job_env_denylist. A fresh slice every call — the caller's project allow list
// is resolved separately, and neither list may alias the config's.
func effectiveJobEnvDeny(cfg *config.Config) []string {
	out := make([]string, 0, util.CapSum(len(DefaultJobEnvDeny), len(cfg.Server.JobEnvDenyList)))
	out = append(out, DefaultJobEnvDeny...)
	return append(out, cfg.Server.JobEnvDenyList...)
}

// jobCredentialEnv is the gofer-owned environment a job's child process gets: its own
// credential, the address of the hub it should talk to, and the gofer binary that issued
// the credential (PATH + GOFER_BIN — see jobBinaryEnv for why the binary is part of the
// credential story).
//
// The credential comes from one of three places, in priority order: the token the hub
// put on this request (a dispatched job), nothing at all (a dispatched job on a peer
// too old to carry one — CredentialExternal with no token), or a freshly minted one
// (a hub-local job, which is what every job on this machine is).
//
// GOFER_SERVER_ADDR is skipped for a dispatched job: the worker set it from the hub
// address its connection actually arrived on, which is the only address guaranteed to
// be reachable from that machine.
func (s *Service) jobCredentialEnv(cfg *config.Config, jobID string, req JobRequest, timeout time.Duration) map[string]string {
	env := make(map[string]string, 4)
	switch {
	case req.JobToken != "":
		env[EnvJobToken] = req.JobToken
	case req.CredentialExternal:
		// A pre-v11 hub sent no credential: the job runs without one (and the hub
		// recorded job.credential_skipped for it).
	default:
		kind, planID := jobCredentialIdentity(req)
		tok, err := s.issueJobToken(jobID, kind, planID, timeout)
		if err != nil {
			// Best-effort by design: a job without a credential is the pre-SEC-01
			// state for that job, which beats refusing to run the work.
			slog.Warn("job credential: issue", "job_id", jobID, "err", err)
		} else {
			env[EnvJobToken] = tok
		}
	}
	if !req.CredentialExternal {
		if addr := jobServerAddr(cfg); addr != "" {
			env[EnvServerAddr] = addr
		}
	}
	for k, v := range jobBinaryEnv() {
		env[k] = v
	}
	return env
}

// jobBinaryEnv points a job's child process at the gofer that is running it: that
// executable's directory goes FIRST on PATH and its absolute path is exported as
// GOFER_BIN.
//
// This is leak 2 of the v0.60 field trial (design §v0.60 真机验收与 F10): the `gofer` a
// job found on PATH was whatever the HOST happened to have installed — 0.53.1 on a
// 0.60.0 server — so an agent following a leader prompt either lacked the subcommand it
// was told to run (`job comment`) or used a CLI that predates job credentials, which
// then read the server's .env and acted as the operator. A job's credential only means
// anything when the binary reading it is the one that issued it, so the job gets that
// binary first — the hub's own executable for a hub-local job, the WORKER's for a
// dispatched job (that is the gofer that ran it).
//
// Best-effort: a process whose own executable cannot be resolved keeps its inherited
// PATH and no GOFER_BIN, which is better than exporting a path we do not have.
func jobBinaryEnv() map[string]string {
	exe, err := os.Executable()
	if err != nil {
		slog.Warn("job env: cannot resolve this gofer's executable, leaving PATH alone", "err", err)
		return nil
	}
	path := filepath.Dir(exe)
	if inherited := os.Getenv("PATH"); inherited != "" {
		path += string(os.PathListSeparator) + inherited
	}
	return map[string]string{EnvJobBin: exe, "PATH": path}
}

// jobServerAddr resolves the address a job process on THIS machine should reach this
// hub on, from server.addr: the listen address is not a connectable one when it is
// unspecified (`0.0.0.0:8765`, `:8765`), so those hosts become loopback. The result is
// a bare host:port — the CLI (and every other client) adds the scheme and applies the
// same 0.0.0.0 rewrite through client.NormalizeBaseURL.
//
// Deliberately not client.NormalizeBaseURL itself: internal/client imports internal/job
// (it decodes JobResult), so calling it here would be an import cycle (G022).
func jobServerAddr(cfg *config.Config) string {
	addr := strings.TrimSpace(cfg.Server.Addr)
	if addr == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

// recordEnvAllowedEvents notes the project's job_env_allow decision on the job when it
// actually rescues something: a key is reported only when the denylist would have
// stripped it AND this process really carries it. An allowance that matched nothing is
// not "in force" and does not deserve an event — the job detail has to answer "what did
// this run see that it normally would not", and an un-fired entry answers nothing.
func (s *Service) recordEnvAllowedEvents(jobID string, allow, deny []string) {
	var keys []string
	for _, key := range allow {
		if !util.EnvironKeyDenied(key, deny, nil) {
			continue // already inherited: the allow list did not have to do anything
		}
		if _, ok := os.LookupEnv(key); ok {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return
	}
	s.recordEvent(jobID, EventJobEnvAllowed, map[string]any{"keys": keys})
}
