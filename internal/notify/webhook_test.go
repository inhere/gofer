package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// TestValidateWebhookURLLegal accepts an https URL whose host is allowlisted and
// is a public address. A public IP literal is used (not a domain) so the test
// never depends on DNS / network reachability.
func TestValidateWebhookURLLegal(t *testing.T) {
	cfg := config.NotificationConfig{AllowHosts: []string{"203.0.113.10"}} // TEST-NET-3 (public, non-private)
	if err := ValidateWebhookURL("https://203.0.113.10/gofer", cfg); err != nil {
		t.Fatalf("expected public IP literal to validate, got %v", err)
	}
}

// TestValidateWebhookURLRejected covers the SSRF / scheme / allowlist rejections.
func TestValidateWebhookURLRejected(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		cfg  config.NotificationConfig
	}{
		{
			name: "http when allow_http false",
			raw:  "http://example.com/x",
			cfg:  config.NotificationConfig{AllowHosts: []string{"example.com"}},
		},
		{
			name: "host not in allowlist",
			raw:  "https://evil.example.org/x",
			cfg:  config.NotificationConfig{AllowHosts: []string{"example.com"}},
		},
		{
			name: "empty allowlist fails closed",
			raw:  "https://example.com/x",
			cfg:  config.NotificationConfig{},
		},
		{
			name: "loopback literal",
			raw:  "https://127.0.0.1/x",
			cfg:  config.NotificationConfig{AllowHosts: []string{"127.0.0.1"}},
		},
		{
			name: "ipv6 loopback literal",
			raw:  "https://[::1]/x",
			cfg:  config.NotificationConfig{AllowHosts: []string{"::1"}},
		},
		{
			name: "private 10/8",
			raw:  "https://10.0.0.5/x",
			cfg:  config.NotificationConfig{AllowHosts: []string{"10.0.0.5"}},
		},
		{
			name: "private 192.168",
			raw:  "https://192.168.1.1/x",
			cfg:  config.NotificationConfig{AllowHosts: []string{"192.168.1.1"}},
		},
		{
			name: "private 172.16",
			raw:  "https://172.16.0.1/x",
			cfg:  config.NotificationConfig{AllowHosts: []string{"172.16.0.1"}},
		},
		{
			name: "link-local 169.254",
			raw:  "https://169.254.169.254/x", // cloud metadata endpoint
			cfg:  config.NotificationConfig{AllowHosts: []string{"169.254.169.254"}},
		},
		{
			name: "unspecified 0.0.0.0",
			raw:  "https://0.0.0.0/x",
			cfg:  config.NotificationConfig{AllowHosts: []string{"0.0.0.0"}},
		},
		{
			name: "no host",
			raw:  "https:///x",
			cfg:  config.NotificationConfig{AllowHosts: []string{""}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateWebhookURL(tc.raw, tc.cfg); err == nil {
				t.Fatalf("%s: expected rejection, got nil", tc.name)
			}
		})
	}
}

// TestValidateWebhookURLAllowHTTP lets a loopback-free http host through only
// when AllowHTTP is set (the local-testing escape hatch); the host must still be
// public + allowlisted.
func TestValidateWebhookURLAllowHTTP(t *testing.T) {
	cfg := config.NotificationConfig{AllowHosts: []string{"example.com"}, AllowHTTP: true}
	if err := ValidateWebhookURL("http://example.com/x", cfg); err != nil {
		t.Fatalf("allow_http should permit http://example.com, got %v", err)
	}
	// allow_http does NOT waive the SSRF blocklist.
	if err := ValidateWebhookURL("http://127.0.0.1/x", config.NotificationConfig{
		AllowHosts: []string{"127.0.0.1"}, AllowHTTP: true,
	}); err == nil {
		t.Fatal("allow_http must NOT permit loopback")
	}
}

// TestSign computes a recomputable HMAC.
func TestSign(t *testing.T) {
	if got := Sign([]byte("body"), ""); got != "" {
		t.Fatalf("empty secret should yield empty sig, got %q", got)
	}
	body := []byte(`{"a":1}`)
	got := Sign(body, "s3cr3t")
	mac := hmac.New(sha256.New, []byte("s3cr3t"))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got != want {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
}

// TestPostWebhookSuccess proves a 2xx is success and the signature + event header
// arrive correctly (HMAC recomputable over the EXACT received body). httptest
// binds loopback (which ValidateWebhookURL rejects by design — see the table test
// above), so the POST mechanics are driven through postRequest, the transport
// half of PostWebhook.
func TestPostWebhookSuccess(t *testing.T) {
	var (
		gotSig   string
		gotEvent string
		gotBody  []byte
		gotCT    string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get(SignatureHeader)
		gotEvent = r.Header.Get(EventHeader)
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	body := []byte(`{"event":{"seq":1}}`)
	secret := "topsecret"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := postRequest(ctx, srv.URL, "job.terminal", body, secret); err != nil {
		t.Fatalf("post: %v", err)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if gotEvent != "job.terminal" {
		t.Errorf("event header = %q", gotEvent)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBody)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotSig != want {
		t.Errorf("signature = %q, want %q (over received body %q)", gotSig, want, gotBody)
	}
}

// TestPostWebhookNoSignatureWithoutSecret asserts the signature header is omitted
// when no secret is configured.
func TestPostWebhookNoSignatureWithoutSecret(t *testing.T) {
	var sawSig bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawSig = r.Header[http.CanonicalHeaderKey(SignatureHeader)]
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := postRequest(ctx, srv.URL, "job.terminal", []byte("{}"), ""); err != nil {
		t.Fatalf("post: %v", err)
	}
	if sawSig {
		t.Fatal("signature header should be absent without a secret")
	}
}

// TestPostWebhookNon2xx maps a 500 to an error.
func TestPostWebhookNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := postRequest(ctx, srv.URL, "job.terminal", []byte("{}"), ""); err == nil {
		t.Fatal("expected error on 500")
	}
}

// TestPostWebhookTimeout proves a slow server hitting the ctx deadline errors.
func TestPostWebhookTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := postRequest(ctx, srv.URL, "job.terminal", []byte("{}"), ""); err == nil {
		t.Fatal("expected timeout error")
	}
}

// TestPostWebhookValidatesBeforeSend proves the full PostWebhook rejects a
// loopback target (validation runs first) without sending.
func TestPostWebhookValidatesBeforeSend(t *testing.T) {
	cfg := config.NotificationConfig{AllowHosts: []string{"127.0.0.1"}, AllowHTTP: true}
	ctx := context.Background()
	if err := PostWebhook(ctx, "http://127.0.0.1:1/x", "job.terminal", []byte("{}"), "", cfg); err == nil {
		t.Fatal("PostWebhook must reject loopback before sending")
	}
}
