package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	webPushRecordSize = 4096
	saltBytes         = 16
	authSecretBytes   = 16
	p256PublicBytes   = 65
	p256PrivateBytes  = 32
)

// encryptPayload applies RFC 8291 and RFC 8188 aes128gcm encoding with caller-
// supplied deterministic sender material. Production generates fresh values in
// encryptForSubscription; the deterministic seam exists for Appendix A.
func encryptPayload(plaintext, receiverPublic, authSecret, senderPrivate, salt []byte) ([]byte, error) {
	if len(receiverPublic) != p256PublicBytes {
		return nil, fmt.Errorf("webpush: receiver public key is %d bytes, want %d", len(receiverPublic), p256PublicBytes)
	}
	if len(authSecret) != authSecretBytes {
		return nil, fmt.Errorf("webpush: auth secret is %d bytes, want %d", len(authSecret), authSecretBytes)
	}
	if len(senderPrivate) != p256PrivateBytes {
		return nil, fmt.Errorf("webpush: sender private key is %d bytes, want %d", len(senderPrivate), p256PrivateBytes)
	}
	if len(salt) != saltBytes {
		return nil, fmt.Errorf("webpush: salt is %d bytes, want %d", len(salt), saltBytes)
	}

	curve := ecdh.P256()
	senderKey, err := curve.NewPrivateKey(senderPrivate)
	if err != nil {
		return nil, fmt.Errorf("webpush: sender private key: %w", err)
	}
	receiverKey, err := curve.NewPublicKey(receiverPublic)
	if err != nil {
		return nil, fmt.Errorf("webpush: receiver public key: %w", err)
	}
	shared, err := senderKey.ECDH(receiverKey)
	if err != nil {
		return nil, fmt.Errorf("webpush: ECDH: %w", err)
	}

	prkKey, err := hkdf.Extract(sha256.New, shared, authSecret)
	if err != nil {
		return nil, fmt.Errorf("webpush: auth extract: %w", err)
	}
	keyInfo := append([]byte("WebPush: info\x00"), receiverPublic...)
	keyInfo = append(keyInfo, senderKey.PublicKey().Bytes()...)
	ikm, err := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	if err != nil {
		return nil, fmt.Errorf("webpush: key combine: %w", err)
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, fmt.Errorf("webpush: content extract: %w", err)
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, fmt.Errorf("webpush: content key: %w", err)
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, fmt.Errorf("webpush: nonce: %w", err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("webpush: AES: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("webpush: GCM: %w", err)
	}
	record := append(append([]byte(nil), plaintext...), 0x02)
	ciphertext := aead.Seal(nil, nonce, record, nil)

	header := append([]byte(nil), salt...)
	size := make([]byte, 4)
	binary.BigEndian.PutUint32(size, webPushRecordSize)
	header = append(header, size...)
	senderPublic := senderKey.PublicKey().Bytes()
	header = append(header, byte(len(senderPublic)))
	header = append(header, senderPublic...)
	return append(header, ciphertext...), nil
}

func encryptForSubscription(plaintext, receiverPublic, authSecret []byte, random io.Reader) ([]byte, error) {
	if random == nil {
		return nil, errors.New("webpush: nil random source")
	}
	salt := make([]byte, saltBytes)
	if _, err := io.ReadFull(random, salt); err != nil {
		return nil, fmt.Errorf("webpush: generate salt: %w", err)
	}
	privateKey, err := ecdh.P256().GenerateKey(random)
	if err != nil {
		return nil, fmt.Errorf("webpush: generate sender key: %w", err)
	}
	return encryptPayload(plaintext, receiverPublic, authSecret, privateKey.Bytes(), salt)
}
