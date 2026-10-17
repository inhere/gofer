package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/workerenroll"
)

// Worker self-enrollment HTTP entry. Four anonymous routes (registered outside the /v1
// auth group) and three admin routes. The handlers only bind, validate, resolve the
// source address and forward to workerenroll.Service (G021).

const (
	// enrollMaxBody is the request body cap of the anonymous POST routes.
	enrollMaxBody = 8 << 10
	// enrollReadTimeout bounds how long one anonymous POST may take to deliver its body.
	enrollReadTimeout = 10 * time.Second
	// enrollMaxWait is the upper bound of one long poll (below the client's 30 s timeout).
	enrollMaxWait = 25 * time.Second
	// enrollSecretHeader carries the raw enroll secret on poll / confirm / cancel.
	enrollSecretHeader = "X-Gofer-Enroll-Secret"
	// enrollMaxSecretLen caps the header we are willing to hash.
	enrollMaxSecretLen = 256

	enrollMaxLabels   = 8
	enrollMaxLabelLen = 32
	enrollMaxAgents   = 16
	enrollMaxRoots    = 16
	enrollMaxRootLen  = 256
	enrollMaxFieldLen = 64
	enrollMaxProjects = 64
	enrollMaxBlockMin = 24 * 60
	// enrollRecent is how far back the admin list reaches.
	enrollRecent = 24 * time.Hour
)

// enrollPollInterval is how often a long poll re-reads the request (a var for tests).
var enrollPollInterval = 300 * time.Millisecond

// enrollHost is the cleaned host description stored in host_json (display only).
type enrollHost struct {
	Hostname        string   `json:"hostname,omitempty"`
	OS              string   `json:"os,omitempty"`
	Arch            string   `json:"arch,omitempty"`
	GoferVersion    string   `json:"gofer_version,omitempty"`
	ProtocolVersion string   `json:"protocol_version,omitempty"`
	Agents          []string `json:"agents,omitempty"`
	Roots           []string `json:"roots,omitempty"`
}

type enrollRequestBody struct {
	RequestedID string     `json:"requested_id"`
	Labels      []string   `json:"labels"`
	Host        enrollHost `json:"host"`
	SecretHash  string     `json:"secret_hash"`
}

type enrollRequestResp struct {
	EnrollID    string `json:"enroll_id"`
	PairingCode string `json:"pairing_code"`
	DecideUntil int64  `json:"decide_until"`
}

// enrollPollResp is the applicant's view. worker_token is present only for an accepted
// request inside its collect window; there is deliberately no connect URL.
type enrollPollResp struct {
	State        string `json:"state"`
	WorkerID     string `json:"worker_id,omitempty"`
	WorkerToken  string `json:"worker_token,omitempty"`
	CollectUntil int64  `json:"collect_until,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// enrollItemView is one row of the admin list: no secret_hash, token_fp or pairing code.
type enrollItemView struct {
	ID           string     `json:"id"`
	State        string     `json:"state"`
	RequestedID  string     `json:"requested_id,omitempty"`
	WorkerID     string     `json:"worker_id,omitempty"`
	Labels       []string   `json:"labels"`
	Projects     []string   `json:"projects"`
	Host         enrollHost `json:"host"`
	Source       string     `json:"source"`
	CreatedAt    int64      `json:"created_at"`
	DecideUntil  int64      `json:"decide_until"`
	CollectUntil int64      `json:"collect_until,omitempty"`
	DecidedAt    int64      `json:"decided_at,omitempty"`
	DecidedBy    string     `json:"decided_by,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	CollectedAt  int64      `json:"collected_at,omitempty"`
}

type enrollListResp struct {
	Enabled      bool             `json:"enabled"`
	PendingCount int              `json:"pending_count"`
	Items        []enrollItemView `json:"items"`
}

type enrollAcceptBody struct {
	PairingCode string   `json:"pairing_code"`
	WorkerID    string   `json:"worker_id"`
	Labels      []string `json:"labels"`
	Projects    []string `json:"projects"`
}

type enrollRejectBody struct {
	Reason         string `json:"reason"`
	BlockSourceMin int    `json:"block_source_min"`
}

func enrollNoStore(c *rux.Context) { c.Resp.Header().Set("Cache-Control", "no-store") }

func enrollNotFound(c *rux.Context) {
	writeError(c, http.StatusNotFound, "not found", "enrollment not found")
}

