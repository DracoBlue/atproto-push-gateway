package store

import (
	"database/sql"
	"fmt"
	"sync"

	_ "github.com/mattn/go-sqlite3"
)

type PushToken struct {
	ActorDID  string
	Platform  string
	PushToken string
	AppID     string
}

type Block struct {
	BlockerDID string
	BlockedDID string
	RKey       string
}

type Store struct {
	db         *sql.DB
	mu         sync.RWMutex
	registeredDIDs      map[string]bool
	blocks              map[string]map[string]bool   // blocker -> blocked -> true
	blocksByRKey        map[string]map[string]string // blocker -> rkey -> blocked
	verificationsByRKey map[string]map[string]string // verifier -> rkey -> subject
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS push_tokens (
			actor_did TEXT NOT NULL,
			platform TEXT NOT NULL CHECK (platform IN ('ios', 'android', 'web')),
			push_token TEXT NOT NULL,
			app_id TEXT NOT NULL,
			created_at TEXT DEFAULT (datetime('now')),
			updated_at TEXT DEFAULT (datetime('now')),
			PRIMARY KEY (actor_did, push_token)
		);
		CREATE TABLE IF NOT EXISTS blocks (
			blocker_did TEXT NOT NULL,
			blocked_did TEXT NOT NULL,
			rkey TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (blocker_did, blocked_did)
		);
		CREATE INDEX IF NOT EXISTS idx_blocks_rkey ON blocks (blocker_did, rkey);
		CREATE TABLE IF NOT EXISTS verifications (
			verifier_did TEXT NOT NULL,
			subject_did TEXT NOT NULL,
			rkey TEXT NOT NULL,
			PRIMARY KEY (verifier_did, rkey)
		);
		CREATE TABLE IF NOT EXISTS blocks_backfilled (
			actor_did TEXT PRIMARY KEY,
			backfilled_at TEXT DEFAULT (datetime('now'))
		);
	`); err != nil {
		return nil, err
	}

	s := &Store{
		db:                  db,
		registeredDIDs:      make(map[string]bool),
		blocks:              make(map[string]map[string]bool),
		blocksByRKey:        make(map[string]map[string]string),
		verificationsByRKey: make(map[string]map[string]string),
	}

	// loadIntoMemory is called without holding locks because the Store has not
	// been returned yet — no other goroutine can have a reference to it, so
	// there is no concurrent access at this point.
	if err := s.loadIntoMemory(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Store) loadIntoMemory() error {
	// Load registered DIDs
	rows, err := s.db.Query("SELECT DISTINCT actor_did FROM push_tokens")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var did string
		if err := rows.Scan(&did); err != nil {
			return err
		}
		s.registeredDIDs[did] = true
	}

	// Load blocks
	blockRows, err := s.db.Query("SELECT blocker_did, blocked_did, rkey FROM blocks")
	if err != nil {
		return err
	}
	defer blockRows.Close()
	for blockRows.Next() {
		var blocker, blocked, rkey string
		if err := blockRows.Scan(&blocker, &blocked, &rkey); err != nil {
			return err
		}
		if s.blocks[blocker] == nil {
			s.blocks[blocker] = make(map[string]bool)
		}
		s.blocks[blocker][blocked] = true
		if rkey != "" {
			if s.blocksByRKey[blocker] == nil {
				s.blocksByRKey[blocker] = make(map[string]string)
			}
			s.blocksByRKey[blocker][rkey] = blocked
		}
	}

	// Load verifications
	verifRows, err := s.db.Query("SELECT verifier_did, rkey, subject_did FROM verifications")
	if err != nil {
		return err
	}
	defer verifRows.Close()
	for verifRows.Next() {
		var verifier, rkey, subject string
		if err := verifRows.Scan(&verifier, &rkey, &subject); err != nil {
			return err
		}
		if s.verificationsByRKey[verifier] == nil {
			s.verificationsByRKey[verifier] = make(map[string]string)
		}
		s.verificationsByRKey[verifier][rkey] = subject
	}

	return nil
}

const maxTokensPerDID = 20

func (s *Store) RegisterToken(actorDID, platform, pushToken, appID string) error {
	// Enforce per-DID cap. An upsert on the same (actor_did, push_token) does
	// not grow the count, so count excluding the same token before insert.
	// Wrap the count+insert pair in a transaction so concurrent registrations
	// for the same DID can't each see existing < cap and all insert, which
	// would overshoot the cap by up to N-1.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var existing int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM push_tokens WHERE actor_did = ? AND push_token != ?",
		actorDID, pushToken,
	).Scan(&existing); err != nil {
		return err
	}
	if existing >= maxTokensPerDID {
		return fmt.Errorf("DID %s already has %d tokens (cap: %d)", actorDID, existing, maxTokensPerDID)
	}

	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO push_tokens (actor_did, platform, push_token, app_id, updated_at)
		 VALUES (?, ?, ?, ?, datetime('now'))`,
		actorDID, platform, pushToken, appID,
	); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	s.mu.Lock()
	s.registeredDIDs[actorDID] = true
	s.mu.Unlock()

	return nil
}

