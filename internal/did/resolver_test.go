package did

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestResolveDID_CachedPLCDocument(t *testing.T) {
	doc := DIDDocument{
		ID: "did:plc:test123",
		VerificationMethod: []VerificationMethod{
			{
				ID:                 "did:plc:test123#atproto",
				Type:               "Multikey",
				Controller:         "did:plc:test123",
				PublicKeyMultibase: "zQ3shXjHeiBuRCKmM36cuYnm7YEMzhGnCmCyW92sRJ9pribSF",
			},
		},
	}

	resolver := NewResolver()

	// Pre-populate cache to test cache hit path
	resolver.mu.Lock()
	resolver.cache["did:plc:test123"] = cacheEntry{
		doc:      &doc,
		cachedAt: time.Now(),
	}
	resolver.mu.Unlock()

	result, err := resolver.ResolveDID("did:plc:test123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "did:plc:test123" {
		t.Errorf("expected ID did:plc:test123, got %s", result.ID)
	}
	if len(result.VerificationMethod) != 1 {
		t.Fatalf("expected 1 verification method, got %d", len(result.VerificationMethod))
	}
}

func TestResolveDID_UnsupportedMethod(t *testing.T) {
	resolver := NewResolver()
	_, err := resolver.ResolveDID("did:key:abc")
	if err == nil {
		t.Error("expected error for unsupported DID method")
	}
}

func TestGetSigningKey_NoAtprotoMethod(t *testing.T) {
	doc := &DIDDocument{
		ID: "did:plc:test",
		VerificationMethod: []VerificationMethod{
			{
				ID:   "did:plc:test#other",
				Type: "Multikey",
			},
		},
	}

	_, err := GetSigningKey(doc)
	if err == nil {
		t.Error("expected error when no #atproto method found")
	}
}

func TestGetSigningKey_NoKeyMaterial(t *testing.T) {
	doc := &DIDDocument{
		ID: "did:plc:test",
		VerificationMethod: []VerificationMethod{
			{
				ID:   "did:plc:test#atproto",
				Type: "Multikey",
				// No publicKeyMultibase or publicKeyJwk
			},
		},
	}

	_, err := GetSigningKey(doc)
	if err == nil {
		t.Error("expected error when no key material present")
	}
}

func TestBase58Decode(t *testing.T) {
	// Test basic base58 decoding
	result := base58Decode("2")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result) != 1 || result[0] != 1 {
		t.Errorf("expected [1], got %v", result)
	}

	// Leading 1s represent zero bytes
	result = base58Decode("1")
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result) != 1 || result[0] != 0 {
		t.Errorf("expected [0], got %v", result)
	}

	// Invalid character
	result = base58Decode("0") // '0' is not in base58 alphabet
	if result != nil {
		t.Errorf("expected nil for invalid character, got %v", result)
	}
}

func TestResolveDID_CacheTTL(t *testing.T) {
	resolver := NewResolver()

	doc := &DIDDocument{ID: "did:plc:cached"}
	resolver.mu.Lock()
	resolver.cache["did:plc:cached"] = cacheEntry{
		doc:      doc,
		cachedAt: time.Now(),
	}
	resolver.mu.Unlock()

	result, err := resolver.ResolveDID("did:plc:cached")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "did:plc:cached" {
		t.Errorf("expected cached doc, got %s", result.ID)
	}
}

func TestParseMultibaseKey_RequiresExactLength(t *testing.T) {
	// 36 bytes: 2-byte p256-pub prefix + 34-byte "compressed" key. Must be
	// rejected because total length isn't 35 (the exact size of a valid key).
	keyBytes := make([]byte, 36)
	keyBytes[0] = 0x80
	keyBytes[1] = 0x24
	keyBytes[2] = 0x02
	for i := 3; i < 36; i++ {
		keyBytes[i] = 0xbb
	}

	encoded := "z" + base58Encode(keyBytes)

	_, err := parseMultibaseKey("Multikey", encoded)
	if err == nil {
		t.Error("expected error for multibase key of wrong total length, got nil")
	}
}

func TestResolveDIDWeb_RejectsLoopback(t *testing.T) {
	r := NewResolver()
	_, err := r.ResolveDID("did:web:127.0.0.1")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("expected SSRF-block error, got: %v", err)
	}
}

func TestResolveDIDWeb_RejectsLocalhost(t *testing.T) {
	r := NewResolver()
	_, err := r.ResolveDID("did:web:localhost")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("expected SSRF-block error, got: %v", err)
	}
}

func TestResolveDIDWeb_RejectsIMDS(t *testing.T) {
	r := NewResolver()
	_, err := r.ResolveDID("did:web:169.254.169.254")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("expected SSRF-block error, got: %v", err)
	}
}

func TestResolveDIDWeb_RejectsRFC1918(t *testing.T) {
	r := NewResolver()
	_, err := r.ResolveDID("did:web:10.0.0.1")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("expected SSRF-block error, got: %v", err)
	}
}

func TestResolveDIDWeb_RejectsCGNAT(t *testing.T) {
	r := NewResolver()
	_, err := r.ResolveDID("did:web:100.64.0.1")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("expected SSRF-block error for CGNAT, got: %v", err)
	}
}

