package store

import (
	"database/sql"
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
	registeredDIDs map[string]bool
	blocks         map[string]map[string]bool   // blocker -> blocked -> true
	blocksByRKey   map[string]map[string]string // blocker -> rkey -> blocked
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
	`); err != nil {
		return nil, err
	}

	s := &Store{
		db:             db,
		registeredDIDs: make(map[string]bool),
		blocks:         make(map[string]map[string]bool),
		blocksByRKey:   make(map[string]map[string]string),
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

	return nil
}

func (s *Store) RegisterToken(actorDID, platform, pushToken, appID string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO push_tokens (actor_did, platform, push_token, app_id, updated_at)
		 VALUES (?, ?, ?, ?, datetime('now'))`,
		actorDID, platform, pushToken, appID,
	)
	if err != nil {
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

func (s *Store) Close() error {
	return s.db.Close()
}
