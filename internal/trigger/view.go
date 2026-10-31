package trigger

import (
	"os"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// ProviderGeneric is the provider name of the generic HMAC + timestamp scheme.
const ProviderGeneric = "generic"

// MaxHeaderValueBytes bounds every request header the trigger reads (signature,
// timestamp, idempotency key and the headers input.vars / dedup.key refer to).
const MaxHeaderValueBytes = 4096

// eventHashDisplay is how much of the stored event hash the read views show.
const eventHashDisplay = 16

// deliveryErrorDisplay bounds the failure text of a delivery row.
const deliveryErrorDisplay = 200

// Endpoint is what an HTTP entry needs to know about a trigger BEFORE it reads the request
// body: how much it may read and which headers matter. It exists for enabled triggers only;
// a disabled or unknown name has no endpoint, which is how both answer the same 404.
type Endpoint struct {
	MaxBodyBytes int
	// Headers are the request headers the engine reads for this trigger (canonical or not;
	// matching is case-insensitive).
	Headers []string
}

// Endpoint resolves the named enabled trigger.
func (e *Engine) Endpoint(name string) (Endpoint, bool) {
	if e == nil {
		return Endpoint{}, false
	}
	tc, ok := e.enabledTrigger(name)
	if !ok {
		return Endpoint{}, false
	}
	return endpointOf(tc), true
}

func (e *Engine) enabledTrigger(name string) (config.TriggerConfig, bool) {
	cfg := e.config()
	if cfg == nil {
		return config.TriggerConfig{}, false
	}
	for i := range cfg.Server.Triggers {
		if cfg.Server.Triggers[i].Name == name && cfg.Server.Triggers[i].Enabled {
			return cfg.Server.Triggers[i].Clone(), true
		}
	}
	return config.TriggerConfig{}, false
}

func endpointOf(tc config.TriggerConfig) Endpoint {
	ep := Endpoint{MaxBodyBytes: tc.Limits.MaxBodyBytes}
	if ep.MaxBodyBytes == 0 {
		ep.MaxBodyBytes = config.DefaultTriggerMaxBodyBytes
	}
	add := func(h string) {
		if h = strings.TrimSpace(h); h != "" {
			ep.Headers = append(ep.Headers, h)
		}
	}
	add(orDefault(tc.Auth.TimestampHeader, "X-Gofer-Trigger-Timestamp"))
	add(orDefault(tc.Auth.SignatureHeader, "X-Gofer-Trigger-Signature"))
	add("Idempotency-Key")
	refHeader := func(ref string) {
		if name, ok := strings.CutPrefix(ref, "header."); ok {
			add(name)
		}
	}
	refHeader(tc.Dedup.Key)
	for _, ref := range tc.Input.Vars {
		refHeader(ref)
	}
	return ep
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// CollectHeaders reads the headers of the endpoint through get (a case-insensitive header
// lookup) and keeps only those: nothing else of the request reaches the engine. A value
// longer than MaxHeaderValueBytes is ErrHeaderTooLarge.
func (ep Endpoint) CollectHeaders(get func(string) string) (map[string]string, error) {
	out := make(map[string]string, len(ep.Headers))
	for _, name := range ep.Headers {
		v := get(name)
		if len(v) > MaxHeaderValueBytes {
			return nil, ErrHeaderTooLarge
		}
		if v != "" {
			out[name] = v
		}
	}
	return out, nil
}

// View is the redacted, operator-facing description of a trigger. It is what
// GET /v1/triggers, `gofer tool trigger` and the MCP tools show: the fixed resources, the
// policies, and whether the secret is configured. The secret value, the signature and any
// payload never appear.
type View struct {
	Name     string     `json:"name"`
	Enabled  bool       `json:"enabled"`
	Owner    string     `json:"owner_caller"`
	Project  string     `json:"project"`
	Template string     `json:"template"`
	Provider string     `json:"provider"`
	Auth     AuthView   `json:"auth"`
	Dedup    DedupView  `json:"dedup"`
	Rate     RateView   `json:"rate"`
	Limits   LimitsView `json:"limits"`
	// Inputs are the declared template variables and the payload field each is read from.
	Inputs map[string]string `json:"inputs,omitempty"`
	// Available is false while an enabled trigger cannot work on this server (see Issues).
	Available bool        `json:"available"`
	Issues    []IssueView `json:"issues,omitempty"`
	// Last is the newest delivery, nil when none is on record.
	Last *DeliveryView `json:"last_delivery,omitempty"`
}

// AuthView shows how a request is authenticated, never the secret.
type AuthView struct {
	Kind             string `json:"kind"`
	SecretEnv        string `json:"secret_env"`
	SecretConfigured bool   `json:"secret_configured"`
	TimestampHeader  string `json:"timestamp_header"`
	SignatureHeader  string `json:"signature_header"`
	ReplayWindowSec  int    `json:"replay_window_sec"`
}

type DedupView struct {
	Key    string `json:"key,omitempty"`
	TTLSec int    `json:"ttl_sec"`
}

type RateView struct {
	PerMinute   int `json:"per_minute"`
	Burst       int `json:"burst"`
	MaxInflight int `json:"max_inflight"`
}

type LimitsView struct {
	MaxBodyBytes int `json:"max_body_bytes"`
	MaxVarBytes  int `json:"max_var_bytes"`
}

// IssueView is one reason an enabled trigger is unavailable (config.TriggerIssue).
type IssueView struct {
	Code string `json:"code"`
	Arg  string `json:"arg,omitempty"`
}

// DeliveryView is one receipt as the read routes show it: the event id only as a truncated
// hash, the status, the job and the time; the payload is not stored and never shown.
type DeliveryView struct {
	EventHash  string `json:"event_hash"`
	Status     string `json:"status"`
	JobID      string `json:"job_id,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	ReceivedAt int64  `json:"received_at"`
	// Error is the (truncated) failure text of a failed or rejected delivery.
	Error string `json:"error,omitempty"`
}

func deliveryView(r jobstore.TriggerReceipt) DeliveryView {
	hash := r.ExternalEventID
	if len(hash) > eventHashDisplay {
		hash = hash[:eventHashDisplay]
	}
	msg := r.LastError
	if len(msg) > deliveryErrorDisplay {
		msg = msg[:deliveryErrorDisplay]
	}
	return DeliveryView{EventHash: hash, Status: r.Status, JobID: r.JobID, RequestID: r.RequestID, ReceivedAt: r.ReceivedAt, Error: msg}
}

// List describes every configured trigger (enabled or not) in configuration order.
func (e *Engine) List() []View {
	if e == nil {
		return nil
	}
	cfg := e.config()
	if cfg == nil {
		return nil
	}
	statuses := cfg.TriggerStatuses()
	out := make([]View, 0, len(cfg.Server.Triggers))
	for i := range cfg.Server.Triggers {
		out = append(out, e.view(cfg.Server.Triggers[i], statuses[i]))
	}
	return out
}

// Show describes one configured trigger (enabled or not).
func (e *Engine) Show(name string) (View, bool) {
	for _, v := range e.List() {
		if v.Name == name {
			return v, true
		}
	}
	return View{}, false
}

// Deliveries lists the newest receipts of a configured trigger. known is false for a name
// the configuration does not declare.
func (e *Engine) Deliveries(name string, limit int) (rows []DeliveryView, known bool, err error) {
	if _, known = e.Show(name); !known {
		return nil, false, nil
	}
	rows = []DeliveryView{}
	if e.store == nil {
		return rows, true, nil
	}
	recs, err := e.store.ListTriggerReceipts(name, limit)
	if err != nil {
		return nil, true, err
	}
	for _, r := range recs {
		rows = append(rows, deliveryView(r))
	}
	return rows, true, nil
}

func (e *Engine) view(tc config.TriggerConfig, st config.TriggerStatus) View {
	ttl := tc.Dedup.TTLSec
	if ttl == 0 {
		ttl = config.DefaultTriggerDedupTTLSec
	}
	window := tc.Auth.ReplayWindowSec
	if window == 0 {
		window = config.DefaultTriggerReplayWindowSec
	}
	kind := tc.Auth.Kind
	if kind == "" {
		kind = "hmac-sha256"
	}
	maxVar := tc.Limits.MaxVarBytes
	if maxVar == 0 {
		maxVar = config.DefaultTriggerMaxVarBytes
	}
	v := View{
		Name: tc.Name, Enabled: tc.Enabled, Owner: strings.TrimSpace(tc.OwnerCaller),
		Project: strings.TrimSpace(tc.Project), Template: strings.TrimSpace(tc.Template), Provider: ProviderGeneric,
		Auth: AuthView{Kind: kind, SecretEnv: tc.Auth.SecretEnv, SecretConfigured: tc.Auth.SecretEnv != "" && secretSet(tc.Auth.SecretEnv),
			TimestampHeader: orDefault(tc.Auth.TimestampHeader, "X-Gofer-Trigger-Timestamp"),
			SignatureHeader: orDefault(tc.Auth.SignatureHeader, "X-Gofer-Trigger-Signature"), ReplayWindowSec: window},
		Dedup:     DedupView{Key: tc.Dedup.Key, TTLSec: ttl},
		Rate:      RateView{PerMinute: tc.Rate.PerMinute, Burst: tc.Rate.Burst, MaxInflight: tc.Rate.MaxInflight},
		Limits:    LimitsView{MaxBodyBytes: endpointOf(tc).MaxBodyBytes, MaxVarBytes: maxVar},
		Available: st.Available(),
	}
	if len(tc.Input.Vars) > 0 {
		v.Inputs = make(map[string]string, len(tc.Input.Vars))
		for name, ref := range tc.Input.Vars {
			v.Inputs[name] = ref
		}
	}
	for _, is := range st.Issues {
		v.Issues = append(v.Issues, IssueView{Code: is.Code, Arg: is.Arg})
	}
	if e.store != nil {
		if recs, err := e.store.ListTriggerReceipts(tc.Name, 1); err == nil && len(recs) == 1 {
			d := deliveryView(recs[0])
			v.Last = &d
		}
	}
	return v
}

func secretSet(envName string) bool { return os.Getenv(envName) != "" }