func (s *Store) UnregisterToken(actorDID, platform, pushToken, appID string) error {
	_, err := s.db.Exec(
		`DELETE FROM push_tokens WHERE actor_did = ? AND platform = ? AND push_token = ? AND app_id = ?`,
		actorDID, platform, pushToken, appID,
	)
	if err != nil {
		return err
	}

	// Check if DID still has any tokens
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM push_tokens WHERE actor_did = ?", actorDID).Scan(&count)
	if count == 0 {
		s.mu.Lock()
		delete(s.registeredDIDs, actorDID)
		s.mu.Unlock()
	}

	return nil
}

func (s *Store) IsRegistered(did string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.registeredDIDs[did]
}

func (s *Store) HasRegisteredDIDs() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.registeredDIDs) > 0
}

func (s *Store) GetTokensForDID(did string) ([]PushToken, error) {
	rows, err := s.db.Query(
		"SELECT actor_did, platform, push_token, app_id FROM push_tokens WHERE actor_did = ?",
		did,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []PushToken
	for rows.Next() {
		var t PushToken
		if err := rows.Scan(&t.ActorDID, &t.Platform, &t.PushToken, &t.AppID); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, nil
}

func (s *Store) AddBlock(blockerDID, blockedDID, rkey string) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO blocks (blocker_did, blocked_did, rkey) VALUES (?, ?, ?)",
		blockerDID, blockedDID, rkey,
	)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if s.blocks[blockerDID] == nil {
		s.blocks[blockerDID] = make(map[string]bool)
	}
	s.blocks[blockerDID][blockedDID] = true
	if rkey != "" {
		if s.blocksByRKey[blockerDID] == nil {
			s.blocksByRKey[blockerDID] = make(map[string]string)
		}
		s.blocksByRKey[blockerDID][rkey] = blockedDID
	}
	s.mu.Unlock()

	return nil
}

// MarkBlocksBackfilled records that this DID's historical blocks have been
// fetched. Returns true if the row was newly inserted (i.e. this caller
// should perform the backfill), false if already done.
func (s *Store) MarkBlocksBackfilled(actorDID string) (bool, error) {
	res, err := s.db.Exec(
		"INSERT OR IGNORE INTO blocks_backfilled (actor_did) VALUES (?)",
		actorDID,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// RemoveBlockByRKey looks up a block by blocker DID and rkey, then removes it.
// Returns the blocked DID if found, or empty string if not found.
func (s *Store) RemoveBlockByRKey(blockerDID, rkey string) (string, error) {
	s.mu.RLock()
	blockedDID := ""
	if s.blocksByRKey[blockerDID] != nil {
		blockedDID = s.blocksByRKey[blockerDID][rkey]
	}
	s.mu.RUnlock()

	if blockedDID == "" {
		return "", nil
	}

	if err := s.RemoveBlock(blockerDID, blockedDID); err != nil {
		return "", err
	}

	s.mu.Lock()
	if s.blocksByRKey[blockerDID] != nil {
		delete(s.blocksByRKey[blockerDID], rkey)
	}
	s.mu.Unlock()

	return blockedDID, nil
}

func (s *Store) RemoveBlock(blockerDID, blockedDID string) error {
	_, err := s.db.Exec(
		"DELETE FROM blocks WHERE blocker_did = ? AND blocked_did = ?",
		blockerDID, blockedDID,
	)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if s.blocks[blockerDID] != nil {
		delete(s.blocks[blockerDID], blockedDID)
	}
	s.mu.Unlock()

	return nil
}

func (s *Store) IsBlocked(actorDID, targetDID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check both directions
	if s.blocks[targetDID] != nil && s.blocks[targetDID][actorDID] {
		return true // target blocked the actor
	}
	if s.blocks[actorDID] != nil && s.blocks[actorDID][targetDID] {
		return true // actor blocked the target
	}
	return false
}

func (s *Store) GetStats() (tokenCount int, blockCount int, didCount int) {
	s.db.QueryRow("SELECT COUNT(*) FROM push_tokens").Scan(&tokenCount)
	s.db.QueryRow("SELECT COUNT(*) FROM blocks").Scan(&blockCount)
	s.mu.RLock()
	didCount = len(s.registeredDIDs)
	s.mu.RUnlock()
	return
}

func (s *Store) AddVerification(verifierDID, subjectDID, rkey string) error {
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO verifications (verifier_did, subject_did, rkey) VALUES (?, ?, ?)",
		verifierDID, subjectDID, rkey,
	)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if s.verificationsByRKey[verifierDID] == nil {
		s.verificationsByRKey[verifierDID] = make(map[string]string)
	}
	s.verificationsByRKey[verifierDID][rkey] = subjectDID
	s.mu.Unlock()

	return nil
}

// RemoveVerificationByRKey removes a verification by verifier DID and rkey.
// Returns the subject DID if found, or empty string if not found.
func (s *Store) RemoveVerificationByRKey(verifierDID, rkey string) (string, error) {
	s.mu.RLock()
	subjectDID := ""
	if s.verificationsByRKey[verifierDID] != nil {
		subjectDID = s.verificationsByRKey[verifierDID][rkey]
	}
	s.mu.RUnlock()

	if subjectDID == "" {
		return "", nil
	}

	_, err := s.db.Exec(
		"DELETE FROM verifications WHERE verifier_did = ? AND rkey = ?",
		verifierDID, rkey,
	)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	if s.verificationsByRKey[verifierDID] != nil {
		delete(s.verificationsByRKey[verifierDID], rkey)
	}
	s.mu.Unlock()

	return subjectDID, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
