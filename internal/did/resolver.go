package did

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
)

const (
	cacheTTL       = 5 * time.Minute
	requestTimeout = 10 * time.Second
)

// DIDDocument represents a W3C DID Document.
type DIDDocument struct {
	ID                 string               `json:"id"`
	VerificationMethod []VerificationMethod `json:"verificationMethod"`
}

// VerificationMethod represents a verification method in a DID Document.
type VerificationMethod struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	Controller         string `json:"controller"`
	PublicKeyMultibase string `json:"publicKeyMultibase"`
	PublicKeyJwk       *JWK   `json:"publicKeyJwk,omitempty"`
}

// JWK represents a JSON Web Key (subset of fields we need).
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

type cacheEntry struct {
	doc      *DIDDocument
	cachedAt time.Time
}

// Resolver resolves DIDs to DID Documents with caching.
type Resolver struct {
	mu     sync.RWMutex
	cache  map[string]cacheEntry
	client *http.Client
}

func NewResolver() *Resolver {
	// Custom transport that blocks resolution to private / loopback /
	// link-local IP addresses for SSRF protection. Applied on every dial,
	// including redirects.
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("no IPs resolved for %s", host)
		}
		for _, ip := range ips {
			if isBlockedIP(ip.IP) {
				return nil, fmt.Errorf("blocked IP %s for host %s (SSRF protection)", ip.IP, host)
			}
		}
		// All returned IPs passed the block check above; dial the first one
		// explicitly (as an IP literal) to pin the connection and prevent
		// later DNS rebinding from affecting this dial.
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	transport := base
	client := &http.Client{
		Timeout:   requestTimeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	return &Resolver{
		cache:  make(map[string]cacheEntry),
		client: client,
	}
}

// isBlockedIP returns true for addresses the resolver must refuse to connect
// to: loopback, link-local (incl. AWS/GCP/Azure IMDS at 169.254.169.254),
// multicast, and RFC1918 private IPv4 plus IPv6 ULA/site-local. Also blocks
// CGNAT (100.64.0.0/10), deprecated IPv6 site-local (fec0::/10), 0.0.0.0/8,
// and the broadcast address.
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return true
	}
	// Broadcast
	if ip.Equal(net.IPv4bcast) {
		return true
	}
	// CGNAT / RFC 6598: 100.64.0.0/10
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		// 0.0.0.0/8 (not just the unspecified singleton)
		if v4[0] == 0 {
			return true
		}
	}
	// Deprecated IPv6 site-local: fec0::/10
	if len(ip) == net.IPv6len && ip[0] == 0xfe && (ip[1]&0xc0) == 0xc0 {
		return true
	}
	return false
}

// ResolveDID fetches and parses a DID document.
func (r *Resolver) ResolveDID(did string) (*DIDDocument, error) {
	// Check cache
	r.mu.RLock()
	if entry, ok := r.cache[did]; ok && time.Since(entry.cachedAt) < cacheTTL {
		r.mu.RUnlock()
		return entry.doc, nil
	}
	r.mu.RUnlock()

	doc, err := r.fetchDIDDocument(did)
	if err != nil {
		return nil, err
	}

	// Cache the result
	r.mu.Lock()
	r.cache[did] = cacheEntry{
		doc:      doc,
		cachedAt: time.Now(),
	}
	r.mu.Unlock()

	return doc, nil
}

func (r *Resolver) fetchDIDDocument(did string) (*DIDDocument, error) {
	var url string

	switch {
	case strings.HasPrefix(did, "did:plc:"):
		url = "https://plc.directory/" + did
	case strings.HasPrefix(did, "did:web:"):
		domain := strings.TrimPrefix(did, "did:web:")
		// did:web uses : as path separator, replace with /
		domain = strings.ReplaceAll(domain, ":", "/")
		if domain == "localhost" || strings.HasPrefix(domain, "localhost:") || strings.HasPrefix(domain, "localhost/") {
			return nil, fmt.Errorf("blocked hostname %q (SSRF protection)", domain)
		}
		url = "https://" + domain + "/.well-known/did.json"
	default:
		return nil, fmt.Errorf("unsupported DID method: %s", did)
	}

	resp, err := r.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch DID document for %s: %w", did, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DID resolution for %s returned HTTP %d", did, resp.StatusCode)
	}

	const maxDocBytes = 256 * 1024 // 256 KiB
	limited := io.LimitReader(resp.Body, maxDocBytes)
	var doc DIDDocument
	if err := json.NewDecoder(limited).Decode(&doc); err != nil {
		return nil, fmt.Errorf("failed to decode DID document for %s: %w", did, err)
	}

	return &doc, nil
}

