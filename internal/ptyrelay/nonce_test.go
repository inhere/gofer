package ptyrelay

import "testing"

func TestNonceStoreIssueConsumeOnce(t *testing.T) {
	s := NewNonceStore()
	b := NonceBinding{WorkerID: "w1", InstanceID: "i1", JobID: "j1", PtySessionID: "p1", Expiry: 20}
	nonce := s.Issue(b)
	if nonce == "" {
		t.Fatal("Issue returned empty nonce")
	}
	got, ok := s.Consume(nonce, 10)
	if !ok {
		t.Fatal("first Consume returned false")
	}
	if got != b {
		t.Fatalf("binding = %+v, want %+v", got, b)
	}
	if _, ok := s.Consume(nonce, 10); ok {
		t.Fatal("second Consume returned true")
	}
}

func TestNonceStoreConsumeExpired(t *testing.T) {
	s := NewNonceStore()
	nonce := s.Issue(NonceBinding{WorkerID: "w1", Expiry: 9})
	if _, ok := s.Consume(nonce, 10); ok {
		t.Fatal("expired Consume returned true")
	}
	if _, ok := s.Consume(nonce, 10); ok {
		t.Fatal("expired nonce was not deleted")
	}
}
