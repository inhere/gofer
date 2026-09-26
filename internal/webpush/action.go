package webpush

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	actionKeyBytes   = 32
	actionNonceBytes = 16
	actionTTL        = 10 * time.Minute
)

var (
	ErrActionUnauthorized = errors.New("webpush: invalid action token")
	ErrActionExpired      = errors.New("webpush: action token expired")
	ErrActionUsed         = errors.New("webpush: action token already used")
	ErrActionOption       = errors.New("webpush: action option is not allowed")
)

// ActionClaims is the authenticated, self-contained part of an OS notification
// action. The nonce registry supplies the one-time property; the HMAC supplies
// integrity and process identity.
type ActionClaims struct {
	CallerID      string   `json:"caller"`
	JobID         string   `json:"job"`
	InteractionID string   `json:"interaction"`
	Options       []string `json:"options"`
	ExpiresAt     int64    `json:"exp"`
	Nonce         string   `json:"nonce"`
}

type actionTokens struct {
	mu     sync.Mutex
	key    []byte
	issued map[string]int64
	random io.Reader
	now    func() time.Time
}

func newActionTokens(random io.Reader, now func() time.Time) (*actionTokens, error) {
	if random == nil {
		return nil, errors.New("webpush: action random source is required")
	}
	if now == nil {
		now = time.Now
	}
	key := make([]byte, actionKeyBytes)
	if _, err := io.ReadFull(random, key); err != nil {
		return nil, fmt.Errorf("webpush: generate action key: %w", err)
	}
	return &actionTokens{
		key: key, issued: make(map[string]int64), random: random, now: now,
	}, nil
}

func (a *actionTokens) mint(callerID, jobID, interactionID string, options []string) (string, error) {
	if strings.TrimSpace(callerID) == "" || strings.TrimSpace(jobID) == "" ||
		strings.TrimSpace(interactionID) == "" {
		return "", errors.New("webpush: action token identity is incomplete")
	}
	allowed := uniqueNonEmpty(options)
	if len(allowed) == 0 {
		return "", errors.New("webpush: action token has no allowed option")
	}
	nonceBytes := make([]byte, actionNonceBytes)
	if _, err := io.ReadFull(a.random, nonceBytes); err != nil {
		return "", fmt.Errorf("webpush: generate action nonce: %w", err)
	}
	claims := ActionClaims{
		CallerID: callerID, JobID: jobID, InteractionID: interactionID,
		Options: allowed, ExpiresAt: a.now().Add(actionTTL).Unix(),
		Nonce: base64.RawURLEncoding.EncodeToString(nonceBytes),
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("webpush: encode action token: %w", err)
	}
	a.mu.Lock()
	a.pruneLocked(a.now().Unix())
	a.issued[claims.Nonce] = claims.ExpiresAt
	a.mu.Unlock()
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(a.sign([]byte(encoded))), nil
}

func (a *actionTokens) consume(token, option string) (ActionClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ActionClaims{}, ErrActionUnauthorized
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, a.sign([]byte(parts[0]))) {
		return ActionClaims{}, ErrActionUnauthorized
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ActionClaims{}, ErrActionUnauthorized
	}
	var claims ActionClaims
	if err := json.Unmarshal(payload, &claims); err != nil ||
		claims.CallerID == "" || claims.JobID == "" || claims.InteractionID == "" ||
		claims.Nonce == "" || claims.ExpiresAt <= 0 {
		return ActionClaims{}, ErrActionUnauthorized
	}
	if !containsOption(claims.Options, option) {
		return ActionClaims{}, ErrActionOption
	}
	now := a.now().Unix()
	if now >= claims.ExpiresAt {
		a.mu.Lock()
		delete(a.issued, claims.Nonce)
		a.pruneLocked(now)
		a.mu.Unlock()
		return ActionClaims{}, ErrActionExpired
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked(now)
	if _, ok := a.issued[claims.Nonce]; !ok {
		return ActionClaims{}, ErrActionUsed
	}
	// Claim before answering. A concurrent replay can no longer pass this point,
	// even if the authoritative interaction has already changed state.
	delete(a.issued, claims.Nonce)
	return claims, nil
}

func (a *actionTokens) prune() {
	a.mu.Lock()
	a.pruneLocked(a.now().Unix())
	a.mu.Unlock()
}

func (a *actionTokens) pruneLocked(now int64) {
	for nonce, expires := range a.issued {
		if expires <= now {
			delete(a.issued, nonce)
		}
	}
}

func (a *actionTokens) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, a.key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func containsOption(options []string, want string) bool {
	for _, option := range options {
		if option == want {
			return true
		}
	}
	return false
}
