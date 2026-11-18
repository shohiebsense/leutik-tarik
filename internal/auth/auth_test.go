package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// testAccounts writes a temp users file with one account and loads it. The
// hash is generated at test time so no credential ever appears in the repo.
func testAccounts(t *testing.T, password string) *Accounts {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "users.yaml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("users:\n  alice: %q\n", string(hash))), 0o600); err != nil {
		t.Fatalf("write users: %v", err)
	}
	accounts, err := LoadAccounts(path)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	return accounts
}

func TestAccountsVerify(t *testing.T) {
	accounts := testAccounts(t, "s3cret-password")
	if !accounts.Verify("alice", "s3cret-password") {
		t.Fatal("correct password not verified")
	}
	if accounts.Verify("alice", "wrong") {
		t.Fatal("wrong password accepted")
	}
	if accounts.Verify("nobody", "whatever") {
		t.Fatal("unknown user accepted")
	}
}

func TestLoadAccountsRejectsBadHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.yaml")
	if err := os.WriteFile(path, []byte("users:\n  alice: not-a-bcrypt-hash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAccounts(path); err == nil {
		t.Fatal("non-bcrypt hash accepted")
	}
}

func TestSessionRoundTrip(t *testing.T) {
	s := NewSession("test-secret")
	rec := httptest.NewRecorder()
	s.SetCookie(rec, "alice")

	req := httptest.NewRequest(http.MethodGet, "/operations", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	if user, ok := s.Validate(req); !ok || user != "alice" {
		t.Fatalf("Validate = %q, %v; want alice, true", user, ok)
	}

	// Tampered signature is rejected.
	orig := rec.Result().Cookies()[0]
	tampered := *orig
	tampered.Value = orig.Value + "x"
	req2 := httptest.NewRequest(http.MethodGet, "/operations", nil)
	req2.AddCookie(&tampered)
	if _, ok := s.Validate(req2); ok {
		t.Fatal("tampered cookie accepted")
	}
}

func TestSessionExpiry(t *testing.T) {
	s := NewSession("test-secret")
	rec := httptest.NewRecorder()
	s.SetCookie(rec, "alice")
	c := rec.Result().Cookies()[0]
	// Rewrite the cookie's expiry to the epoch (username|1|mac), past-due.
	parts := splitCookie(c.Value)
	expired := parts[0] + "|1|" + parts[2]
	req := httptest.NewRequest(http.MethodGet, "/operations", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: expired})
	if _, ok := s.Validate(req); ok {
		t.Fatal("expired cookie accepted")
	}
}

func splitCookie(v string) [3]string {
	var out [3]string
	i := 0
	start := 0
	for j, r := range v {
		if r == '|' && i < 2 {
			out[i] = v[start:j]
			start = j + 1
			i++
		}
	}
	out[2] = v[start:]
	return out
}

func TestIngestBearer(t *testing.T) {
	g := NewIngest("test-ingest-token")

	req := httptest.NewRequest(http.MethodPost, "/v1/traces", nil)
	req.Header.Set("Authorization", "Bearer test-ingest-token")
	if !g.Authorize(req) {
		t.Fatal("correct bearer token rejected")
	}

	req.Header.Set("Authorization", "Bearer wrong")
	if g.Authorize(req) {
		t.Fatal("wrong bearer token accepted")
	}

	req.Header.Del("Authorization")
	if g.Authorize(req) {
		t.Fatal("missing token accepted")
	}

	// Off (empty token) authorises everything.
	off := NewIngest("")
	if !off.Authorize(req) {
		t.Fatal("disabled ingest should authorise")
	}
	if off.Enabled() {
		t.Fatal("empty token should report disabled")
	}
}

func TestRequireUI(t *testing.T) {
	accounts := testAccounts(t, "s3cret-password")
	a := &Auth{accounts: accounts, session: NewSession("test-secret")}
	handler := a.RequireUI(func(w http.ResponseWriter, r *http.Request) {
		user, _ := User(r.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(user))
	})

	// Unauthenticated -> redirect to /login with the original path.
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/services", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("unauthenticated status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login?next=%2Fservices" {
		t.Fatalf("redirect = %q", loc)
	}

	// Authenticated -> handler sees the username in context.
	signed := httptest.NewRecorder()
	a.session.SetCookie(signed, "alice")
	req := httptest.NewRequest(http.MethodGet, "/services", nil)
	req.AddCookie(signed.Result().Cookies()[0])
	rec2 := httptest.NewRecorder()
	handler(rec2, req)
	if rec2.Code != http.StatusOK || rec2.Body.String() != "alice" {
		t.Fatalf("authenticated = %d %q", rec2.Code, rec2.Body.String())
	}
}

// TestDisabledAuthIsPassthrough proves a nil Auth guards nothing.
func TestDisabledAuthIsPassthrough(t *testing.T) {
	var a *Auth
	rec := httptest.NewRecorder()
	a.RequireUI(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })(rec, httptest.NewRequest(http.MethodGet, "/services", nil))
	if rec.Code != 204 {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                 "/operations",
		"/services":        "/services",
		"/operations?x=1":  "/operations?x=1",
		"//evil.com":       "/operations",
		"https://evil.com": "/operations",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Fatalf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
