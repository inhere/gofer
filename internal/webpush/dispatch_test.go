package webpush

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

type dispatchJobs struct {
	mu          sync.Mutex
	result      job.JobResult
	interaction job.Interaction
}

func (d *dispatchJobs) Get(id string) (job.JobResult, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id != d.result.ID {
		return job.JobResult{}, false
	}
	return d.result, true
}

func (d *dispatchJobs) GetInteractions(id string) ([]job.Interaction, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id != d.result.ID {
		return nil, nil
	}
	return []job.Interaction{d.interaction}, nil
}

func (d *dispatchJobs) AnswerInteractionByPush(jobID, interactionID, answer string) (job.Interaction, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if jobID != d.result.ID || interactionID != d.interaction.ID {
		return job.Interaction{}, job.ErrUnknownInteraction
	}
	if d.interaction.Status != job.InteractionPending {
		return job.Interaction{}, job.ErrInteractionState
	}
	d.interaction.Status = job.InteractionAnswered
	d.interaction.Answer = answer
	d.interaction.AnsweredBy = "push"
	return d.interaction, nil
}

type capturedPush struct {
	body    []byte
	headers http.Header
}

func waitPush(t *testing.T, ch <-chan capturedPush) capturedPush {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for push")
		return capturedPush{}
	}
}

