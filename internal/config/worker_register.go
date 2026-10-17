package config

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
)

var workerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var (
	// ErrWorkerAlreadyRegistered is returned by RegisterWorkerInto when server.workers already has the id.
	ErrWorkerAlreadyRegistered = errors.New("worker already registered")
	// ErrWorkerNotFound is returned by RemoveWorkerFrom when server.workers has no such id.
	ErrWorkerNotFound = errors.New("worker not found")
	// ErrWorkerFingerprintMismatch is returned by RemoveWorkerFrom when the expected token fingerprint differs.
	ErrWorkerFingerprintMismatch = errors.New("worker token fingerprint mismatch")
)

// NewWorkerToken generates a random worker token.
func NewWorkerToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gw_" + hex.EncodeToString(b), nil
}

// WorkerTokenFingerprint returns the hex SHA-256 of a worker token.
func WorkerTokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RegisterWorkerInto registers worker id in cfg: server.workers, runners and the
// allowed_runners of each listed project. cfg is only mutated on success.
func RegisterWorkerInto(cfg *Config, id string, labels, projects []string, token string) error {
	if !workerIDPattern.MatchString(id) {
		return fmt.Errorf("worker_id must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	if err := CheckWorkerID(id); err != nil {
		return err
	}
	if cfg.Server.Workers == nil {
		cfg.Server.Workers = map[string]WorkerAuthConfig{}
	}
	if _, exists := cfg.Server.Workers[id]; exists {
		return ErrWorkerAlreadyRegistered
	}
	if cfg.Runners == nil {
		cfg.Runners = map[string]RunnerConfig{}
	}
	if rc, exists := cfg.Runners[id]; exists && (rc.Type != "worker" || rc.WorkerID != id) {
		return fmt.Errorf("runner %q already exists", id)
	}
	for _, projectKey := range projects {
		p, ok := cfg.Projects[projectKey]
		if !ok {
			return fmt.Errorf("project %q not found", projectKey)
		}
		if !containsString(p.AllowedRunners, id) {
			// An absent allowlist means "local only" (job.checkRunnerAllowed);
			// spell that out first so adding a worker never revokes local.
			if len(p.AllowedRunners) == 0 {
				p.AllowedRunners = []string{BuiltinLocalRunner}
			}
			p.AllowedRunners = append(p.AllowedRunners, id)
			cfg.Projects[projectKey] = p
		}
	}
	cfg.Server.Workers[id] = WorkerAuthConfig{Token: token, Labels: append([]string(nil), labels...)}
	cfg.Runners[id] = RunnerConfig{Type: "worker", WorkerID: id}
	return nil
}

// RemoveWorkerFrom removes worker id from server.workers, runners and project
// allowed_runners. A non-empty expectFingerprint must equal the fingerprint of
// the configured token, otherwise cfg is untouched and removed is false.
func RemoveWorkerFrom(cfg *Config, id, expectFingerprint string) (removed bool, err error) {
	w, ok := cfg.Server.Workers[id]
	if !ok {
		return false, ErrWorkerNotFound
	}
	if expectFingerprint != "" &&
		subtle.ConstantTimeCompare([]byte(WorkerTokenFingerprint(w.Token)), []byte(expectFingerprint)) != 1 {
		return false, ErrWorkerFingerprintMismatch
	}
	delete(cfg.Server.Workers, id)
	for key, rc := range cfg.Runners {
		if key == id || (rc.Type == "worker" && rc.WorkerID == id) {
			delete(cfg.Runners, key)
		}
	}
	for key, p := range cfg.Projects {
		if len(p.AllowedRunners) == 0 {
			continue
		}
		filtered := p.AllowedRunners[:0]
		for _, runner := range p.AllowedRunners {
			if runner != id {
				filtered = append(filtered, runner)
			}
		}
		p.AllowedRunners = filtered
		cfg.Projects[key] = p
	}
	return true, nil
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
