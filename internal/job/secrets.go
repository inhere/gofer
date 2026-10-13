package job

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/inhere/gofer/internal/config"
)

// Job secrets (JOB-06②, docs/design/2026-10-13-job-secret-design.md): the request
// names the secrets, the executing machine resolves their values from its own
// environment into the job's, and the job's logs are masked as they are written.

// EventJobSecretsGranted records which secrets a job was given: {names}.
const EventJobSecretsGranted = "job.secrets_granted"

// secretMaskText replaces a secret value in a job's logs.
const secretMaskText = "***"

// checkSecrets validates a submit's --secret names against the project allowlist and
// the shapes that cannot carry a secret.
func checkSecrets(cfg *config.Config, proj config.ProjectConfig, req JobRequest, remote bool) error {
	if len(req.Secrets) == 0 {
		return nil
	}
	if remote {
		return fmt.Errorf("%w: --secret is only supported on the server's own runner for now (runner %q is remote)", ErrInvalidRequest, req.Runner)
	}
	if req.Interactive {
		return fmt.Errorf("%w: --secret cannot be combined with --interactive (the terminal recording is not masked)", ErrInvalidRequest)
	}
	for _, name := range req.Secrets {
		if _, ok := cfg.Secrets[name]; !ok {
			return fmt.Errorf("%w: secret %q is not declared (top-level secrets in the server config)", ErrInvalidRequest, name)
		}
		if !slices.Contains(proj.Secrets, name) {
			return fmt.Errorf("%w: project %q does not allow secret %q (projects.%s.secrets)", ErrInvalidRequest, req.ProjectKey, name, req.ProjectKey)
		}
	}
	return nil
}

// resolveSecrets reads the values of the requested secrets on this machine: the job
// environment to add and the values to mask in its logs.
func resolveSecrets(cfg *config.Config, names []string) (env map[string]string, values []string, err error) {
	if len(names) == 0 {
		return nil, nil, nil
	}
	env = make(map[string]string, len(names))
	for _, name := range names {
		v, value, rerr := cfg.ResolveSecretValue(name)
		if rerr != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrInvalidRequest, rerr)
		}
		env[v] = value
		values = append(values, value)
	}
	return env, values, nil
}

// normalizeSecretNames trims and de-duplicates the requested names (order kept).
func normalizeSecretNames(in []string) []string {
	var out []string
	for _, n := range in {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// secretMasker writes through to w with every secret value replaced by ***. The
// tail of each write that could be the start of a value split across two writes is
// held back until the next write or Close.
type secretMasker struct {
	w        io.WriteCloser
	values   [][]byte
	keep     int // longest value - 1: the bytes that may still grow into a value
	pending  []byte
	closeErr error
	closed   bool
}

func newSecretMasker(w io.WriteCloser, values []string) io.WriteCloser {
	if len(values) == 0 {
		return w
	}
	m := &secretMasker{w: w}
	for _, v := range values {
		if v == "" {
			continue
		}
		m.values = append(m.values, []byte(v))
		if len(v)-1 > m.keep {
			m.keep = len(v) - 1
		}
	}
	// Longest first, so a value that contains another is masked whole.
	slices.SortFunc(m.values, func(a, b []byte) int { return len(b) - len(a) })
	return m
}

func (m *secretMasker) mask(b []byte) []byte {
	for _, v := range m.values {
		b = bytes.ReplaceAll(b, v, []byte(secretMaskText))
	}
	return b
}

func (m *secretMasker) Write(p []byte) (int, error) {
	buf := m.mask(append(m.pending, p...))
	cut := len(buf) - m.keep
	if cut <= 0 {
		m.pending = buf
		return len(p), nil
	}
	if _, err := m.w.Write(buf[:cut]); err != nil {
		return 0, err
	}
	m.pending = append(m.pending[:0], buf[cut:]...)
	return len(p), nil
}

// Close flushes what was held back and closes the underlying writer. A second
// Close only repeats the first one's result.
func (m *secretMasker) Close() error {
	if m.closed {
		return m.closeErr
	}
	m.closed = true
	var werr error
	if len(m.pending) > 0 {
		_, werr = m.w.Write(m.pending)
		m.pending = nil
	}
	m.closeErr = m.w.Close()
	if m.closeErr == nil {
		m.closeErr = werr
	}
	return m.closeErr
}