// enrollGate answers 404 (and returns false) when the feature is off or not assembled;
// it touches no database.
func (s *Server) enrollGate(c *rux.Context) bool {
	if s.enroll == nil || !s.enroll.Settings().Enabled {
		writeError(c, http.StatusNotFound, "not found", "worker enrollment is disabled on this server")
		return false
	}
	return true
}

// enrollSource resolves the requesting address: the connection's host, or, only when the
// connection comes from a configured trusted proxy, the right-most X-Forwarded-For hop
// that is not itself trusted. The existing clientIP helper is NOT used: it trusts the
// first forwarded hop unconditionally.
func (s *Server) enrollSource(r *http.Request) string {
	var trusted []netip.Prefix
	if sc := s.serverCfg(); sc != nil {
		trusted = sc.WorkerEnrollment.TrustedProxyPrefixes()
	}
	return enrollSourceOf(r.RemoteAddr, r.Header.Values("X-Forwarded-For"), trusted)
}

func enrollSourceOf(remoteAddr string, xff []string, trusted []netip.Prefix) string {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	remote, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return host
	}
	remote = remote.Unmap().WithZone("")
	if !addrInPrefixes(remote, trusted) {
		return remote.String()
	}
	var hops []string
	for _, v := range xff {
		for _, p := range strings.Split(v, ",") {
			hops = append(hops, strings.TrimSpace(p))
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(hops[i])
		if err != nil {
			// A garbled hop from a trusted proxy: do not guess, keep the connection address.
			return remote.String()
		}
		a = a.Unmap().WithZone("")
		if !addrInPrefixes(a, trusted) {
			return a.String()
		}
	}
	return remote.String()
}

