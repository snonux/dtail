package server

import (
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

const (
	defaultAuthKeyTTL        = 24 * time.Hour
	defaultAuthKeyMaxPerUser = 5
)

type authKeyEntry struct {
	pubKey       gossh.PublicKey
	registeredAt time.Time
}

var authKeyStore = NewAuthKeyStore(0, 0)

// AuthKeyStore is an in-memory, per-user cache of SSH public keys.
type AuthKeyStore struct {
	mu             sync.RWMutex
	keysByUser     map[string][]authKeyEntry
	ttl            time.Duration
	maxKeysPerUser int
	now            func() time.Time
}

// NewAuthKeyStore builds a thread-safe auth key store.
func NewAuthKeyStore(ttl time.Duration, maxKeysPerUser int) *AuthKeyStore {
	return newAuthKeyStoreWithClock(ttl, maxKeysPerUser, time.Now)
}

func newAuthKeyStoreWithClock(ttl time.Duration, maxKeysPerUser int,
	nowFn func() time.Time) *AuthKeyStore {

	if ttl <= 0 {
		ttl = defaultAuthKeyTTL
	}
	if maxKeysPerUser <= 0 {
		maxKeysPerUser = defaultAuthKeyMaxPerUser
	}
	if nowFn == nil {
		nowFn = time.Now
	}

	return &AuthKeyStore{
		keysByUser:     make(map[string][]authKeyEntry),
		ttl:            ttl,
		maxKeysPerUser: maxKeysPerUser,
		now:            nowFn,
	}
}

// Add stores or refreshes a key for a user.
func (s *AuthKeyStore) Add(user string, pubKey gossh.PublicKey) {
	if user == "" || pubKey == nil {
		return
	}

	now := s.now()
	offeredKey := marshalKey(pubKey)

	s.mu.Lock()
	defer s.mu.Unlock()

	userEntries := s.pruneExpiredLocked(user, now)

	newEntries := make([]authKeyEntry, 0, len(userEntries)+1)
	for _, entry := range userEntries {
		if marshalKey(entry.pubKey) == offeredKey {
			continue
		}
		newEntries = append(newEntries, entry)
	}

	newEntries = append(newEntries, authKeyEntry{
		pubKey:       pubKey,
		registeredAt: now,
	})
	if len(newEntries) > s.maxKeysPerUser {
		newEntries = newEntries[len(newEntries)-s.maxKeysPerUser:]
	}

	s.keysByUser[user] = newEntries
}

// Has returns true if a non-expired key exists for a user.
func (s *AuthKeyStore) Has(user string, pubKey gossh.PublicKey) bool {
	if user == "" || pubKey == nil {
		return false
	}

	now := s.now()
	offeredKey := marshalKey(pubKey)

	s.mu.Lock()
	defer s.mu.Unlock()

	userEntries := s.pruneExpiredLocked(user, now)
	for _, entry := range userEntries {
		if marshalKey(entry.pubKey) == offeredKey {
			return true
		}
	}

	return false
}

// Remove deletes a key for a user if it exists.
func (s *AuthKeyStore) Remove(user string, pubKey gossh.PublicKey) {
	if user == "" || pubKey == nil {
		return
	}

	offeredKey := marshalKey(pubKey)

	s.mu.Lock()
	defer s.mu.Unlock()

	userEntries := s.pruneExpiredLocked(user, s.now())
	if len(userEntries) == 0 {
		return
	}

	remaining := make([]authKeyEntry, 0, len(userEntries))
	for _, entry := range userEntries {
		if marshalKey(entry.pubKey) == offeredKey {
			continue
		}
		remaining = append(remaining, entry)
	}

	if len(remaining) == 0 {
		delete(s.keysByUser, user)
		return
	}

	s.keysByUser[user] = remaining
}

func (s *AuthKeyStore) pruneExpiredLocked(user string, now time.Time) []authKeyEntry {
	userEntries, ok := s.keysByUser[user]
	if !ok || len(userEntries) == 0 {
		delete(s.keysByUser, user)
		return nil
	}

	hasExpiredEntries := false
	for _, entry := range userEntries {
		if s.expired(entry, now) {
			hasExpiredEntries = true
			break
		}
	}
	if !hasExpiredEntries {
		return userEntries
	}

	activeEntries := make([]authKeyEntry, 0, len(userEntries))
	for _, entry := range userEntries {
		if s.expired(entry, now) {
			continue
		}
		activeEntries = append(activeEntries, entry)
	}

	if len(activeEntries) == 0 {
		delete(s.keysByUser, user)
		return nil
	}

	s.keysByUser[user] = activeEntries
	return activeEntries
}

func (s *AuthKeyStore) expired(entry authKeyEntry, now time.Time) bool {
	return !entry.registeredAt.Add(s.ttl).After(now)
}

func marshalKey(pubKey gossh.PublicKey) string {
	return string(pubKey.Marshal())
}
