package vapid

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testKey generates a P-256 key pair and returns the private key plus its
// public point encoded the way a VAPID `k` parameter carries it.
func testKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	raw := make([]byte, 65)
	raw[0] = 4
	key.PublicKey.X.FillBytes(raw[1:33])
	key.PublicKey.Y.FillBytes(raw[33:65])
	return key, base64.RawURLEncoding.EncodeToString(raw)
}

func signedHeader(t *testing.T, key *ecdsa.PrivateKey, pubKey string, exp time.Time) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.RegisteredClaims{
		Audience:  jwt.ClaimStrings{"https://push.example.org"},
		ExpiresAt: jwt.NewNumericDate(exp),
		Subject:   "mailto:admin@instance.example",
	})
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}
	return "vapid t=" + signed + ",k=" + pubKey
}

func TestVerify_PinnedKeyAndValidSignature(t *testing.T) {
	key, pub := testKey(t)
	header := signedHeader(t, key, pub, time.Now().Add(time.Hour))

	if err := Verify(header, pub); err != nil {
		t.Errorf("expected valid header to pass, got %v", err)
	}
}

// A sender with its own key pair signs a perfectly valid JWT. Without
// pinning this would pass, which is why pinning is the actual control.
func TestVerify_ForeignKeyRejected(t *testing.T) {
	attackerKey, attackerPub := testKey(t)
	_, instancePub := testKey(t)

	header := signedHeader(t, attackerKey, attackerPub, time.Now().Add(time.Hour))

	err := Verify(header, instancePub)
	if !errors.Is(err, ErrKeyMismatch) {
		t.Errorf("expected ErrKeyMismatch, got %v", err)
	}
}

// Offering the instance's key but signing with a different private key must
// fail: possession of the public point proves nothing.
func TestVerify_KeySpoofedButSignatureWrong(t *testing.T) {
	attackerKey, _ := testKey(t)
	_, instancePub := testKey(t)

	header := signedHeader(t, attackerKey, instancePub, time.Now().Add(time.Hour))

	err := Verify(header, instancePub)
	if !errors.Is(err, ErrBadSignature) {
		t.Errorf("expected ErrBadSignature, got %v", err)
	}
}

func TestVerify_ExpiredToken(t *testing.T) {
	key, pub := testKey(t)
	header := signedHeader(t, key, pub, time.Now().Add(-time.Hour))

	err := Verify(header, pub)
	if !errors.Is(err, ErrBadSignature) {
		t.Errorf("expected ErrBadSignature for expired token, got %v", err)
	}
}

// Empty pinnedKey is the bring-up mode: signature still has to verify, but
// any key is accepted.
func TestVerify_UnpinnedAcceptsAnyKey(t *testing.T) {
	key, pub := testKey(t)
	header := signedHeader(t, key, pub, time.Now().Add(time.Hour))

	if err := Verify(header, ""); err != nil {
		t.Errorf("expected unpinned verify to pass, got %v", err)
	}
}

func TestVerify_UnpinnedStillChecksSignature(t *testing.T) {
	key, _ := testKey(t)
	_, otherPub := testKey(t)
	header := signedHeader(t, key, otherPub, time.Now().Add(time.Hour))

	if err := Verify(header, ""); !errors.Is(err, ErrBadSignature) {
		t.Errorf("expected ErrBadSignature, got %v", err)
	}
}

func TestVerify_MissingHeader(t *testing.T) {
	if err := Verify("", "whatever"); !errors.Is(err, ErrMissing) {
		t.Errorf("expected ErrMissing, got %v", err)
	}
}

// Mastodon reports configuration.vapid.public_key in standard base64 while
// the header carries base64url. The same key must compare equal across both.
func TestVerify_EncodingVariantsCompareEqual(t *testing.T) {
	key, urlEncoded := testKey(t)
	raw, err := base64.RawURLEncoding.DecodeString(urlEncoded)
	if err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	stdEncoded := base64.StdEncoding.EncodeToString(raw)

	header := signedHeader(t, key, urlEncoded, time.Now().Add(time.Hour))
	if err := Verify(header, stdEncoded); err != nil {
		t.Errorf("standard-base64 pinned key should match base64url header, got %v", err)
	}
}

func TestParseHeader(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantToken string
		wantKey   string
		wantErr   error
	}{
		{"normal", "vapid t=abc,k=def", "abc", "def", nil},
		{"reversed order", "vapid k=def,t=abc", "abc", "def", nil},
		{"extra spaces", "vapid  t=abc , k=def ", "abc", "def", nil},
		{"uppercase scheme", "VAPID t=abc,k=def", "abc", "def", nil},
		{"wrong scheme", "Bearer t=abc,k=def", "", "", ErrMalformed},
		{"missing k", "vapid t=abc", "", "", ErrMalformed},
		{"missing t", "vapid k=def", "", "", ErrMalformed},
		{"empty", "", "", "", ErrMissing},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, err := ParseHeader(tc.input)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if h.Token != tc.wantToken || h.PublicKey != tc.wantKey {
				t.Errorf("got t=%q k=%q, want t=%q k=%q",
					h.Token, h.PublicKey, tc.wantToken, tc.wantKey)
			}
		})
	}
}

// A JWT signed with a symmetric algorithm must not be accepted just because
// it parses; alg has to be constrained to ES256.
func TestVerify_RejectsNonES256(t *testing.T) {
	_, pub := testKey(t)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	signed, err := token.SignedString([]byte("secret"))
	if err != nil {
		t.Fatalf("failed to sign: %v", err)
	}

	err = Verify("vapid t="+signed+",k="+pub, pub)
	if !errors.Is(err, ErrBadSignature) {
		t.Errorf("expected ErrBadSignature for HS256 token, got %v", err)
	}
}

func TestVerify_MalformedPublicKey(t *testing.T) {
	key, _ := testKey(t)
	header := signedHeader(t, key, base64.RawURLEncoding.EncodeToString([]byte("too short")), time.Now().Add(time.Hour))

	if err := Verify(header, ""); !errors.Is(err, ErrMalformed) {
		t.Errorf("expected ErrMalformed, got %v", err)
	}
}

// A 65-byte blob with the right prefix but coordinates that are not on the
// curve must be rejected rather than assembled into a bogus key.
func TestVerify_PointNotOnCurve(t *testing.T) {
	raw := make([]byte, 65)
	raw[0] = 4
	for i := 1; i < 65; i++ {
		raw[i] = 0xAA
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)

	key, _ := testKey(t)
	header := signedHeader(t, key, encoded, time.Now().Add(time.Hour))

	if err := Verify(header, ""); !errors.Is(err, ErrMalformed) {
		t.Errorf("expected ErrMalformed for off-curve point, got %v", err)
	}
}
