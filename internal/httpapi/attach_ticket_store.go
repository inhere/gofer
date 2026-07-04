package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
)

// AttachTicketBinding is the in-memory binding carried by a browser attach
// ticket. The ticket token itself is opaque and consumed once by the T7 WS path.
type AttachTicketBinding struct {
	Caller string
	JobID  string
	Mode   string
	Origin string
	Expiry int64
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

func randomAttachTicket() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
