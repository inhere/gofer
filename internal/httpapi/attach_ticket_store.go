package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// AttachTicketBinding is the in-memory binding carried by a browser attach
// ticket. The ticket token itself is opaque and consumed once by the T7 WS path.
//
// The last three fields exist for the long-connection revocation of design §6: the WS handshake
// carries no Authorization header, so the ticket records what the handshake cannot ask again —
// the session the issuing request named, the permissions it was admitted on, and the database
// grants behind them (the dependency set).
type AttachTicketBinding struct {
	Caller       string
	JobID        string
	PtySessionID string
	Mode         string
	Origin       string
	Expiry       int64
	// Session is the terminal session the ticket's request named (empty when it named none).
	Session string
	// Token is the bearer token the issuing request authenticated with (the caller's credential,
	// so a reload that changes or removes it ends the connection the ticket opened).
	Token string
	// Perms are the permissions the issuing request rested on for this connection (empty = the
	// connection needs nothing but its identity: the caller's own job, or the push channel).
	Perms []string
	// Deps are the ids of the database grants behind those permissions.
	Deps []string
}

// AttachTicketStore holds short-lived one-time browser attach tickets.
type AttachTicketStore struct {
	mu      sync.Mutex
	entries map[string]AttachTicketBinding
}

func NewAttachTicketStore() *AttachTicketStore {
	return &AttachTicketStore{entries: map[string]AttachTicketBinding{}}
}

func (s *AttachTicketStore) Issue(b AttachTicketBinding) string {
	if s == nil {
		return ""
	}
	token := randomAttachTicket()
	s.mu.Lock()
	s.sweepExpiredLocked(time.Now().Unix())
	for {
		if _, exists := s.entries[token]; !exists {
			s.entries[token] = b
			s.mu.Unlock()
			return token
		}
		token = randomAttachTicket()
	}
}

func (s *AttachTicketStore) Consume(token string, nowUnix int64) (AttachTicketBinding, bool) {
	if s == nil || token == "" {
		return AttachTicketBinding{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.entries[token]
	if !ok {
		return AttachTicketBinding{}, false
	}
	delete(s.entries, token)
	if b.Expiry < nowUnix {
		return AttachTicketBinding{}, false
	}
	return b, true
}

func (s *AttachTicketStore) sweepExpiredLocked(nowUnix int64) {
	for token, b := range s.entries {
		if b.Expiry < nowUnix {
			delete(s.entries, token)
		}
	}
}

func randomAttachTicket() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
