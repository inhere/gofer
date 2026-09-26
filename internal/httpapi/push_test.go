package httpapi

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/webpush"
)

type pushTestClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *pushTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *pushTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func attachPushService(t *testing.T, s *Server, callers []string, clock *pushTestClock, allowLoopback bool) *webpush.Service {
	t.Helper()
	if clock == nil {
		clock = &pushTestClock{now: time.Unix(1_800_000_000, 0)}
	}
	service, err := webpush.NewService(webpush.Options{
		Store: s.jobs.Meta(), Jobs: s.jobs, ConfigDir: t.TempDir(),
		Now: func() time.Time { return clock.Now() },
		UserCallers: func() []string {
			return append([]string(nil), callers...)
		},
		Visible: func(_, projectKey string) bool {
			_, ok := s.projects.Get(projectKey)
			return ok
		},
		AllowInsecureLoopback: allowLoopback,
	})
	if err != nil {
		t.Fatalf("new webpush service: %v", err)
	}
	s.SetWebPush(service)
	s.jobs.AddEventObserver(service.ObserveEvent)
	t.Cleanup(service.Close)
	return service
}

func doPushRequest(t *testing.T, s *Server, method, path, token, userAgent string, body any) *http.Response {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

func newBrowserSubscription(t *testing.T, endpoint string) (map[string]any, *ecdh.PrivateKey, []byte) {
	t.Helper()
	privateKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate receiver key: %v", err)
	}
	auth := bytes.Repeat([]byte{0x27}, 16)
	return map[string]any{
		"endpoint": endpoint,
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(auth),
		},
	}, privateKey, auth
}

type pushSubscriptionList struct {
	Subscriptions []struct {
		Endpoint  string `json:"endpoint"`
		UserAgent string `json:"user_agent"`
		CreatedAt int64  `json:"created_at"`
		LastOKAt  int64  `json:"last_ok_at"`
	} `json:"subscriptions"`
}

func TestPushSubscriptionCRUD(t *testing.T) {
	const (
		aliceToken = "alice-token"
		bobToken   = "bob-token"
		endpoint   = "https://push.example.test/device-1"
	)
	s := newWorkbenchTestServer(t, config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "alice", Token: aliceToken},
		{ID: "bob", Token: bobToken},
	}})
	attachPushService(t, s, []string{"alice", "bob"}, nil, false)
	body, _, _ := newBrowserSubscription(t, endpoint)

	resp := doPushRequest(t, s, http.MethodPost, "/v1/push/subscriptions", aliceToken, "browser-a", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("alice subscribe status=%d, want 200", resp.StatusCode)
	}
	resp = doPushRequest(t, s, http.MethodGet, "/v1/push/subscriptions", aliceToken, "", nil)
	var alice pushSubscriptionList
	decode(t, resp, &alice)
	if len(alice.Subscriptions) != 1 || alice.Subscriptions[0].Endpoint != endpoint ||
		alice.Subscriptions[0].UserAgent != "browser-a" || alice.Subscriptions[0].LastOKAt != 0 {
		t.Fatalf("alice subscriptions = %#v", alice.Subscriptions)
	}

	resp = doPushRequest(t, s, http.MethodGet, "/v1/push/subscriptions", bobToken, "", nil)
	var bob pushSubscriptionList
	decode(t, resp, &bob)
	if len(bob.Subscriptions) != 0 {
		t.Fatalf("bob saw alice subscription: %#v", bob.Subscriptions)
	}

	// The same endpoint re-registered by bob transfers ownership.
	resp = doPushRequest(t, s, http.MethodPost, "/v1/push/subscriptions", bobToken, "browser-b", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob subscribe status=%d", resp.StatusCode)
	}
	resp = doPushRequest(t, s, http.MethodGet, "/v1/push/subscriptions", aliceToken, "", nil)
	alice = pushSubscriptionList{}
	decode(t, resp, &alice)
	if len(alice.Subscriptions) != 0 {
		t.Fatalf("alice retained transferred endpoint: %#v", alice.Subscriptions)
	}

	resp = doPushRequest(t, s, http.MethodDelete, "/v1/push/subscriptions", aliceToken, "", map[string]string{"endpoint": endpoint})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("alice delete bob endpoint=%d, want 404", resp.StatusCode)
	}
	resp = doPushRequest(t, s, http.MethodDelete, "/v1/push/subscriptions", bobToken, "", map[string]string{"endpoint": endpoint})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob delete endpoint=%d, want 200", resp.StatusCode)
	}
}

