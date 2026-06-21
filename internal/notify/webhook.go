// Package notify implements the E14 webhook outbound side (design §5.5–5.7):
// it validates a webhook URL against the outbound allowlist + SSRF rules, signs
// and POSTs the delivery body, and matches an event to its subscribed webhooks.
// It depends only on internal/config (a leaf) so internal/job can enqueue/POST
// deliveries without an import cycle.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// EventHeader / SignatureHeader are the headers a webhook POST carries. The
// signature is `sha256=<hex hmac>` over the exact request body (design §5.6);
// X-Gofer-Event echoes the triggering event type.
const (
	EventHeader     = "X-Gofer-Event"
	SignatureHeader = "X-Gofer-Signature"
)

// DefaultPostTimeoutSeconds bounds one webhook POST (SR603/§5.6). It is a hard
// upper bound on a single attempt; the sweeper retries on the backoff table.
const DefaultPostTimeoutSeconds = 5

// ValidateWebhookURL enforces the E14 outbound safety rules (design §5.7, SR904):
//   - the URL parses and has a host;
//   - the scheme is https (http only when cfg.AllowHTTP, for local testing);
//   - the host is in cfg.AllowHosts (when AllowHosts is non-empty; an empty
//     allowlist means "no host is permitted" — fail closed);
//   - every resolved IP is public: loopback / private / link-local / unique-local
//     / unspecified / multicast addresses are rejected (anti-SSRF).
//
// It performs DNS resolution, so a host that resolves only to internal addresses
// is rejected even if it is allowlisted by name (defence in depth). A host that
// does not resolve is rejected (cannot prove it is public).
func ValidateWebhookURL(raw string, cfg config.NotificationConfig) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid webhook url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && !(scheme == "http" && cfg.AllowHTTP) {
		return fmt.Errorf("webhook url scheme %q not allowed (https required%s)",
			u.Scheme, allowHTTPHint(cfg))
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("webhook url has no host: %q", raw)
	}
	if !hostAllowed(host, cfg.AllowHosts) {
		return fmt.Errorf("webhook host %q not in allow_hosts", host)
	}

	// SSRF: resolve and reject any non-public address. A literal IP host is
	// checked directly; a name is resolved (every A/AAAA must be public).
	ips, err := resolveHostIPs(host)
	if err != nil {
		return fmt.Errorf("webhook host %q resolve: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("webhook host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return fmt.Errorf("webhook host %q resolves to non-public address %s", host, ip)
		}
	}
	return nil
}

func allowHTTPHint(cfg config.NotificationConfig) string {
	if cfg.AllowHTTP {
		return ""
	}
	return "; set allow_http for local http"
}

// hostAllowed reports whether host matches the allowlist (case-insensitive exact
// host match). An empty allowlist fails closed (no host permitted) — the operator
// must explicitly list the webhook hosts.
func hostAllowed(host string, allow []string) bool {
	if len(allow) == 0 {
		return false
	}
	h := strings.ToLower(host)
	for _, a := range allow {
		if strings.ToLower(strings.TrimSpace(a)) == h {
			return true
		}
	}
	return false
}

// resolveHostIPs returns the IPs for host: a literal IP host parses directly,
// otherwise it is resolved via the default resolver.
func resolveHostIPs(host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if na, ok := netip.AddrFromSlice(a); ok {
			out = append(out, na.Unmap())
		}
	}
	return out, nil
}

// isPublicIP reports whether ip is a routable public address: it rejects
// loopback, private (RFC1918 / ULA), link-local, unspecified and multicast
// ranges (the SSRF blocklist, SR904).
func isPublicIP(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	return true
}

// Sign returns the hex HMAC-SHA256 of body under secret, formatted as the
// X-Gofer-Signature value `sha256=<hex>`. An empty secret yields "".
func Sign(body []byte, secret string) string {
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// PostWebhook validates target and POSTs body as application/json with the E14
// headers (design §5.6). It re-validates the URL on every send (config can have
// reloaded) and resolves the HMAC secret from secretEnv via os.Getenv (SR403 —
// secret is never logged or stored). The request runs under ctx (the sweeper
// gives it a per-attempt timeout) and does NOT follow redirects (anti-SSRF: a 3xx
// could bounce to an internal host). A 2xx is success (nil); any other status or
// a transport error is returned.
//
// eventType is stamped into X-Gofer-Event. secretValue is the already-resolved
// secret (the caller reads the env so this stays test-friendly / pure).
func PostWebhook(ctx context.Context, target, eventType string, body []byte, secretValue string, cfg config.NotificationConfig) error {
	if err := ValidateWebhookURL(target, cfg); err != nil {
		return err
	}
	return postRequest(ctx, target, eventType, body, secretValue)
}

// postRequest performs the signed POST without re-validating the URL. It is the
// transport half of PostWebhook (validation is the other half) — kept separate so
// the POST mechanics (headers / HMAC / status mapping / no-redirect) can be unit
// tested against an httptest loopback server, which ValidateWebhookURL would (by
// design) reject. PRODUCTION callers MUST go through PostWebhook so validation is
// never skipped.
func postRequest(ctx context.Context, target, eventType string, body []byte, secretValue string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(EventHeader, eventType)
	if sig := Sign(body, secretValue); sig != "" {
		req.Header.Set(SignatureHeader, sig)
	}

	client := &http.Client{
		// Never follow redirects: a 3xx must not bounce the POST to an unvalidated
		// (possibly internal) location.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook POST %s: %w", target, err)
	}
	defer resp.Body.Close()
	// Drain a little so the connection can be reused; ignore content.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook POST %s: status %d", target, resp.StatusCode)
	}
	return nil
}
