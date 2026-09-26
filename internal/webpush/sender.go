package webpush

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

const pushTTLSeconds = 3600

type pushSender struct {
	keys                  *vapidKeys
	subject               string
	client                *http.Client
	random                io.Reader
	now                   func() time.Time
	allowInsecureLoopback bool
}

func newPushSender(configDir, subject string, client *http.Client, now func() time.Time, random io.Reader, allowLoopback bool) (*pushSender, error) {
	keys, err := newVAPIDKeys(configDir)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	return &pushSender{
		keys: keys, subject: subject, client: client, now: now, random: random,
		allowInsecureLoopback: allowLoopback,
	}, nil
}

func (s *pushSender) publicKey() (string, error) {
	key, err := s.keys.load()
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(vapidPublicKey(key)), nil
}

func (s *pushSender) send(sub jobstore.PushSubscription, payload []byte, urgency, topic string) (int, error) {
	endpoint, err := url.Parse(sub.Endpoint)
	if err != nil || endpoint.Host == "" {
		return 0, errors.New("webpush: invalid endpoint")
	}
	if endpoint.Scheme != "https" && !(s.allowInsecureLoopback && endpoint.Scheme == "http" && loopbackHost(endpoint.Hostname())) {
		return 0, errors.New("webpush: endpoint must use https")
	}
	receiverPublic, err := base64.RawURLEncoding.DecodeString(sub.P256DH)
	if err != nil {
		return 0, fmt.Errorf("webpush: decode p256dh: %w", err)
	}
	authSecret, err := base64.RawURLEncoding.DecodeString(sub.Auth)
	if err != nil {
		return 0, fmt.Errorf("webpush: decode auth: %w", err)
	}
	body, err := encryptForSubscription(payload, receiverPublic, authSecret, s.random)
	if err != nil {
		return 0, err
	}
	key, err := s.keys.load()
	if err != nil {
		return 0, err
	}
	token, err := signVAPIDJWT(key, s.subject, sub.Endpoint, s.now(), s.random)
	if err != nil {
		return 0, err
	}
	public := base64.RawURLEncoding.EncodeToString(vapidPublicKey(key))
	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("webpush: create request: %w", err)
	}
	req.Header.Set("Authorization", "vapid t="+token+", k="+public)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(pushTTLSeconds))
	if urgency != "" {
		req.Header.Set("Urgency", urgency)
	}
	if topic != "" {
		req.Header.Set("Topic", topic)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("webpush: post endpoint: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func topicForThread(threadID string) string {
	sum := sha256.Sum256([]byte(threadID))
	return base64.RawURLEncoding.EncodeToString(sum[:])[:32]
}