func createPushPermission(t *testing.T, s *Server, jobID, token string) job.Interaction {
	t.Helper()
	resp := do(t, s, http.MethodPost, "/v1/jobs/"+jobID+"/interactions", token, createInteractionReq{
		Type:   job.InteractionTypePermission,
		Prompt: "allow edit?",
		Options: []job.InteractionOption{
			{Value: "allow-once", Kind: "allow_once", Label: "Allow once"},
			{Value: "reject-once", Kind: "reject_once", Label: "Reject once"},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create permission status=%d", resp.StatusCode)
	}
	var interaction job.Interaction
	decode(t, resp, &interaction)
	return interaction
}

func actionStatus(t *testing.T, s *Server, token, option string) int {
	t.Helper()
	resp := doPushRequest(t, s, http.MethodPost, "/v1/push/actions", "", "", map[string]string{
		"token": token, "option": option,
	})
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestPushActionTokenAnswersOnce(t *testing.T) {
	clock := &pushTestClock{now: time.Unix(1_800_000_000, 0)}
	s := newTestServer(t, testToken, false)
	push := attachPushService(t, s, []string{"default"}, clock, false)
	jobID := submitRunningJob(t, s)

	interaction := createPushPermission(t, s, jobID, testToken)
	token, err := push.NewActionToken("default", jobID, interaction.ID, []string{"allow-once", "reject-once"})
	if err != nil {
		t.Fatalf("new action token: %v", err)
	}
	if got := actionStatus(t, s, token, "allow-once"); got != http.StatusOK {
		t.Fatalf("first action status=%d, want 200", got)
	}
	interactions, err := s.jobs.GetInteractions(jobID)
	if err != nil || len(interactions) == 0 {
		t.Fatalf("get interactions = %#v, %v", interactions, err)
	}
	if got := interactions[len(interactions)-1]; got.Answer != "allow-once" || got.AnsweredBy != "push" {
		t.Fatalf("answered interaction = %#v", got)
	}
	if got := actionStatus(t, s, token, "allow-once"); got != http.StatusConflict {
		t.Fatalf("replay status=%d, want 409", got)
	}

	expiring := createPushPermission(t, s, jobID, testToken)
	expiredToken, err := push.NewActionToken("default", jobID, expiring.ID, []string{"allow-once"})
	if err != nil {
		t.Fatalf("new expiring token: %v", err)
	}
	clock.Advance(11 * time.Minute)
	if got := actionStatus(t, s, expiredToken, "allow-once"); got != http.StatusConflict {
		t.Fatalf("expired status=%d, want 409", got)
	}

	optionInteraction := createPushPermission(t, s, jobID, testToken)
	optionToken, _ := push.NewActionToken("default", jobID, optionInteraction.ID, []string{"allow-once"})
	if got := actionStatus(t, s, optionToken, "reject-once"); got != http.StatusBadRequest {
		t.Fatalf("unlisted option status=%d, want 400", got)
	}
	if got := actionStatus(t, s, optionToken+"x", "allow-once"); got != http.StatusUnauthorized {
		t.Fatalf("tampered status=%d, want 401", got)
	}

	answered := createPushPermission(t, s, jobID, testToken)
	answeredToken, _ := push.NewActionToken("default", jobID, answered.ID, []string{"allow-once"})
	if _, err := s.jobs.AnswerInteractionByHuman(jobID, answered.ID, "allow-once", "operator"); err != nil {
		t.Fatalf("pre-answer interaction: %v", err)
	}
	if got := actionStatus(t, s, answeredToken, "allow-once"); got != http.StatusConflict {
		t.Fatalf("already answered status=%d, want 409", got)
	}
}

func TestPushJobCallerForbidden(t *testing.T) {
	const userToken = "operator-token"
	s := newCredentialServer(t,
		config.ServerConfig{Callers: []config.CallerConfig{{ID: "alice", Token: userToken}}},
		map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)
	attachPushService(t, s, []string{"alice"}, nil, false)
	jobResult := submitExecJob(t, s, userToken)
	jobToken := seedJobToken(t, s, jobResult.ID, jobstore.JobCredentialMember, "")
	subscription, _, _ := newBrowserSubscription(t, "https://push.example.test/device")

	denied := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/push/vapid-public-key", nil},
		{http.MethodGet, "/v1/push/subscriptions", nil},
		{http.MethodPost, "/v1/push/subscriptions", subscription},
		{http.MethodDelete, "/v1/push/subscriptions", map[string]string{"endpoint": "https://push.example.test/device"}},
		{http.MethodPost, "/v1/push/test", nil},
	}
	for _, tc := range denied {
		resp := doPushRequest(t, s, tc.method, tc.path, jobToken, "", tc.body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s as job caller=%d, want 403", tc.method, tc.path, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	resp := doPushRequest(t, s, http.MethodGet, "/v1/push/vapid-public-key", userToken, "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("user public key status=%d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func decryptHTTPPush(t *testing.T, body []byte, privateKey *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	if len(body) < 22 {
		t.Fatalf("encrypted body too short: %d", len(body))
	}
	salt := body[:16]
	keyLen := int(body[20])
	if len(body) < 21+keyLen {
		t.Fatalf("invalid key length %d", keyLen)
	}
	senderRaw := body[21 : 21+keyLen]
	sender, err := ecdh.P256().NewPublicKey(senderRaw)
	if err != nil {
		t.Fatalf("sender public: %v", err)
	}
	shared, err := privateKey.ECDH(sender)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	prkKey, err := hkdf.Extract(sha256.New, shared, auth)
	if err != nil {
		t.Fatal(err)
	}
	info := append([]byte("WebPush: info\x00"), privateKey.PublicKey().Bytes()...)
	info = append(info, senderRaw...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	if err != nil {
		t.Fatal(err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		t.Fatal(err)
	}
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	aead, _ := cipher.NewGCM(block)
	plain, err := aead.Open(nil, nonce, body[21+keyLen:], nil)
	if err != nil {
		t.Fatalf("decrypt push: %v", err)
	}
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		t.Fatalf("bad final delimiter: %x", plain)
	}
	return plain[:len(plain)-1]
}

func TestPushEndToEndSmoke(t *testing.T) {
	received := make(chan []byte, 1)
	pushEndpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.WriteHeader(http.StatusCreated)
	}))
	defer pushEndpoint.Close()

	s := newTestServer(t, testToken, false)
	attachPushService(t, s, []string{"default"}, nil, true)
	subscription, receiverPrivate, auth := newBrowserSubscription(t, pushEndpoint.URL)
	resp := doPushRequest(t, s, http.MethodPost, "/v1/push/subscriptions", testToken, "smoke-browser", subscription)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscribe status=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	jobID := submitRunningJob(t, s)
	interaction := createPushPermission(t, s, jobID, testToken)
	var encrypted []byte
	select {
	case encrypted = <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for encrypted push")
	}
	var payload struct {
		ThreadID    string `json:"thread_id"`
		ActionToken string `json:"action_token"`
		Actions     []struct {
			Option string `json:"option"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(decryptHTTPPush(t, encrypted, receiverPrivate, auth), &payload); err != nil {
		t.Fatalf("decode decrypted push: %v", err)
	}
	if payload.ThreadID == "" || payload.ActionToken == "" || len(payload.Actions) == 0 {
		t.Fatalf("decrypted payload = %#v", payload)
	}
	if !strings.HasPrefix(payload.ThreadID, "j:") && !strings.HasPrefix(payload.ThreadID, "s:") {
		t.Fatalf("invalid thread id %q", payload.ThreadID)
	}
	if got := actionStatus(t, s, payload.ActionToken, payload.Actions[0].Option); got != http.StatusOK {
		t.Fatalf("push action status=%d", got)
	}
	if got := actionStatus(t, s, payload.ActionToken, payload.Actions[0].Option); got != http.StatusConflict {
		t.Fatalf("push replay status=%d, want 409", got)
	}
	interactions, _ := s.jobs.GetInteractions(jobID)
	for _, got := range interactions {
		if got.ID == interaction.ID && got.AnsweredBy == "push" {
			return
		}
	}
	t.Fatalf("interaction was not answered by push: %#v", interactions)
}
