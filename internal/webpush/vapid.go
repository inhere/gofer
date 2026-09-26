package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultVAPIDSubject = "mailto:gofer@localhost"
	vapidFileName       = "vapid.json"
)

type vapidFile struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}

type vapidKeys struct {
	mu   sync.Mutex
	path string
	key  *ecdsa.PrivateKey
}

func newVAPIDKeys(configDir string) (*vapidKeys, error) {
	if strings.TrimSpace(configDir) == "" {
		return nil, errors.New("webpush: empty config directory")
	}
	return &vapidKeys{path: filepath.Join(configDir, "push", vapidFileName)}, nil
}

func (v *vapidKeys) load() (*ecdsa.PrivateKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key != nil {
		return v.key, nil
	}
	key, err := readVAPIDFile(v.path)
	if err == nil {
		v.key = key
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("webpush: generate VAPID key: %w", err)
	}
	if err := writeVAPIDFile(v.path, key); err != nil {
		return nil, err
	}
	v.key = key
	return key, nil
}

func readVAPIDFile(path string) (*ecdsa.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var record vapidFile
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("webpush: decode VAPID file: %w", err)
	}
	privateBytes, err := base64.RawURLEncoding.DecodeString(record.PrivateKey)
	if err != nil || len(privateBytes) != p256PrivateBytes {
		return nil, errors.New("webpush: invalid VAPID private key")
	}
	publicBytes, err := base64.RawURLEncoding.DecodeString(record.PublicKey)
	if err != nil || len(publicBytes) != p256PublicBytes {
		return nil, errors.New("webpush: invalid VAPID public key")
	}
	curve := elliptic.P256()
	d := new(big.Int).SetBytes(privateBytes)
	if d.Sign() <= 0 || d.Cmp(curve.Params().N) >= 0 {
		return nil, errors.New("webpush: VAPID private scalar out of range")
	}
	x, y := curve.ScalarBaseMult(privateBytes)
	derived := elliptic.Marshal(curve, x, y)
	if !equalBytes(derived, publicBytes) {
		return nil, errors.New("webpush: VAPID public key does not match private key")
	}
	_ = os.Chmod(path, 0o600)
	return &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: d}, nil
}

func writeVAPIDFile(path string, key *ecdsa.PrivateKey) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("webpush: create VAPID directory: %w", err)
	}
	privateBytes := key.D.FillBytes(make([]byte, p256PrivateBytes))
	publicBytes := elliptic.Marshal(elliptic.P256(), key.PublicKey.X, key.PublicKey.Y)
	body, err := json.MarshalIndent(vapidFile{
		PrivateKey: base64.RawURLEncoding.EncodeToString(privateBytes),
		PublicKey:  base64.RawURLEncoding.EncodeToString(publicBytes),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("webpush: encode VAPID file: %w", err)
	}
	body = append(body, '\n')
	tmp, err := os.CreateTemp(dir, vapidFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("webpush: create VAPID temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("webpush: chmod VAPID temp file: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("webpush: write VAPID temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("webpush: sync VAPID temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("webpush: close VAPID temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("webpush: install VAPID file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("webpush: chmod VAPID file: %w", err)
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func vapidPublicKey(key *ecdsa.PrivateKey) []byte {
	return elliptic.Marshal(elliptic.P256(), key.PublicKey.X, key.PublicKey.Y)
}

func signVAPIDJWT(privateKey *ecdsa.PrivateKey, subject, endpoint string, now time.Time, random io.Reader) (string, error) {
	if privateKey == nil || random == nil {
		return "", errors.New("webpush: VAPID key and random source are required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("webpush: invalid push endpoint")
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		subject = DefaultVAPIDSubject
	}
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"aud": parsed.Scheme + "://" + parsed.Host,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": subject,
	})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(random, privateKey, digest[:])
	if err != nil {
		return "", fmt.Errorf("webpush: sign VAPID JWT: %w", err)
	}
	signature := append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