func addrInPrefixes(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// enrollReadBody reads a bounded body under a short read deadline. The deadline is
// cleared again afterwards: a short deadline left on the connection would also break
// the server's background read that notices a client going away.
func enrollReadBody(c *rux.Context) ([]byte, error) {
	rc := http.NewResponseController(c.Resp)
	_ = rc.SetReadDeadline(time.Now().Add(enrollReadTimeout)) // unsupported writers (tests) just skip it
	defer func() { _ = rc.SetReadDeadline(time.Time{}) }()
	return io.ReadAll(http.MaxBytesReader(c.Resp, c.Req.Body, enrollMaxBody))
}

func enrollBadRequest(c *rux.Context, detail string) {
	writeError(c, http.StatusBadRequest, "invalid request", detail)
}

func decodeStrict(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

// visibleASCII reports whether s is non-control printable ASCII (space allowed).
func visibleASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func labelChars(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		ok := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '.' || b == '_' || b == '-'
		if !ok {
			return false
		}
	}
	return true
}

// cleanTags validates a labels / agents style list.
func cleanTags(name string, in []string, maxN, maxLen int) ([]string, error) {
	if len(in) > maxN {
		return nil, errors.New(name + ": at most " + strconv.Itoa(maxN) + " entries")
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if len(v) > maxLen || !labelChars(v) {
			return nil, errors.New(name + ": entries must be 1-" + strconv.Itoa(maxLen) + " characters of [A-Za-z0-9._-]")
		}
		out = append(out, v)
	}
	return out, nil
}

func cleanASCIIField(name, v string) (string, error) {
	v = strings.TrimSpace(v)
	if len(v) > enrollMaxFieldLen || !visibleASCII(v) {
		return "", errors.New(name + ": at most " + strconv.Itoa(enrollMaxFieldLen) + " visible ASCII characters")
	}
	return v, nil
}

func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func cleanHost(h enrollHost) (enrollHost, error) {
	var out enrollHost
	var err error
	if out.Hostname, err = cleanASCIIField("host.hostname", h.Hostname); err != nil {
		return out, err
	}
	if out.OS, err = cleanASCIIField("host.os", h.OS); err != nil {
		return out, err
	}
	if out.Arch, err = cleanASCIIField("host.arch", h.Arch); err != nil {
		return out, err
	}
	if out.GoferVersion, err = cleanASCIIField("host.gofer_version", h.GoferVersion); err != nil {
		return out, err
	}
	if out.ProtocolVersion, err = cleanASCIIField("host.protocol_version", h.ProtocolVersion); err != nil {
		return out, err
	}
	if out.Agents, err = cleanTags("host.agents", h.Agents, enrollMaxAgents, enrollMaxFieldLen); err != nil {
		return out, err
	}
	if len(h.Roots) > enrollMaxRoots {
		return out, errors.New("host.roots: at most " + strconv.Itoa(enrollMaxRoots) + " entries")
	}
	for _, r := range h.Roots {
		r = strings.TrimSpace(stripControl(r))
		if r == "" {
			continue
		}
		if len([]rune(r)) > enrollMaxRootLen {
			return out, errors.New("host.roots: each entry at most " + strconv.Itoa(enrollMaxRootLen) + " characters")
		}
		out.Roots = append(out.Roots, r)
	}
	return out, nil
}

// checkEnrollWorkerID applies the worker id rules (pattern + reserved names).
func checkEnrollWorkerID(id string) error {
	if !workerIDPattern.MatchString(id) {
		return errors.New("worker id must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	return config.CheckWorkerID(id)
}

// handleWorkerEnrollRequest is POST /v1/worker-enroll (anonymous).
func (s *Server) handleWorkerEnrollRequest(c *rux.Context) {
	enrollNoStore(c)
	if !s.enrollGate(c) {
		return
	}
	raw, err := enrollReadBody(c)
	if err != nil {
		enrollBadRequest(c, "request body is unreadable or larger than 8 KiB")
		return
	}
	var body enrollRequestBody
	if err := decodeStrict(raw, &body); err != nil {
		enrollBadRequest(c, "malformed JSON body")
		return
	}
	in, err := buildEnrollInput(body)
	if err != nil {
		enrollBadRequest(c, err.Error())
		return
	}
	in.Source = s.enrollSource(c.Req)
	res, err := s.enroll.Request(c.Req.Context(), in)
	if err != nil {
		s.writeEnrollError(c, err)
		return
	}
	c.JSON(http.StatusCreated, enrollRequestResp{EnrollID: res.EnrollID, PairingCode: res.PairingCode, DecideUntil: res.DecideUntil})
}

func buildEnrollInput(b enrollRequestBody) (workerenroll.RequestInput, error) {
	var in workerenroll.RequestInput
	hash := strings.ToLower(strings.TrimSpace(b.SecretHash))
	if len(hash) != 64 || !isLowerHex(hash) {
		return in, errors.New("secret_hash must be 64 hex characters")
	}
	if id := strings.TrimSpace(b.RequestedID); id != "" {
		if err := checkEnrollWorkerID(id); err != nil {
			return in, errors.New("requested_id: " + err.Error())
		}
		in.RequestedID = id
	}
	labels, err := cleanTags("labels", b.Labels, enrollMaxLabels, enrollMaxLabelLen)
	if err != nil {
		return in, err
	}
	host, err := cleanHost(b.Host)
	if err != nil {
		return in, err
	}
	hostJSON, err := json.Marshal(host)
	if err != nil {
		return in, err
	}
	in.Labels, in.HostJSON, in.SecretHash = labels, string(hostJSON), hash
	return in, nil
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}

// enrollSecret reads the enroll secret header; ok=false means the request cannot be
// authenticated and gets the uniform 404 without touching the store.
func enrollSecret(c *rux.Context) (string, bool) {
	v := c.Req.Header.Get(enrollSecretHeader)
	if v == "" || len(v) > enrollMaxSecretLen {
		return "", false
	}
	return v, true
}

// handleWorkerEnrollPoll is GET /v1/worker-enroll/{id}?wait=N (long poll, anonymous).
func (s *Server) handleWorkerEnrollPoll(c *rux.Context) {
	enrollNoStore(c)
	if !s.enrollGate(c) {
		return
	}
	wait := time.Duration(0)
	if q := strings.TrimSpace(c.Req.URL.Query().Get("wait")); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 0 {
			enrollBadRequest(c, "wait must be a non-negative number of seconds")
			return
		}
		wait = min(time.Duration(n)*time.Second, enrollMaxWait)
	}
	secret, ok := enrollSecret(c)
	if !ok {
		enrollNotFound(c)
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	ctx := c.Req.Context()
	deadline := time.Now().Add(wait)
	for {
		res, err := s.enroll.Poll(ctx, id, secret)
		if err != nil {
			s.writeEnrollError(c, err)
			return
		}
		left := time.Until(deadline)
		if res.State != jobstore.EnrollPending || left <= 0 {
			c.JSON(http.StatusOK, enrollPollResp{State: res.State, WorkerID: res.WorkerID,
				WorkerToken: res.Token, CollectUntil: res.CollectUntil, Reason: res.Reason})
			return
		}
		t := time.NewTimer(min(enrollPollInterval, left))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// handleWorkerEnrollConfirm is POST /v1/worker-enroll/{id}/confirm (anonymous).
func (s *Server) handleWorkerEnrollConfirm(c *rux.Context) {
	s.enrollApplicantAction(c, func(id, secret string) error {
		return s.enroll.Confirm(c.Req.Context(), id, secret)
	})
}

// handleWorkerEnrollCancel is POST /v1/worker-enroll/{id}/cancel (anonymous).
func (s *Server) handleWorkerEnrollCancel(c *rux.Context) {
	s.enrollApplicantAction(c, func(id, secret string) error {
		return s.enroll.Cancel(c.Req.Context(), id, secret)
	})
}

func (s *Server) enrollApplicantAction(c *rux.Context, act func(id, secret string) error) {
	enrollNoStore(c)
	if !s.enrollGate(c) {
		return
	}
	if _, err := enrollReadBody(c); err != nil { // bounded; the body carries nothing
		enrollBadRequest(c, "request body is unreadable or larger than 8 KiB")
		return
	}
	secret, ok := enrollSecret(c)
	if !ok {
		enrollNotFound(c)
		return
	}
	if err := act(strings.TrimSpace(c.Param("id")), secret); err != nil {
		s.writeEnrollError(c, err)
		return
	}
	c.Resp.WriteHeader(http.StatusNoContent)
}

// writeEnrollError maps service errors; see docs/reference/http-api.md.
func (s *Server) writeEnrollError(c *rux.Context, err error) {
	switch {
	case errors.Is(err, workerenroll.ErrNotFound):
		enrollNotFound(c)
	case errors.Is(err, workerenroll.ErrDisabled):
		writeError(c, http.StatusNotFound, "not found", "worker enrollment is disabled on this server")
	case errors.Is(err, workerenroll.ErrInvalidRequest):
		enrollBadRequest(c, err.Error())
	case errors.Is(err, workerenroll.ErrRateLimited), errors.Is(err, workerenroll.ErrSourceBlocked):
		c.Resp.Header().Set("Retry-After", "60")
		writeError(c, http.StatusTooManyRequests, "rate limited", "too many enrollment requests from this source; retry later")
	case errors.Is(err, workerenroll.ErrCapReached):
		c.Resp.Header().Set("Retry-After", "30")
		writeError(c, http.StatusTooManyRequests, "rate limited", "too many pending enrollments on this server; retry later")
	case errors.Is(err, workerenroll.ErrNotPending), errors.Is(err, workerenroll.ErrWrongState):
		writeError(c, http.StatusConflict, "conflict", "the enrollment is no longer pending or is in the wrong state for this action")
	case errors.Is(err, workerenroll.ErrTokenMismatch):
		writeError(c, http.StatusConflict, "conflict", "the registered worker no longer matches this enrollment")
	case errors.Is(err, workerenroll.ErrCodeMismatch):
		enrollBadRequest(c, "pairing code mismatch")
	case errors.Is(err, config.ErrWorkerAlreadyRegistered):
		writeError(c, http.StatusConflict, "worker already exists", "remove the existing worker first or choose another worker id")
	default:
		writeError(c, http.StatusInternalServerError, "enrollment failed", "internal error")
	}
}

func enrollView(e jobstore.WorkerEnrollment) enrollItemView {
	v := enrollItemView{ID: e.ID, State: e.State, RequestedID: e.RequestedID, WorkerID: e.WorkerID,
		Source: e.Source, CreatedAt: e.CreatedAt, DecideUntil: e.DecideUntil, CollectUntil: e.CollectUntil,
		DecidedAt: e.DecidedAt, DecidedBy: e.DecidedBy, Reason: e.Reason, CollectedAt: e.CollectedAt}
	_ = json.Unmarshal([]byte(e.LabelsJSON), &v.Labels)
	_ = json.Unmarshal([]byte(e.ProjectsJSON), &v.Projects)
	_ = json.Unmarshal([]byte(e.HostJSON), &v.Host)
	if v.Labels == nil {
		v.Labels = []string{}
	}
	if v.Projects == nil {
		v.Projects = []string{}
	}
	return v
}

// handleListWorkerEnrollments is GET /v1/worker-enrollments (worker.manage, people only).
func (s *Server) handleListWorkerEnrollments(c *rux.Context) {
	enrollNoStore(c)
	if !adminUserOnly(c, "list worker enrollments") {
		return
	}
	resp := enrollListResp{Items: []enrollItemView{}}
	if s.enroll != nil {
		resp.Enabled = s.enroll.Settings().Enabled
		rows, err := s.enroll.ListAdmin(enrollRecent)
		if err != nil {
			writeError(c, http.StatusInternalServerError, "list enrollments failed", "internal error")
			return
		}
		now := time.Now().Unix()
		for _, e := range rows {
			if e.State == jobstore.EnrollPending && now < e.DecideUntil {
				resp.PendingCount++
			}
			resp.Items = append(resp.Items, enrollView(e))
		}
	}
	c.JSON(http.StatusOK, resp)
}

// handleAcceptWorkerEnrollment is POST /v1/worker-enrollments/{id}/accept.
func (s *Server) handleAcceptWorkerEnrollment(c *rux.Context) {
	enrollNoStore(c)
	caller := callerFromCtx(c)
	if !adminUserOnly(c, "accept a worker enrollment") {
		return
	}
	if s.enroll == nil {
		writeError(c, http.StatusNotFound, "not found", "worker enrollment is not available on this server")
		return
	}
	var body enrollAcceptBody
	raw, err := enrollReadBody(c)
	if err == nil {
		err = decodeStrict(raw, &body)
	}
	if err != nil {
		enrollBadRequest(c, "malformed JSON body")
		return
	}
	in, err := buildAcceptInput(body)
	if err != nil {
		enrollBadRequest(c, err.Error())
		return
	}
	in.Actor = caller
	res, err := s.enroll.Accept(c.Req.Context(), strings.TrimSpace(c.Param("id")), in)
	if err != nil {
		s.writeEnrollError(c, err)
		return
	}
	s.recordConfigUpdate(caller, "workers", res.WorkerID, []string{"worker_id", "labels", "projects"})
	c.JSON(http.StatusOK, map[string]any{"worker_id": res.WorkerID, "collect_until": res.CollectUntil})
}

func buildAcceptInput(b enrollAcceptBody) (workerenroll.AcceptInput, error) {
	var in workerenroll.AcceptInput
	code := workerenroll.NormalizePairingCode(b.PairingCode)
	if code == "" || len(code) > 16 {
		return in, errors.New("pairing_code is required")
	}
	id := strings.TrimSpace(b.WorkerID)
	if err := checkEnrollWorkerID(id); err != nil {
		return in, errors.New("worker_id: " + err.Error())
	}
	labels, err := cleanTags("labels", b.Labels, enrollMaxLabels, enrollMaxLabelLen)
	if err != nil {
		return in, err
	}
	if len(b.Projects) > enrollMaxProjects {
		return in, errors.New("projects: at most " + strconv.Itoa(enrollMaxProjects) + " entries")
	}
	projects := make([]string, 0, len(b.Projects))
	for _, p := range b.Projects {
		p = strings.TrimSpace(p)
		if p == "" || len(p) > enrollMaxFieldLen || !visibleASCII(p) {
			return in, errors.New("projects: entries must be 1-64 visible ASCII characters")
		}
		projects = append(projects, p)
	}
	return workerenroll.AcceptInput{PairingCode: code, WorkerID: id, Labels: labels, Projects: projects}, nil
}

// handleRejectWorkerEnrollment is POST /v1/worker-enrollments/{id}/reject.
func (s *Server) handleRejectWorkerEnrollment(c *rux.Context) {
	enrollNoStore(c)
	caller := callerFromCtx(c)
	if !adminUserOnly(c, "reject a worker enrollment") {
		return
	}
	if s.enroll == nil {
		writeError(c, http.StatusNotFound, "not found", "worker enrollment is not available on this server")
		return
	}
	var body enrollRejectBody
	raw, err := enrollReadBody(c)
	if err == nil && len(bytes.TrimSpace(raw)) > 0 {
		err = decodeStrict(raw, &body)
	}
	if err != nil {
		enrollBadRequest(c, "malformed JSON body")
		return
	}
	reason := strings.TrimSpace(stripControl(body.Reason))
	if len([]rune(reason)) > 200 {
		enrollBadRequest(c, "reason: at most 200 characters")
		return
	}
	if body.BlockSourceMin < 0 || body.BlockSourceMin > enrollMaxBlockMin {
		enrollBadRequest(c, "block_source_min must be between 0 and 1440")
		return
	}
	err = s.enroll.Reject(c.Req.Context(), strings.TrimSpace(c.Param("id")), workerenroll.RejectInput{
		Reason: reason, Actor: caller, BlockSourceMin: body.BlockSourceMin})
	if err != nil {
		s.writeEnrollError(c, err)
		return
	}
	c.Resp.WriteHeader(http.StatusNoContent)
}