// GetSigningKey extracts the atproto signing key from a DID document.
// It looks for a verification method with id containing "#atproto".
func GetSigningKey(doc *DIDDocument) (*ecdsa.PublicKey, error) {
	for _, vm := range doc.VerificationMethod {
		if !strings.Contains(vm.ID, "#atproto") {
			continue
		}

		// Try publicKeyJwk first
		if vm.PublicKeyJwk != nil {
			return parseJWKKey(vm.PublicKeyJwk)
		}

		// Try multibase-encoded key
		if vm.PublicKeyMultibase != "" {
			return parseMultibaseKey(vm.Type, vm.PublicKeyMultibase)
		}

		return nil, fmt.Errorf("verification method %s has no usable key material", vm.ID)
	}

	return nil, fmt.Errorf("no #atproto verification method found in DID document for %s", doc.ID)
}

// parseJWKKey parses an EC public key from a JWK.
func parseJWKKey(jwk *JWK) (*ecdsa.PublicKey, error) {
	if jwk.Kty != "EC" {
		return nil, fmt.Errorf("unsupported JWK key type: %s", jwk.Kty)
	}

	xBytes, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWK x: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWK y: %w", err)
	}

	var curve elliptic.Curve
	switch jwk.Crv {
	case "secp256k1":
		curve = secp256k1.S256()
	case "P-256":
		curve = elliptic.P256()
	default:
		return nil, fmt.Errorf("unsupported JWK curve: %s", jwk.Crv)
	}

	return &ecdsa.PublicKey{
		Curve: curve,
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}, nil
}

// parseMultibaseKey parses a multibase/multicodec encoded public key.
// Multibase prefix 'z' = base58btc encoding.
// Multicodec prefix 0xe7 0x01 = secp256k1 public key (compressed, 33 bytes).
// Multicodec prefix 0x80 0x24 = P-256 public key (compressed, 33 bytes).
func parseMultibaseKey(vmType string, multibase string) (*ecdsa.PublicKey, error) {
	if len(multibase) < 2 {
		return nil, fmt.Errorf("multibase key too short")
	}

	prefix := multibase[0]
	if prefix != 'z' {
		return nil, fmt.Errorf("unsupported multibase prefix: %c (only base58btc 'z' supported)", prefix)
	}

	decoded := base58Decode(multibase[1:])
	if decoded == nil {
		return nil, fmt.Errorf("failed to base58 decode multibase key")
	}

	if len(decoded) < 2 {
		return nil, fmt.Errorf("decoded multibase key too short")
	}

	// Check multicodec varint prefix
	// secp256k1-pub: 0xe7 0x01 (varint for 0xe7)
	// p256-pub: 0x80 0x24 (varint for 0x1200)
	if len(decoded) == 35 && decoded[0] == 0xe7 && decoded[1] == 0x01 {
		// secp256k1 compressed public key (33 bytes after 2-byte prefix)
		compressedKey := decoded[2:]
		if len(compressedKey) != 33 {
			return nil, fmt.Errorf("expected 33-byte compressed secp256k1 key, got %d bytes", len(compressedKey))
		}
		pubKey, err := secp256k1.ParsePubKey(compressedKey)
		if err != nil {
			return nil, fmt.Errorf("failed to parse compressed secp256k1 key: %w", err)
		}
		return &ecdsa.PublicKey{
			Curve: secp256k1.S256(),
			X:     pubKey.X(),
			Y:     pubKey.Y(),
		}, nil
	}

	if len(decoded) == 35 && decoded[0] == 0x80 && decoded[1] == 0x24 {
		// P-256 compressed public key (33 bytes after 2-byte prefix)
		compressedKey := decoded[2:]
		if len(compressedKey) != 33 {
			return nil, fmt.Errorf("expected 33-byte compressed P-256 key, got %d bytes", len(compressedKey))
		}
		x, y := elliptic.UnmarshalCompressed(elliptic.P256(), compressedKey)
		if x == nil {
			return nil, fmt.Errorf("failed to unmarshal compressed P-256 key")
		}
		return &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     x,
			Y:     y,
		}, nil
	}

	return nil, fmt.Errorf("unsupported multicodec prefix: 0x%x 0x%x", decoded[0], decoded[1])
}

// base58Decode decodes a base58btc string (Bitcoin alphabet).
func base58Decode(s string) []byte {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

	result := big.NewInt(0)
	base := big.NewInt(58)

	for _, c := range s {
		idx := strings.IndexRune(alphabet, c)
		if idx < 0 {
			return nil
		}
		result.Mul(result, base)
		result.Add(result, big.NewInt(int64(idx)))
	}

	// Count leading '1's (representing leading zero bytes)
	leadingZeros := 0
	for _, c := range s {
		if c != '1' {
			break
		}
		leadingZeros++
	}

	resultBytes := result.Bytes()
	decoded := make([]byte, leadingZeros+len(resultBytes))
	copy(decoded[leadingZeros:], resultBytes)

	return decoded
}
