package ptyrelay

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// NonceBinding is the serve-side binding encoded by an issued relay nonce. The
// nonce itself is opaque; callers validate these fields after Consume.
type NonceBinding struct {
	WorkerID     string
	InstanceID   string
	JobID        string
	PtySessionID string
	Expiry       int64
}

// NonceStore holds one-time relay nonces in memory. It is live-only: process
// restart drops all entries, matching the relay registry lifecycle.
type NonceStore struct {
	mu      sync.Mutex
	entries map[string]NonceBinding
}

// NewNonceStore returns an empty in-memory nonce store.
func NewNonceStore() *NonceStore {
	return &NonceStore{entries: map[string]NonceBinding{}}
}

// Issue stores b and returns a cryptographically-random one-time token.
func (s *NonceStore) Issue(b NonceBinding) string {
	if s == nil {
		return ""
	}
	nonce := randomNonce()
	s.mu.Lock()
	s.sweepExpiredLocked(time.Now().Unix())
	for {
		if _, exists := s.entries[nonce]; !exists {
			s.entries[nonce] = b
			s.mu.Unlock()
			return nonce
		}
		nonce = randomNonce()
	}
}

// Consume atomically removes nonce and returns its binding. Expired nonces are
// deleted and reported as missing. nowUnix is injected by the caller for
// deterministic tests.
func (s *NonceStore) Consume(nonce string, nowUnix int64) (NonceBinding, bool) {
	if s == nil || nonce == "" {
		return NonceBinding{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.entries[nonce]
	if !ok {
		return NonceBinding{}, false
	}
	delete(s.entries, nonce)
	if b.Expiry < nowUnix {
		return NonceBinding{}, false
	}
	return b, true
}

func (s *NonceStore) sweepExpiredLocked(nowUnix int64) {
	for nonce, b := range s.entries {
		if b.Expiry < nowUnix {
			delete(s.entries, nonce)
		}
	}
}

func randomNonce() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
