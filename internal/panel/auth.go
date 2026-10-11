package panel

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"sync"
	"time"
)

const (
	codeDigits      = 8
	codeLifetime    = 10 * time.Minute
	codeAttempts    = 5
	attemptInterval = time.Second
	// sessionIdle ends a browser session that made no request for this long.
	// Sessions live in memory only: they never outlive the panel process.
	sessionIdle = 12 * time.Hour
)

var (
	errNoCode      = errors.New("no pairing code is active: create a new one where the panel was started")
	errBadCode     = errors.New("wrong pairing code")
	errTooFast     = errors.New("too many attempts: wait a second")
	errCodeExpired = errors.New("the pairing code expired: create a new one where the panel was started")
)

type session struct {
	Created  time.Time
	LastSeen time.Time
	CSRF     string
	// done is closed when the session ends (logout, revoke-all, idle
	// expiry), so a long-lived stream of that browser can stop at once.
	done chan struct{}
}

// auth holds the pairing code and the sessions. Sessions exist only in
// memory: a new panel process starts with none, so no credential from an
// earlier process can ever be valid again.
type auth struct {
	mu          sync.Mutex
	sessions    map[string]*session // keyed by SHA-256 of the cookie token
	code        string
	codeExpires time.Time
	attempts    int
	lastTry     map[string]time.Time
	now         func() time.Time
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// openAuth starts a fresh authentication lifecycle. Versions up to 0.1.x of
// the panel persisted sessions in legacyPath; that file is deleted, and if it
// cannot be deleted the panel refuses to start rather than leave a store that
// an older binary would honour.
func openAuth(legacyPath string) (*auth, error) {
	if err := os.Remove(legacyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("cannot discard old panel sessions: %w", err)
	}
	return &auth{sessions: map[string]*session{}, lastTry: map[string]time.Time{}, now: time.Now}, nil
}

// endLocked drops a session and wakes its streams.
func (a *auth) endLocked(key string) {
	if s, ok := a.sessions[key]; ok {
		close(s.done)
		delete(a.sessions, key)
	}
}

// revokeAll ends every session and the active pairing code, in memory,
// before returning.
func (a *auth) revokeAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k := range a.sessions {
		a.endLocked(k)
	}
	a.code = ""
	a.attempts = 0
}

// newCode replaces the pairing code: 8 random digits, valid 10 minutes or
// until 5 wrong attempts or one success.
func (a *auth) newCode() (string, time.Time, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		return "", time.Time{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.code = fmt.Sprintf("%0*d", codeDigits, n.Int64())
	a.codeExpires = a.now().Add(codeLifetime)
	a.attempts = 0
	return a.code, a.codeExpires, nil
}

// pair exchanges the pairing code for a new session. remote is the
// client's address, for the per-address rate limit.
func (a *auth) pair(code, remote string) (token, csrf string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if last, ok := a.lastTry[remote]; ok && now.Sub(last) < attemptInterval {
		return "", "", errTooFast
	}
	a.lastTry[remote] = now
	if len(a.lastTry) > 1024 {
		a.lastTry = map[string]time.Time{remote: now}
	}
	if a.code == "" {
		return "", "", errNoCode
	}
	if now.After(a.codeExpires) {
		a.code = ""
		return "", "", errCodeExpired
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(a.code)) != 1 {
		a.attempts++
		if a.attempts >= codeAttempts {
			a.code = ""
		}
		return "", "", errBadCode
	}
	a.code = "" // single use
	if token, err = randomToken(32); err != nil {
		return "", "", err
	}
	if csrf, err = randomToken(32); err != nil {
		return "", "", err
	}
	a.sessions[tokenKey(token)] = &session{Created: now, LastSeen: now, CSRF: csrf, done: make(chan struct{})}
	return token, csrf, nil
}

// codeActive reports whether a pairing code can still be used.
func (a *auth) codeActive() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.code != "" && !a.now().After(a.codeExpires)
}

// check returns the session of a cookie token, if it is valid.
func (a *auth) check(token string) (session, bool) {
	if token == "" {
		return session{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := tokenKey(token)
	s, ok := a.sessions[key]
	now := a.now()
	if !ok {
		return session{}, false
	}
	if now.Sub(s.LastSeen) >= sessionIdle {
		a.endLocked(key)
		return session{}, false
	}
	s.LastSeen = now
	return *s, true
}

// csrfOK compares a request's CSRF header with its session's token.
func csrfOK(s session, header string) bool {
	return header != "" && subtle.ConstantTimeCompare([]byte(header), []byte(s.CSRF)) == 1
}

func (a *auth) logout(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.endLocked(tokenKey(token))
}

// RemoveLegacySessions deletes the session file older versions wrote.
func RemoveLegacySessions(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
