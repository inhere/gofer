package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func decodeURLFixture(t *testing.T, value string) []byte {
	t.Helper()
	value = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		default:
			return r
		}
	}, value)
	out, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return out
}

func TestPushEncryptRFC8291Vector(t *testing.T) {
	// RFC 8291 Appendix A / Section 5. These literals are the independent
	// reference output, not values recomputed by the implementation.
	plaintext := decodeURLFixture(t, "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24")
	receiverPublic := decodeURLFixture(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	authSecret := decodeURLFixture(t, "BTBZMqHH6r4Tts7J_aSIgg")
	senderPrivate := decodeURLFixture(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw")
	salt := decodeURLFixture(t, "DGv6ra1nlYgDCS1FRnbzlw")
	want := decodeURLFixture(t, `
		DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml
		mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT
		pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN
	`)

	got, err := encryptPayload(plaintext, receiverPublic, authSecret, senderPrivate, salt)
	if err != nil {
		t.Fatalf("encrypt payload: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("RFC 8291 body mismatch\n got: %s\nwant: %s",
			base64.RawURLEncoding.EncodeToString(got),
			base64.RawURLEncoding.EncodeToString(want))
	}
}

func TestPushVAPIDJWT(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Unix(1_800_000_000, 0)
	endpoint := "https://push.example.test:8443/send/device"

	for _, tc := range []struct {
		name, subject, wantSubject string
	}{
		{name: "default subject", wantSubject: DefaultVAPIDSubject},
		{name: "configured subject", subject: "mailto:ops@example.test", wantSubject: "mailto:ops@example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token, err := signVAPIDJWT(privateKey, tc.subject, endpoint, now, rand.Reader)
			if err != nil {
				t.Fatalf("sign jwt: %v", err)
			}
			parts := strings.Split(token, ".")
			if len(parts) != 3 {
				t.Fatalf("jwt parts = %d", len(parts))
			}
			var header map[string]any
			if err := json.Unmarshal(decodeURLFixture(t, parts[0]), &header); err != nil {
				t.Fatalf("decode header: %v", err)
			}
			if header["alg"] != "ES256" || header["typ"] != "JWT" {
				t.Fatalf("header = %#v", header)
			}
			var claims struct {
				Audience string `json:"aud"`
				Expires  int64  `json:"exp"`
				Subject  string `json:"sub"`
			}
			if err := json.Unmarshal(decodeURLFixture(t, parts[1]), &claims); err != nil {
				t.Fatalf("decode claims: %v", err)
			}
			if claims.Audience != "https://push.example.test:8443" {
				t.Fatalf("aud = %q", claims.Audience)
			}
			if claims.Subject != tc.wantSubject {
				t.Fatalf("sub = %q, want %q", claims.Subject, tc.wantSubject)
			}
			if claims.Expires <= now.Unix() || claims.Expires > now.Add(24*time.Hour).Unix() {
				t.Fatalf("exp = %d, now=%d", claims.Expires, now.Unix())
			}
			sig := decodeURLFixture(t, parts[2])
			if len(sig) != 64 {
				t.Fatalf("ES256 signature len = %d, want 64", len(sig))
			}
			sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			r := new(big.Int).SetBytes(sig[:32])
			s := new(big.Int).SetBytes(sig[32:])
			if !ecdsa.Verify(&privateKey.PublicKey, sum[:], r, s) {
				t.Fatal("public key did not verify JWT")
			}
		})
	}
}
