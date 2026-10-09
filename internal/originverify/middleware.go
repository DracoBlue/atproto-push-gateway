// Package originverify provides an HTTP middleware that gates incoming
// requests behind a shared-secret header. Intended for deployments where
// the gateway sits behind a CDN / WAF (Cloudflare, CloudFront, etc.) that
// injects the secret on every forwarded request, so direct origin-bypass
// requests can be rejected.
//
// Mirrors the AWS CloudFront pattern "Restricting access to your origin
// through CloudFront using a custom header" — Cloudflare offers the same
// via Transform Rules / Workers.
package originverify

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
)

// Config controls the middleware behavior.
type Config struct {
	// Secret is the expected value of the header. When empty, the
	// middleware is a no-op pass-through.
	Secret string

	// HeaderName is the request header to inspect. Empty falls back to
	// "X-Origin-Verify".
	HeaderName string

	// ExcludeHealth skips the check for GET /health, useful when
	// loadbalancer probes can't set custom headers.
	ExcludeHealth bool

	// ExcludeDIDJSON skips the check for GET /.well-known/did.json so
	// the gateway's DID document remains universally fetchable for
	// service-auth verification by external PDSes.
	ExcludeDIDJSON bool
}

// relayPrefix is the path tree served by the web push relay.
//
// Requests here arrive straight from arbitrary Fediverse servers, which
// cannot be asked to attach an origin-verify header. The exemption is
// unconditional, and not an operator option, because getting it wrong is
// silent and permanent: Mastodon destroys a push subscription on any 4xx
// other than 408/429, so a single 403 from this middleware would end that
// user's notifications until they re-register — with nothing in the client
// to indicate why.
//
// The relay is not left unprotected by this. It authenticates senders with
// pinned VAPID keys and an unguessable endpoint id, which is the appropriate
// control for a path the public must reach.
const relayPrefix = "/relay/"

// Wrap returns next unchanged when cfg.Secret is empty. Otherwise it
// returns a handler that requires every request to carry the configured
// header set to the configured secret (compared in constant time), with
// the optional /health and /.well-known/did.json exemptions applied.
func Wrap(next http.Handler, cfg Config) http.Handler {
	if cfg.Secret == "" {
		return next
	}
	headerName := cfg.HeaderName
	if headerName == "" {
		headerName = "X-Origin-Verify"
	}
	expected := []byte(cfg.Secret)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isExempt(r, cfg) {
			next.ServeHTTP(w, r)
			return
		}
		offered := []byte(r.Header.Get(headerName))
		if subtle.ConstantTimeCompare(offered, expected) != 1 {
			log.Printf("[origin-verify] rejected %s %s from %s: header mismatch", r.Method, r.URL.Path, clientAddr(r))
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isExempt(r *http.Request, cfg Config) bool {
	if strings.HasPrefix(r.URL.Path, relayPrefix) {
		return true
	}
	if cfg.ExcludeHealth && r.URL.Path == "/health" {
		return true
	}
	if cfg.ExcludeDIDJSON && r.URL.Path == "/.well-known/did.json" {
		return true
	}
	return false
}

func clientAddr(r *http.Request) string {
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}
