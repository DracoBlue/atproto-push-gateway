package did

import (
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

