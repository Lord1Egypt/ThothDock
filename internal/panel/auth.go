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
	"sync"
	"time"

	"github.com/Lord1Egypt/ThothDock/internal/store"
)

const (
	codeDigits      = 8
	codeLifetime    = 10 * time.Minute
	codeAttempts    = 5
	attemptInterval = time.Second
	sessionIdle     = 30 * 24 * time.Hour
	// lastSeenGranularity limits how often a session's last use is written
	// back to disk: the panel must not write on every request.
	lastSeenGranularity = time.Hour
)

var (
	errNoCode      = errors.New("no pairing code is active: create a new one where the panel was started")
	errBadCode     = errors.New("wrong pairing code")
	errTooFast     = errors.New("too many attempts: wait a second")
	errCodeExpired = errors.New("the pairing code expired: create a new one where the panel was started")
)

type session struct {
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"lastSeen"`
	CSRF     string    `json:"csrf"`
}

type sessionFile struct {
	// Sessions are keyed by the SHA-256 of the cookie token, so the file
	// never holds a usable credential.
	Sessions map[string]*session `json:"sessions"`
}

// auth holds the pairing code and the sessions.
type auth struct {
	mu          sync.Mutex
	path        string
	sessions    map[string]*session
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

func openAuth(path string) (*auth, error) {
	a := &auth{path: path, sessions: map[string]*session{}, lastTry: map[string]time.Time{}, now: time.Now}
	var f sessionFile
	if err := store.ReadJSON(path, &f); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for k, s := range f.Sessions {
		if s != nil && len(k) == 64 && s.CSRF != "" && a.now().Sub(s.LastSeen) < sessionIdle {
			a.sessions[k] = s
		}
	}
	return a, nil
}

func (a *auth) saveLocked() error {
	return store.WriteJSONAtomic(a.path, &sessionFile{Sessions: a.sessions})
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
	a.sessions[tokenKey(token)] = &session{Created: now, LastSeen: now, CSRF: csrf}
	if err := a.saveLocked(); err != nil {
		delete(a.sessions, tokenKey(token))
		return "", "", err
	}
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
		delete(a.sessions, key)
		a.saveLocked()
		return session{}, false
	}
	if now.Sub(s.LastSeen) >= lastSeenGranularity {
		s.LastSeen = now
		a.saveLocked()
	}
	return *s, true
}

// csrfOK compares a request's CSRF header with its session's token.
func csrfOK(s session, header string) bool {
	return header != "" && subtle.ConstantTimeCompare([]byte(header), []byte(s.CSRF)) == 1
}

func (a *auth) logout(token string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, tokenKey(token))
	return a.saveLocked()
}

// RevokeAll ends every session stored at path.
func RevokeAll(path string) error {
	return store.WriteJSONAtomic(path, &sessionFile{Sessions: map[string]*session{}})
}