func decryptPushPayload(t *testing.T, body []byte, receiverPrivate *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	if len(body) < 16+4+1 {
		t.Fatalf("encrypted body too short: %d", len(body))
	}
	salt := body[:16]
	_ = binary.BigEndian.Uint32(body[16:20])
	keyLen := int(body[20])
	if keyLen == 0 || len(body) < 21+keyLen {
		t.Fatalf("invalid key id length: %d", keyLen)
	}
	senderPublicRaw := body[21 : 21+keyLen]
	ciphertext := body[21+keyLen:]
	senderPublic, err := ecdh.P256().NewPublicKey(senderPublicRaw)
	if err != nil {
		t.Fatalf("sender public key: %v", err)
	}
	shared, err := receiverPrivate.ECDH(senderPublic)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		t.Fatalf("extract auth: %v", err)
	}
	keyInfo := append([]byte("WebPush: info\x00"), receiverPrivate.PublicKey().Bytes()...)
	keyInfo = append(keyInfo, senderPublicRaw...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	if err != nil {
		t.Fatalf("expand ikm: %v", err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		t.Fatalf("extract salt: %v", err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatalf("expand cek: %v", err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatalf("expand nonce: %v", err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatalf("AES: %v", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("GCM: %v", err)
	}
	plain, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatalf("missing final delimiter: %x", plain)
	}
	return plain[:len(plain)-1]
}

func TestPushDispatchOnInteraction(t *testing.T) {
	store, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	receiverPrivate, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("receiver key: %v", err)
	}
	auth := bytes.Repeat([]byte{0x42}, 16)
	p256dh := base64.RawURLEncoding.EncodeToString(receiverPrivate.PublicKey().Bytes())
	authText := base64.RawURLEncoding.EncodeToString(auth)

	goodCh := make(chan capturedPush, 4)
	goodServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		goodCh <- capturedPush{body: body, headers: r.Header.Clone()}
		w.WriteHeader(http.StatusCreated)
	}))
	defer goodServer.Close()
	goneServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer goneServer.Close()
	bobCh := make(chan capturedPush, 1)
	bobServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bobCh <- capturedPush{body: body, headers: r.Header.Clone()}
		w.WriteHeader(http.StatusCreated)
	}))
	defer bobServer.Close()

	for _, sub := range []jobstore.PushSubscription{
		{CallerID: "alice", Endpoint: goodServer.URL, P256DH: p256dh, Auth: authText, CreatedAt: 1},
		{CallerID: "alice", Endpoint: goneServer.URL, P256DH: p256dh, Auth: authText, CreatedAt: 2},
		{CallerID: "bob", Endpoint: bobServer.URL, P256DH: p256dh, Auth: authText, CreatedAt: 3},
	} {
		if err := store.UpsertPushSubscription(sub); err != nil {
			t.Fatalf("upsert subscription: %v", err)
		}
	}

	clock := time.Unix(1_800_000_000, 0)
	jobs := &dispatchJobs{
		result: job.JobResult{
			ID: "job-1", ProjectKey: "project-a", SessionID: "sess-1",
			Agent: "codex", Status: job.StatusPendingInteraction,
		},
		interaction: job.Interaction{
			ID: "interaction-1", JobID: "job-1", Type: job.InteractionTypePermission,
			Prompt: "allow edit?", Status: job.InteractionPending,
			Options: []job.InteractionOption{
				{Value: "allow-once", Kind: "allow_once", Label: "Allow once"},
				{Value: "reject-once", Kind: "reject_once", Label: "Reject once"},
			},
		},
	}
	service, err := NewService(Options{
		Store: store, Jobs: jobs, ConfigDir: t.TempDir(),
		HTTPClient: &http.Client{Timeout: time.Second},
		Now:        func() time.Time { return clock },
		UserCallers: func() []string {
			return []string{"alice", "bob"}
		},
		Visible: func(callerID, projectKey string) bool {
			return callerID == "alice" && projectKey == "project-a"
		},
		AllowInsecureLoopback: true,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	defer service.Close()

	service.ObserveEvent("job-1", job.EventInteractionCreated, map[string]any{
		"interaction_id": "interaction-1",
		"type":           job.InteractionTypePermission,
		"prompt":         "allow edit?",
	})
	push := waitPush(t, goodCh)
	if push.headers.Get("Content-Encoding") != "aes128gcm" ||
		push.headers.Get("TTL") != "3600" ||
		push.headers.Get("Urgency") != "high" ||
		push.headers.Get("Topic") == "" {
		t.Fatalf("push headers = %#v", push.headers)
	}
	var payload struct {
		ThreadID    string `json:"thread_id"`
		URL         string `json:"url"`
		ActionToken string `json:"action_token"`
		Actions     []struct {
			Action string `json:"action"`
			Option string `json:"option"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(decryptPushPayload(t, push.body, receiverPrivate, auth), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.ThreadID != "s:sess-1" || payload.URL != "/workbench?thread=s%3Asess-1" {
		t.Fatalf("payload route = %#v", payload)
	}
	if payload.ActionToken == "" || len(payload.Actions) != 2 {
		t.Fatalf("permission payload = %#v", payload)
	}

	select {
	case got := <-bobCh:
		t.Fatalf("invisible caller received push: %#v", got)
	case <-time.After(150 * time.Millisecond):
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rows, _ := store.ListPushSubscriptions("alice")
		if len(rows) == 1 && rows[0].Endpoint == goodServer.URL && rows[0].LastOKAt == clock.Unix() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	rows, _ := store.ListPushSubscriptions("alice")
	if len(rows) != 1 || rows[0].Endpoint != goodServer.URL {
		t.Fatalf("410 subscription not removed: %#v", rows)
	}

	// A second event in the same caller/thread window is merged, not delivered.
	service.ObserveEvent("job-1", job.EventInteractionCreated, map[string]any{
		"interaction_id": "interaction-1",
	})
	select {
	case got := <-goodCh:
		t.Fatalf("coalesced thread emitted a second push: %#v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestWebPushSessionAwaitingReply(t *testing.T) {
	store, err := jobstore.Open(filepath.Join(t.TempDir(), "gofer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	receiverPrivate, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := bytes.Repeat([]byte{0x52}, 16)
	gotCh := make(chan capturedPush, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotCh <- capturedPush{body: body, headers: r.Header.Clone()}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	if err := store.UpsertPushSubscription(jobstore.PushSubscription{CallerID: "alice", Endpoint: server.URL, P256DH: base64.RawURLEncoding.EncodeToString(receiverPrivate.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(auth), CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	jobs := &dispatchJobs{result: job.JobResult{ID: "job-2", ProjectKey: "project-a", SessionID: "sess-2", Title: "修复会话", Agent: "codex", Status: job.StatusAwaitingInput}}
	service, err := NewService(Options{Store: store, Jobs: jobs, ConfigDir: t.TempDir(), UserCallers: func() []string { return []string{"alice"} }, Visible: func(_, _ string) bool { return true }, AllowInsecureLoopback: true})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	service.ObserveEvent("job-2", job.EventSessionAwaitingReply, map[string]any{"turn_no": 3, "idle_deadline_at": int64(1800000300), "reply_preview": "请回复 A", "agent": "codex", "project": "project-a"})
	push := waitPush(t, gotCh)
	var payload PushPayload
	if err := json.Unmarshal(decryptPushPayload(t, push.body, receiverPrivate, auth), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Title != "会话等你回复：修复会话" || payload.URL != "/workbench?thread=s%3Asess-2" || !strings.Contains(payload.Body, "第 3 轮") || !strings.Contains(payload.Body, "请回复 A") {
		t.Fatalf("payload = %#v", payload)
	}
}
