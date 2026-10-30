package trigger

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

func TestVerifyHMACAndReplayWindow(t *testing.T) {
	const secret = "trigger-test-secret"
	t.Setenv("GOFER_TRIGGER_TEST", secret)
	tc := config.TriggerConfig{Auth: config.TriggerAuthConfig{SecretEnv: "GOFER_TRIGGER_TEST", ReplayWindowSec: 300}}
	body := []byte(`{"ref":"main"}`)
	ts := time.Unix(1000, 0)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("1000\n"))
	mac.Write(body)
	h := map[string]string{"X-Gofer-Trigger-Timestamp": "1000", "X-Gofer-Trigger-Signature": "sha256=" + hex.EncodeToString(mac.Sum(nil))}
	if err := VerifyHMAC(tc, body, h, ts); err != nil {
		t.Fatal(err)
	}
	if err := VerifyHMAC(tc, body, h, ts.Add(301*time.Second)); err != ErrReplay {
		t.Fatalf("replay error = %v", err)
	}
	_ = os.Getenv("GOFER_TRIGGER_TEST")
}

func TestEngineUsesOwnerAndFixedResources(t *testing.T) {
	var got job.JobRequest
	cfg := &config.Config{Server: config.ServerConfig{Triggers: []config.TriggerConfig{{Name: "push", Enabled: true, OwnerCaller: "automation", Project: "demo", Template: "source-update", Auth: config.TriggerAuthConfig{SecretEnv: "GOFER_TRIGGER_TEST"}, Input: config.TriggerInputConfig{Vars: map[string]string{"ref": "body.ref"}}}}}}
	t.Setenv("GOFER_TRIGGER_TEST", "s")
	body := []byte(`{"ref":"main"}`)
	mac := hmac.New(sha256.New, []byte("s"))
	mac.Write([]byte("1000\n"))
	mac.Write(body)
	eng := NewEngine(cfg, nil, func(_ context.Context, req job.JobRequest) (string, error) { got = req; return "job-1", nil })
	eng.SetClock(func() time.Time { return time.Unix(1000, 0) })
	res, err := eng.Invoke(context.Background(), TriggerRequest{TriggerName: "push", ExternalEventID: "delivery-1", RawBody: body, Payload: map[string]any{"ref": "main"}, Headers: map[string]string{"X-Gofer-Trigger-Timestamp": "1000", "X-Gofer-Trigger-Signature": "sha256=" + hex.EncodeToString(mac.Sum(nil))}})
	if err != nil {
		t.Fatal(err)
	}
	if res.JobID != "job-1" {
		t.Fatalf("job id=%q", res.JobID)
	}
	if got.CallerID != "" || got.OriginCaller != "automation" || got.ProjectKey != "demo" || got.Template != "source-update" || got.TemplateVars["ref"] != "main" {
		t.Fatalf("request=%+v", got)
	}
}
