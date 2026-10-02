// Package certutil creates the local CA and server certificate used by gofer's
// optional HTTPS listener. Private keys are written with owner-only permissions.
package certutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Result struct {
	CACertFile     string
	ServerCertFile string
	ServerKeyFile  string
}

func Generate(dir string, hosts []string) (Result, error) {
	if strings.TrimSpace(dir) == "" {
		return Result{}, errors.New("certificate output directory is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create certificate directory: %w", err)
	}
	caCertPath := filepath.Join(dir, "ca.crt")
	caKeyPath := filepath.Join(dir, "ca.key")
	caCert, caKey, err := loadOrCreateCA(caCertPath, caKeyPath)
	if err != nil {
		return Result{}, err
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Result{}, fmt.Errorf("generate server key: %w", err)
	}
	if len(hosts) == 0 {
		hosts = []string{"localhost"}
	}
	serial, err := serialNumber()
	if err != nil {
		return Result{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    now.Add(-5 * time.Minute), NotAfter: now.Add(825 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		return Result{}, fmt.Errorf("create server certificate: %w", err)
	}
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	if err := writeFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return Result{}, fmt.Errorf("write server certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		return Result{}, fmt.Errorf("marshal server key: %w", err)
	}
	if err := preparePrivateFile(keyPath); err != nil {
		return Result{}, err
	}
	if err := writeFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return Result{}, fmt.Errorf("write server key: %w", err)
	}
	if err := securePrivateFile(keyPath); err != nil {
		return Result{}, err
	}
	return Result{CACertFile: caCertPath, ServerCertFile: certPath, ServerKeyFile: keyPath}, nil
}

func loadOrCreateCA(certPath, keyPath string) (*x509.Certificate, *rsa.PrivateKey, error) {
	certBytes, certErr := os.ReadFile(certPath)
	keyBytes, keyErr := os.ReadFile(keyPath)
	if certErr == nil && keyErr == nil {
		certBlock, _ := pem.Decode(certBytes)
		keyBlock, _ := pem.Decode(keyBytes)
		if certBlock == nil || keyBlock == nil {
			return nil, nil, errors.New("existing CA files are not valid PEM")
		}
		cert, err := x509.ParseCertificate(certBlock.Bytes)
		if err != nil {
			return nil, nil, fmt.Errorf("parse existing CA certificate: %w", err)
		}
		key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			key, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if err != nil || !ok {
			return nil, nil, errors.New("existing CA key is not RSA private key")
		}
		return cert, rsaKey, nil
	}
	if !errors.Is(certErr, os.ErrNotExist) || !errors.Is(keyErr, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("read existing CA files: cert=%v key=%v", certErr, keyErr)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "gofer local CA"},
		NotBefore:    now.Add(-5 * time.Minute), NotAfter: now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create CA certificate: %w", err)
	}
	keyDER := x509.MarshalPKCS1PrivateKey(key)
	if err := writeFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, nil, fmt.Errorf("write CA key: %w", err)
	}
	if err := securePrivateFile(keyPath); err != nil {
		return nil, nil, err
	}
	if err := writeFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, nil, fmt.Errorf("write CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func serialNumber() (*big.Int, error) {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	return n, nil
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func securePrivateFile(path string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	current, err := user.Current()
	if err != nil || strings.TrimSpace(current.Username) == "" {
		return fmt.Errorf("resolve current Windows user for private key ACL: %w", err)
	}
	if output, err := exec.Command("icacls", path, "/inheritance:r", "/grant:r", current.Username+":R").CombinedOutput(); err != nil {
		return fmt.Errorf("tighten private key ACL: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func preparePrivateFile(path string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect existing private key ACL: %w", err)
	}
	current, err := user.Current()
	if err != nil || strings.TrimSpace(current.Username) == "" {
		return fmt.Errorf("resolve current Windows user for private key rewrite: %w", err)
	}
	if output, err := exec.Command("icacls", path, "/grant:r", current.Username+":F").CombinedOutput(); err != nil {
		return fmt.Errorf("prepare private key rewrite: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}
