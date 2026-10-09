// Package vapid verifies the Authorization header that a Web Push sender
// (RFC 8292) attaches to every delivery.
//
// The header looks like:
//
//	Authorization: vapid t=<JWT>,k=<base64url P-256 public key>
//
// Verifying the JWT alone proves very little: anyone can mint a VAPID key
// pair and sign a well-formed token. The guarantee only appears once the
// offered `k` is compared against a key that was pinned out-of-band — for
// this gateway, the `configuration.vapid.public_key` the client read from
// its own instance at registration time. That turns the check into "only
// the instance this subscription belongs to may push here".
package vapid

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

var (
	// ErrMissing is returned when the request carries no VAPID header.
	ErrMissing = errors.New("vapid: missing Authorization header")
	// ErrMalformed is returned when the header is not a parseable
	// `vapid t=…,k=…` pair.
	ErrMalformed = errors.New("vapid: malformed Authorization header")
	// ErrKeyMismatch is returned when the offered key is not the pinned one.
	ErrKeyMismatch = errors.New("vapid: public key does not match pinned key")
	// ErrBadSignature is returned when the JWT does not verify against the
	// offered key, or has expired.
	ErrBadSignature = errors.New("vapid: JWT signature invalid")
)

// Header holds the two parameters carried in a VAPID Authorization header.
type Header struct {
	Token     string // the `t` parameter: a compact ES256 JWT
	PublicKey string // the `k` parameter: base64url of an uncompressed P-256 point
}

// ParseHeader splits a `vapid t=…,k=…` Authorization header. The scheme
// prefix is matched case-insensitively; parameter order is not significant.
func ParseHeader(authorization string) (Header, error) {
	if authorization == "" {
		return Header{}, ErrMissing
	}

	rest := strings.TrimSpace(authorization)
	if len(rest) < 5 || !strings.EqualFold(rest[:5], "vapid") {
		return Header{}, fmt.Errorf("%w: expected \"vapid\" scheme", ErrMalformed)
	}
	rest = strings.TrimSpace(rest[5:])

	var h Header
	for _, part := range strings.Split(rest, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "t":
			h.Token = strings.TrimSpace(value)
		case "k":
			h.PublicKey = strings.TrimSpace(value)
		}
	}

	if h.Token == "" || h.PublicKey == "" {
		return Header{}, fmt.Errorf("%w: need both t and k", ErrMalformed)
	}
	return h, nil
}

// Verify checks a VAPID Authorization header against a pinned public key.
//
// pinnedKey is the base64url-encoded uncompressed P-256 point the client
// supplied at registration. When pinnedKey is empty the key comparison is
// skipped and only the JWT signature is checked — useful while bringing a
// deployment up, but it leaves the endpoint open to anyone who learns the
// URL, so callers should log loudly in that mode.
func Verify(authorization, pinnedKey string) error {
	h, err := ParseHeader(authorization)
	if err != nil {
		return err
	}

	if pinnedKey != "" && !sameKey(h.PublicKey, pinnedKey) {
		return ErrKeyMismatch
	}

	pub, err := parsePublicKey(h.PublicKey)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}

	// ES256 only. Accepting the `alg` from the token would let a sender
	// downgrade to "none" or to an HMAC whose key we would have to guess
	// from the public point.
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"ES256"}))
	if _, err := parser.Parse(h.Token, func(*jwt.Token) (interface{}, error) {
		return pub, nil
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrBadSignature, err)
	}

	return nil
}

// sameKey compares two base64url-encoded keys by their decoded bytes, so
// that padding differences ("=" kept or stripped) do not cause a mismatch.
// The comparison is constant time to avoid leaking the pinned key byte by
// byte to a caller who can send many probes.
func sameKey(offered, pinned string) bool {
	a, errA := decodeKey(offered)
	b, errB := decodeKey(pinned)
	if errA != nil || errB != nil {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

// decodeKey accepts base64url with or without padding, and also tolerates
// standard base64 — Mastodon reports its instance key in standard base64
// (with "+/" and padding) while the VAPID header uses base64url.
func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, fmt.Errorf("not valid base64: %q", truncate(s))
}

// parsePublicKey turns a base64url uncompressed P-256 point into a key
// usable for signature verification.
func parsePublicKey(encoded string) (*ecdsa.PublicKey, error) {
	raw, err := decodeKey(encoded)
	if err != nil {
		return nil, err
	}
	if len(raw) != 65 || raw[0] != 4 {
		return nil, fmt.Errorf("expected 65-byte uncompressed point, got %d bytes", len(raw))
	}

	// ecdh.NewPublicKey rejects points that are not on the curve. Doing this
	// first means the ecdsa.PublicKey assembled below is always a valid point,
	// without reaching for the deprecated elliptic.Unmarshal/IsOnCurve pair.
	if _, err := ecdh.P256().NewPublicKey(raw); err != nil {
		return nil, fmt.Errorf("point is not on P-256: %w", err)
	}

	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(raw[1:33]),
		Y:     new(big.Int).SetBytes(raw[33:65]),
	}, nil
}

func truncate(s string) string {
	if len(s) <= 16 {
		return s
	}
	return s[:16] + "…"
}
