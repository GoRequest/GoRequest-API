package auth

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testCredentials = `{"email":"Person@Example.com","password":"a long test password"}`

func setup(t *testing.T, secure bool) (*Store, *API, http.Handler) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a, err := NewAPI(s, secure)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	mux.Handle("POST /requests/execute", a.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := CurrentUser(r.Context()); !ok {
			t.Error("missing identity")
		}
		w.WriteHeader(200)
	})))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h, err := BrowserProtection(mux, "http://localhost:3000")
	if err != nil {
		t.Fatal(err)
	}
	return s, a, h
}

func request(h http.Handler, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost:8080"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func status(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body.String())
	}
}

func TestAccountLifecycle(t *testing.T) {
	s, _, h := setup(t, true)
	status(t, request(h, "GET", "/health", "", nil), 200)
	status(t, request(h, "POST", "/requests/execute", "{}", nil), 401)
	w := request(h, "POST", "/auth/signup", testCredentials, nil)
	status(t, w, 201)
	if strings.Contains(w.Body.String(), "password") {
		t.Fatal("password exposed")
	}
	var response struct {
		User User `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.User.Email != "person@example.com" {
		t.Fatalf("unexpected user: %+v", response)
	}
	_, hash, err := s.credentials(t.Context(), "person@example.com")
	if err != nil || !verifyPassword("a long test password", hash) || strings.Contains(hash, "test password") {
		t.Fatal("password was not hashed correctly")
	}
	status(t, request(h, "POST", "/auth/signup", testCredentials, nil), 409)
	wrong := request(h, "POST", "/auth/login", `{"email":"person@example.com","password":"wrong"}`, nil)
	unknown := request(h, "POST", "/auth/login", `{"email":"unknown@example.com","password":"wrong"}`, nil)
	status(t, wrong, 401)
	status(t, unknown, 401)
	if wrong.Body.String() != unknown.Body.String() {
		t.Fatal("login discloses account existence")
	}
	w = request(h, "POST", "/auth/login", testCredentials, nil)
	status(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("expected session cookie")
	}
	c := cookies[0]
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.Name != "__Host-gorequest_session" || c.MaxAge != int(sessionLifetime.Seconds()) {
		t.Fatalf("unsafe cookie: %+v", c)
	}
	var stored []byte
	if err := s.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte(c.Value))
	if string(stored) != string(expected[:]) {
		t.Fatal("raw session token stored")
	}
	status(t, request(h, "GET", "/auth/me", "", c), 200)
	status(t, request(h, "POST", "/requests/execute", "{}", c), 200)
	forged := *c
	forged.Value = strings.Repeat("x", 43)
	status(t, request(h, "GET", "/auth/me", "", &forged), 401)
	// Signing in rotates the browser's previous session.
	w = request(h, "POST", "/auth/login", testCredentials, c)
	status(t, w, 200)
	status(t, request(h, "GET", "/auth/me", "", c), 401)
	c = w.Result().Cookies()[0]
	w = request(h, "POST", "/auth/logout", "", c)
	status(t, w, 204)
	if w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("cookie was not cleared")
	}
	status(t, request(h, "POST", "/requests/execute", "{}", c), 401)
	status(t, request(h, "POST", "/auth/logout", "", nil), 204)
}

func TestPersistenceAndExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persistent.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.createUser(t.Context(), "persist@example.com", "test hash")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.newSession(t.Context(), u.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.sessionUser(t.Context(), token)
	if err != nil || got != u {
		t.Fatalf("session lost on restart: %+v %v", got, err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET expires_at = ?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sessionUser(t.Context(), token); err == nil {
		t.Fatal("expired session accepted")
	}
}

func TestValidationAndThrottling(t *testing.T) {
	_, _, h := setup(t, false)
	for _, body := range []string{
		`null`, `{}`, `{"email":"invalid","password":"a long password"}`,
		`{"email":"user@example.com","password":"short"}`, testCredentials + ` {}`,
		`{"email":"user@example.com","password":"a long password","admin":true}`,
	} {
		status(t, request(h, "POST", "/auth/signup", body, nil), 400)
	}
	status(t, request(h, "POST", "/auth/signup", `{"password":"`+strings.Repeat("x", 9000)+`"}`, nil), 413)
	r := httptest.NewRequest("POST", "/auth/login", strings.NewReader(testCredentials))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	status(t, w, 415)
	for range 2 {
		status(t, request(h, "POST", "/auth/login", `{}`, nil), 400)
	}
	w = request(h, "POST", "/auth/login", testCredentials, nil)
	status(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing retry hint")
	}
}

func TestBrowserProtection(t *testing.T) {
	_, _, h := setup(t, false)
	for _, origin := range []string{"https://evil.example", "http://localhost:3000"} {
		r := httptest.NewRequest("POST", "http://localhost:8080/auth/signup", strings.NewReader(testCredentials))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if origin == "https://evil.example" {
			status(t, w, 403)
		} else {
			status(t, w, 201)
			if w.Header().Get("Access-Control-Allow-Origin") != origin || w.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Fatal("missing credentialed CORS")
			}
		}
	}
	r := httptest.NewRequest("OPTIONS", "http://localhost:8080/auth/login", nil)
	r.Header.Set("Origin", "http://localhost:3000")
	r.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	status(t, w, 204)
	if _, err := BrowserProtection(h, "not-an-origin"); err == nil {
		t.Fatal("invalid origin accepted")
	}
}

func TestPasswordSaltsAndVerification(t *testing.T) {
	first, err := hashPassword("a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	second, err := hashPassword("a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("password salts reused")
	}
	if !verifyPassword("a sufficiently long password", first) || verifyPassword("wrong", first) {
		t.Fatal("incorrect password verification")
	}
	for _, invalid := range []string{"", "$argon2id$v=19$m=19456,t=2,p=1$bad$bad", "$unknown$"} {
		if verifyPassword("password", invalid) {
			t.Fatal("invalid hash accepted")
		}
	}
}

func TestAuthenticationConcurrencyBound(t *testing.T) {
	_, a, h := setup(t, false)
	// Simulate both expensive password slots being occupied.
	a.workers <- struct{}{}
	a.workers <- struct{}{}
	status(t, request(h, "POST", "/auth/login", testCredentials, nil), 429)
	<-a.workers
	<-a.workers
	// Exhaust one peer's quota even when it supplies spoofed forwarding headers.
	for range 9 {
		r := httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", "203.0.113.20")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		status(t, w, 400)
	}
	status(t, request(h, "POST", "/auth/login", testCredentials, nil), 429)
	// An unrelated direct peer still gets its own quota.
	r := httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{}`))
	r.RemoteAddr = "203.0.113.21:1234"
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	status(t, w, 400)
}
