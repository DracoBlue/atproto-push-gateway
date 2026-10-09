package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
)

// WebPushEndpoint is one relay endpoint: the mapping from an opaque URL
// segment to the device that should receive whatever arrives there.
type WebPushEndpoint struct {
	Platform       string
	DeviceToken    string
	AppID          string
	VAPIDPublicKey string
	Instance       string
}

// ErrEndpointNotFound is returned when an endpoint id has no mapping —
// either it never existed or it was unregistered.
var ErrEndpointNotFound = errors.New("store: webpush endpoint not found")

// endpointIDBytes is the length of the random part of a relay URL. The id is
// a bearer credential: knowing it is what lets a sender reach the device, so
// it has to be long enough that it cannot be guessed or enumerated.
const endpointIDBytes = 32

// NewEndpointID returns a fresh random endpoint id, base64url encoded.
func NewEndpointID() (string, error) {
	buf := make([]byte, endpointIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate endpoint id: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// hashEndpointID derives the database key for an endpoint id.
//
// Only the hash is stored. The id itself behaves like a password — it grants
// the ability to push to a device — so a leaked database should not hand an
// attacker a ready-made list of working endpoints. Lookups hash the incoming
// id and compare, exactly like a password check. The cost of this choice is
// that an endpoint URL cannot be recovered from the database for debugging;
// the client re-registers to get a new one.
func hashEndpointID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// CreateWebPushEndpoint stores a new endpoint and returns its id.
//
// Any endpoint previously registered for the same device is removed first: a
// device needs exactly one live endpoint, and leaving stale rows behind would
// keep pushing to a subscription the client has already replaced.
func (s *Store) CreateWebPushEndpoint(e WebPushEndpoint) (string, error) {
	id, err := NewEndpointID()
	if err != nil {
		return "", err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		"DELETE FROM webpush_endpoints WHERE device_token = ? AND app_id = ?",
		e.DeviceToken, e.AppID,
	); err != nil {
		return "", err
	}

	if _, err := tx.Exec(`
		INSERT INTO webpush_endpoints
			(endpoint_hash, platform, device_token, app_id, vapid_public_key, instance)
		VALUES (?, ?, ?, ?, ?, ?)`,
		hashEndpointID(id), e.Platform, e.DeviceToken, e.AppID, e.VAPIDPublicKey, e.Instance,
	); err != nil {
		return "", err
	}

	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// GetWebPushEndpoint resolves an endpoint id to its device mapping.
func (s *Store) GetWebPushEndpoint(id string) (WebPushEndpoint, error) {
	var e WebPushEndpoint
	err := s.db.QueryRow(`
		SELECT platform, device_token, app_id, vapid_public_key, instance
		FROM webpush_endpoints WHERE endpoint_hash = ?`,
		hashEndpointID(id),
	).Scan(&e.Platform, &e.DeviceToken, &e.AppID, &e.VAPIDPublicKey, &e.Instance)

	if errors.Is(err, sql.ErrNoRows) {
		return WebPushEndpoint{}, ErrEndpointNotFound
	}
	if err != nil {
		return WebPushEndpoint{}, err
	}
	return e, nil
}

// DeleteWebPushEndpoint removes an endpoint by id. Deleting an id that does
// not exist is not an error — the caller's goal is "this must be gone".
func (s *Store) DeleteWebPushEndpoint(id string) error {
	_, err := s.db.Exec("DELETE FROM webpush_endpoints WHERE endpoint_hash = ?", hashEndpointID(id))
	return err
}

// DeleteWebPushEndpointsForDevice removes every endpoint pointing at a device
// token, used when the push provider reports the token as permanently invalid.
func (s *Store) DeleteWebPushEndpointsForDevice(deviceToken, appID string) error {
	_, err := s.db.Exec(
		"DELETE FROM webpush_endpoints WHERE device_token = ? AND app_id = ?",
		deviceToken, appID,
	)
	return err
}

// TouchWebPushEndpoint records that a push was relayed through an endpoint.
// Failures are the caller's to ignore: this is bookkeeping, not delivery.
func (s *Store) TouchWebPushEndpoint(id string) error {
	_, err := s.db.Exec(
		"UPDATE webpush_endpoints SET last_push_at = datetime('now') WHERE endpoint_hash = ?",
		hashEndpointID(id),
	)
	return err
}

// CountWebPushEndpoints returns the number of registered relay endpoints,
// for the health report.
func (s *Store) CountWebPushEndpoints() (count int) {
	s.db.QueryRow("SELECT COUNT(*) FROM webpush_endpoints").Scan(&count)
	return
}