func TestResolveDIDWeb_RejectsZeroSubnet(t *testing.T) {
	r := NewResolver()
	_, err := r.ResolveDID("did:web:0.1.2.3")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("expected SSRF-block error for 0.0.0.0/8, got: %v", err)
	}
}

// base58Encode is a minimal test helper (encodes using the Bitcoin alphabet).
func base58Encode(b []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	x := new(big.Int).SetBytes(b)
	base := big.NewInt(58)
	mod := new(big.Int)
	var result []byte
	for x.Sign() > 0 {
		x.DivMod(x, base, mod)
		result = append([]byte{alphabet[mod.Int64()]}, result...)
	}
	for _, c := range b {
		if c != 0 {
			break
		}
		result = append([]byte{'1'}, result...)
	}
	return string(result)
}

func TestNewResolverWithCacheSize_FallsBackToDefault(t *testing.T) {
	resolver := NewResolverWithCacheSize(0)
	if resolver.maxCacheSize != defaultMaxCacheSize {
		t.Errorf("expected default cache size %d for size 0, got %d", defaultMaxCacheSize, resolver.maxCacheSize)
	}
	resolver = NewResolverWithCacheSize(500)
	if resolver.maxCacheSize != 500 {
		t.Errorf("expected cache size 500, got %d", resolver.maxCacheSize)
	}
}

func TestEvictOldest(t *testing.T) {
	resolver := NewResolverWithCacheSize(100)

	base := time.Now()
	for i := 0; i < 100; i++ {
		did := "did:plc:" + string(rune('A'+i/26)) + string(rune('a'+i%26))
		resolver.cache[did] = cacheEntry{
			doc:      &DIDDocument{ID: did},
			cachedAt: base.Add(time.Duration(i) * time.Second),
		}
	}

	resolver.evictOldest()

	// Should evict the oldest 25 (100/4)
	if len(resolver.cache) != 75 {
		t.Errorf("expected cache size 75 after eviction, got %d", len(resolver.cache))
	}
	// The newest entry must survive, the oldest must be gone
	if _, ok := resolver.cache["did:plc:Dv"]; !ok {
		t.Error("expected newest entry to survive eviction")
	}
	if _, ok := resolver.cache["did:plc:Aa"]; ok {
		t.Error("expected oldest entry to be evicted")
	}
}

func TestParseJWKKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	xB64 := base64.RawURLEncoding.EncodeToString(key.PublicKey.X.FillBytes(make([]byte, 32)))
	yB64 := base64.RawURLEncoding.EncodeToString(key.PublicKey.Y.FillBytes(make([]byte, 32)))

	t.Run("valid P-256", func(t *testing.T) {
		pub, err := parseJWKKey(&JWK{Kty: "EC", Crv: "P-256", X: xB64, Y: yB64})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pub.X.Cmp(key.PublicKey.X) != 0 || pub.Y.Cmp(key.PublicKey.Y) != 0 {
			t.Error("parsed key does not match original")
		}
		if pub.Curve != elliptic.P256() {
			t.Error("expected P-256 curve")
		}
	})

	t.Run("rejects non-EC kty", func(t *testing.T) {
		if _, err := parseJWKKey(&JWK{Kty: "RSA", Crv: "P-256", X: xB64, Y: yB64}); err == nil {
			t.Error("expected error for kty=RSA")
		}
	})

	t.Run("rejects unsupported curve", func(t *testing.T) {
		if _, err := parseJWKKey(&JWK{Kty: "EC", Crv: "P-384", X: xB64, Y: yB64}); err == nil {
			t.Error("expected error for P-384")
		}
	})

	t.Run("rejects invalid base64", func(t *testing.T) {
		if _, err := parseJWKKey(&JWK{Kty: "EC", Crv: "P-256", X: "!!!", Y: yB64}); err == nil {
			t.Error("expected error for invalid x encoding")
		}
		if _, err := parseJWKKey(&JWK{Kty: "EC", Crv: "P-256", X: xB64, Y: "!!!"}); err == nil {
			t.Error("expected error for invalid y encoding")
		}
	})
}

func TestParseMultibaseKey_P256(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	compressed := elliptic.MarshalCompressed(elliptic.P256(), key.PublicKey.X, key.PublicKey.Y)
	multikey := append([]byte{0x80, 0x24}, compressed...)
	multibase := "z" + base58Encode(multikey)

	pub, err := parseMultibaseKey("Multikey", multibase)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pub.X.Cmp(key.PublicKey.X) != 0 || pub.Y.Cmp(key.PublicKey.Y) != 0 {
		t.Error("parsed key does not match original")
	}
}

func TestParseMultibaseKey_RejectsBadInput(t *testing.T) {
	if _, err := parseMultibaseKey("Multikey", "z"); err == nil {
		t.Error("expected error for too-short key")
	}
	if _, err := parseMultibaseKey("Multikey", "ufoo"); err == nil {
		t.Error("expected error for non-base58btc prefix")
	}
	if _, err := parseMultibaseKey("Multikey", "z0OIl"); err == nil {
		t.Error("expected error for invalid base58 characters")
	}
}
