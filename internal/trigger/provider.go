package trigger

import (
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// ProviderGeneric and ProviderGitHub are the adapter names (server.triggers[].provider).
const (
	ProviderGeneric = config.TriggerProviderGeneric
	ProviderGitHub  = config.TriggerProviderGitHub
)

// adapter is the provider seam of Engine.Invoke: everything that differs between a generic
// HMAC + timestamp sender and a vendor webhook. Its methods run in the order
// authenticate -> eventID -> filter -> vars, and the engine owns everything after them
// (limits, preflight, dedup claim, owner admission, dispatch), so every provider shares one
// dedup / rate / audit path.
type adapter interface {
	name() string
	// headers are the request headers the engine reads (Endpoint.Headers).
	headers(tc config.TriggerConfig) []string
	// authenticate verifies the sender over the raw body, before the body is parsed.
	// It returns ErrUnauthenticated or ErrReplay.
	authenticate(tc config.TriggerConfig, req TriggerRequest, now time.Time) error
	// eventID returns the external event id used as the durable dedup key ("" = none).
	// It runs after the payload is parsed.
	eventID(tc config.TriggerConfig, req TriggerRequest) (string, error)
	// filter decides whether an authenticated delivery should dispatch. A non-empty reason
	// means "acknowledge and do nothing"; an error refuses the delivery.
	filter(tc config.TriggerConfig, req TriggerRequest, now time.Time) (ignoreReason string, err error)
	// vars resolves the template variables from the declared input.vars.
	vars(tc config.TriggerConfig, req TriggerRequest) (map[string]string, error)
}

// adapterFor selects the adapter of a trigger; ok is false for a provider this build does
// not know (load-time validation makes that unreachable, the engine still refuses it).
func adapterFor(tc config.TriggerConfig) (adapter, bool) {
	switch tc.EffectiveProvider() {
	case ProviderGeneric:
		return genericAdapter{}, true
	case ProviderGitHub:
		return githubAdapter{}, true
	}
	return nil, false
}

// genericAdapter is the HMAC-SHA256 + timestamp scheme of design 3.5 (VerifyHMAC).
type genericAdapter struct{}

func (genericAdapter) name() string { return ProviderGeneric }

func (genericAdapter) headers(tc config.TriggerConfig) []string {
	var out []string
	add := func(h string) {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
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
	return out
}

func (genericAdapter) authenticate(tc config.TriggerConfig, req TriggerRequest, now time.Time) error {
	return VerifyHMAC(tc, req.RawBody, req.Headers, now)
}

func (genericAdapter) eventID(tc config.TriggerConfig, req TriggerRequest) (string, error) {
	return ExternalEventID(tc, req)
}

func (genericAdapter) filter(config.TriggerConfig, TriggerRequest, time.Time) (string, error) {
	return "", nil
}

func (genericAdapter) vars(tc config.TriggerConfig, req TriggerRequest) (map[string]string, error) {
	return resolveVars(tc, req)
}
