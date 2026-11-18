// Package auth provides in-app authentication, replacing the Caddy basic_auth
// edge workaround: the platform authenticates its own viewers now, so no
// reverse proxy has to re-hash a password on every request.
//
// Three pieces:
//   - Accounts: a username -> bcrypt-hash map loaded from a YAML file. bcrypt
//     runs once per sign-in (not per request), so a high cost is fine here.
//   - Session: an HMAC-SHA256-signed cookie for the browser UI. Every request
//     after sign-in is a cheap HMAC check, no password hashing.
//   - Ingest: a bearer token for machine traffic (the OTLP exporter). Compared
//     in constant time, so trace export stays fast.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

// SessionTTL is how long one browser sign-in lasts.
const SessionTTL = 30 * 24 * time.Hour

const cookieName = "leutik_session"

// Accounts is a username -> bcrypt-hash map.
type Accounts struct {
	users map[string][]byte
}

// LoadAccounts reads a users file of the form:
//
//	users:
//	  shohieb: "$2b$10$..."
//
// Passwords are bcrypt hashes; make one with `caddy hash-password --plaintext`
// or `htpasswd -nB <user>`.
func LoadAccounts(path string) (*Accounts, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read users: %w", err)
	}
	var f struct {
		Users map[string]string `yaml:"users"`
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse users: %w", err)
	}
	if len(f.Users) == 0 {
		return nil, fmt.Errorf("users file %q defines no users", path)
	}
	a := &Accounts{users: make(map[string][]byte, len(f.Users))}
	for name, hash := range f.Users {
		name = strings.TrimSpace(name)
		hash = strings.TrimSpace(hash)
		if name == "" || hash == "" {
			return nil, fmt.Errorf("users file %q has an empty username or hash", path)
		}
		if strings.ContainsAny(name, "|\n") {
			return nil, fmt.Errorf("user %q: usernames cannot contain '|' or newlines", name)
		}
		if _, err := bcrypt.Cost([]byte(hash)); err != nil {
			return nil, fmt.Errorf("user %q: not a bcrypt hash: %w", name, err)
		}
		a.users[name] = []byte(hash)
	}
	return a, nil
}

// Verify reports whether password matches the account. An unknown username is
// compared against a fake hash so response timing does not reveal which
// usernames exist.
func (a *Accounts) Verify(username, password string) bool {
	hash, ok := a.users[username]
	if !ok {
		_ = bcrypt.CompareHashAndPassword([]byte(fakeHash), []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}

// fakeHash is a bcrypt hash of a random string at the same cost as the seeded
// accounts, used only to equalise the timing of unknown usernames.
const fakeHash = "$2b$10$tu2FYczV1mjIasdLHImeUeXsiISihjuDk3lT9n0EamT0BO47yYoK6"

// Session signs and verifies browser session cookies.
type Session struct {
	secret []byte
}

// NewSession builds a signer. An empty secret generates an ephemeral one:
// sessions then do not survive a restart. Set LEUTIK_SESSION_SECRET to keep
// sign-ins stable across restarts and replicas.
func NewSession(secret string) *Session {
	if secret == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		secret = base64.RawURLEncoding.EncodeToString(b)
	}
	return &Session{secret: []byte(secret)}
}

// SetCookie writes a signed session cookie for username.
func (s *Session) SetCookie(w http.ResponseWriter, username string) {
	exp := time.Now().Add(SessionTTL).Unix()
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    username + "|" + strconv.FormatInt(exp, 10) + "|" + s.mac(username, exp),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(SessionTTL),
	})
}

// Clear removes the session cookie.
func (s *Session) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}

// Validate returns the username from a valid, unexpired cookie.
func (s *Session) Validate(r *http.Request) (string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	parts := strings.Split(c.Value, "|")
	if len(parts) != 3 {
		return "", false
	}
	username, expStr, mac := parts[0], parts[1], parts[2]
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	if !hmac.Equal([]byte(mac), []byte(s.mac(username, exp))) {
		return "", false
	}
	return username, true
}

func (s *Session) mac(username string, exp int64) string {
	h := hmac.New(sha256.New, s.secret)
	fmt.Fprintf(h, "%s|%d", username, exp)
	return hex.EncodeToString(h.Sum(nil))
}

// Ingest authorises machine traffic with a bearer token.
type Ingest struct {
	token []byte
}

// NewIngest builds an ingest gate. An empty token authorises everything
// (off), which is the pre-auth behaviour for a fresh install.
func NewIngest(token string) *Ingest {
	return &Ingest{token: []byte(token)}
}

// Enabled reports whether ingest authentication is on.
func (g *Ingest) Enabled() bool { return len(g.token) > 0 }

// Authorize reports whether the request carries the right bearer token.
func (g *Ingest) Authorize(r *http.Request) bool {
	if len(g.token) == 0 {
		return true
	}
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	return subtle.ConstantTimeCompare(g.token, []byte(h[len(prefix):])) == 1
}

// Auth ties the three pieces into route guards and the login/logout handlers.
type Auth struct {
	accounts *Accounts
	session  *Session
	ingest   *Ingest
}

// New wires accounts, session and ingest into one Auth.
func New(accounts *Accounts, session *Session, ingest *Ingest) *Auth {
	return &Auth{accounts: accounts, session: session, ingest: ingest}
}

// Enabled reports whether UI authentication is on (accounts were loaded).
func (a *Auth) Enabled() bool { return a != nil && a.accounts != nil }

// RequireUI guards an HTML route: without a valid session it redirects to the
// login page, carrying the original path so sign-in lands back there.
func (a *Auth) RequireUI(next http.HandlerFunc) http.HandlerFunc {
	if a == nil || a.accounts == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		username, ok := a.session.Validate(r)
		if !ok {
			target := r.URL.RequestURI()
			http.Redirect(w, r, "/login?next="+url.QueryEscape(target), http.StatusFound)
			return
		}
		next(w, r.WithContext(WithUser(r.Context(), username)))
	}
}

// RequireIngest guards a machine route with the bearer token. Off (no token)
// means the route stays open, matching the pre-auth install.
func (a *Auth) RequireIngest(next http.Handler) http.Handler {
	if a == nil || a.ingest == nil || !a.ingest.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.ingest.Authorize(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// LoginPOST verifies credentials and starts a session. On success it redirects
// to next (a same-origin path) or /operations. On failure it redirects back to
// the login page with error=1.
func (a *Auth) LoginPOST(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username := r.FormValue("username")
	password := r.FormValue("password")
	if !a.accounts.Verify(username, password) {
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}
	a.session.SetCookie(w, username)
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}

// Logout clears the session and returns to the login page.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	a.session.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// safeNext returns a same-origin redirect target, falling back to /operations.
// A bare "/" is allowed; "//evil" and anything absolute are not.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/operations"
	}
	return next
}

type ctxKey struct{}

// WithUser stores the authenticated username in the request context.
func WithUser(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, ctxKey{}, username)
}

// User returns the authenticated username, if any.
func User(ctx context.Context) (string, bool) {
	u, ok := ctx.Value(ctxKey{}).(string)
	return u, ok
}
