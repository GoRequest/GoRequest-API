package auth

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeGitHub struct {
	id         int64
	email      string
	verified   bool
	challenge  string
	tokenCalls int
	failToken  bool
}

func setup(t *testing.T, secure bool) (*Store, *API, http.Handler, *fakeGitHub) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a := NewAPI(s, secure)
	scheme := "http"
	if secure {
		scheme = "https"
	}
	if err := a.ConfigureGitHub(GitHubConfig{ClientID: "test-client", ClientSecret: "test-secret", RedirectURL: scheme + "://localhost:8080/auth/github/callback"}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeGitHub{id: 42, email: "person@example.com", verified: true}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			fake.tokenCalls++
			if fake.failToken {
				w.WriteHeader(500)
				return
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(challenge[:]) != fake.challenge || r.Form.Get("client_secret") != "test-secret" || r.Form.Get("redirect_uri") != a.github.RedirectURL || r.Form.Get("code") != "test-code" {
				t.Error("invalid code exchange or PKCE verifier")
			}
			_, _ = w.Write([]byte(`{"access_token":"private-github-token","token_type":"bearer"}`))
		case "/user":
			if r.Header.Get("Authorization") != "Bearer private-github-token" {
				t.Error("missing GitHub token")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": fake.id})
		case "/user/emails":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"email": fake.email, "primary": true, "verified": fake.verified}})
		default:
			t.Errorf("unexpected GitHub request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(upstream.Close)
	a.github.tokenURL, a.github.apiURL = upstream.URL+"/token", upstream.URL
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	mux.Handle("POST /requests/execute", a.OptionalUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := CurrentUser(r.Context()); ok {
			w.Header().Set("Test-User", u.Email)
		}
		w.WriteHeader(200)
	})))
	mux.Handle("GET /members", a.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := CurrentUser(r.Context()); !ok {
			t.Error("missing user context")
		}
		w.WriteHeader(200)
	})))
	h, err := BrowserProtection(mux, "http://localhost:3000")
	if err != nil {
		t.Fatal(err)
	}
	return s, a, h, fake
}

func request(h http.Handler, method, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost:8080"+path, nil)
	for _, cookie := range cookies {
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

func start(t *testing.T, h http.Handler, fake *fakeGitHub, cookies ...*http.Cookie) (string, *http.Cookie) {
	t.Helper()
	w := request(h, "GET", "/auth/github", cookies...)
	status(t, w, 302)
	u, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != "github.com" || q.Get("client_id") != "test-client" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != "read:user user:email" || strings.Contains(u.String(), "test-secret") {
		t.Fatal("invalid authorization redirect")
	}
	fake.challenge = q.Get("code_challenge")
	cookie := w.Result().Cookies()[0]
	if cookie.SameSite != http.SameSiteLaxMode || !cookie.HttpOnly || cookie.Path != "/" || cookie.MaxAge != 600 {
		t.Fatal("invalid OAuth cookie")
	}
	return "/auth/github/callback?code=test-code&state=" + q.Get("state"), cookie
}

func sessionCookie(t *testing.T, a *API, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == a.cookieName() {
			return cookie
		}
	}
	t.Fatal("session cookie missing")
	return nil
}

func TestGitHubLifecycleAndGuestAccess(t *testing.T) {
	s, a, h, fake := setup(t, true)
	status(t, request(h, "POST", "/requests/execute"), 200)
	status(t, request(h, "GET", "/auth/me"), 401)
	status(t, request(h, "GET", "/members"), 401)
	status(t, request(h, "POST", "/auth/signup"), 404)
	status(t, request(h, "POST", "/auth/login"), 404)
	path, flow := start(t, h, fake)
	if !flow.Secure || flow.Name != "__Host-gorequest_oauth" {
		t.Fatal("insecure OAuth cookie")
	}
	w := request(h, "GET", path, flow)
	status(t, w, 200)
	if strings.Contains(w.Body.String(), "private-github-token") {
		t.Fatal("GitHub token exposed")
	}
	cookie := sessionCookie(t, a, w)
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != int(sessionLifetime.Seconds()) {
		t.Fatal("invalid session cookie")
	}
	var stored []byte
	if err := s.db.QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte(cookie.Value))
	if string(stored) != string(expected[:]) {
		t.Fatal("raw session stored")
	}
	status(t, request(h, "GET", "/auth/me", cookie), 200)
	status(t, request(h, "GET", "/members", cookie), 200)
	if request(h, "POST", "/requests/execute", cookie).Header().Get("Test-User") != "person@example.com" {
		t.Fatal("public endpoint lost signed-in identity")
	}
	status(t, request(h, "GET", path, flow), 400)
	// GitHub ID remains the identity even after the primary email changes.
	fake.email = "changed@example.com"
	path, flow = start(t, h, fake, cookie)
	w = request(h, "GET", path, flow)
	status(t, w, 200)
	status(t, request(h, "GET", "/auth/me", cookie), 401)
	cookie = sessionCookie(t, a, w)
	var users int
	if err := s.db.QueryRow(`SELECT count(*) FROM users`).Scan(&users); err != nil || users != 1 {
		t.Fatalf("identity duplicated: %d %v", users, err)
	}
	status(t, request(h, "POST", "/auth/logout", cookie), 204)
	status(t, request(h, "GET", "/auth/me", cookie), 401)
	status(t, request(h, "POST", "/requests/execute", cookie), 200)
}

func TestOAuthRejections(t *testing.T) {
	for _, scenario := range []string{"missing cookie", "wrong state", "expired", "denied", "missing code", "unverified email", "upstream failure", "email collision"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, h, fake := setup(t, false)
			path, flow := start(t, h, fake)
			want := 400
			switch scenario {
			case "missing cookie":
				status(t, request(h, "GET", path), want)
				return
			case "wrong state":
				flow.Value = strings.Repeat("x", 43)
			case "expired":
				if _, err := s.db.Exec(`UPDATE oauth_flows SET expires_at = 0`); err != nil {
					t.Fatal(err)
				}
			case "denied":
				path += "&error=access_denied"
			case "missing code":
				path = strings.ReplaceAll(path, "code=test-code&", "")
			case "unverified email":
				fake.verified = false
				want = 502
			case "upstream failure":
				fake.failToken = true
				want = 502
			case "email collision":
				if _, err := s.db.Exec(`INSERT INTO users(email) VALUES (?)`, fake.email); err != nil {
					t.Fatal(err)
				}
				want = 409
			}
			status(t, request(h, "GET", path, flow), want)
			if want == 400 && fake.tokenCalls != 0 {
				t.Fatal("invalid callback contacted GitHub")
			}
			var sessions int
			if err := s.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
				t.Fatal("failed sign-in created session")
			}
		})
	}
}

func TestFrontendRedirectAndBrowserProtection(t *testing.T) {
	_, a, h, fake := setup(t, false)
	a.github.FrontendOrigin = "http://localhost:3000"
	path, flow := start(t, h, fake)
	w := request(h, "GET", path, flow)
	status(t, w, 303)
	if w.Header().Get("Location") != "http://localhost:3000/" {
		t.Fatal("incorrect frontend redirect")
	}
	for _, origin := range []string{"https://evil.example", "http://localhost:3000"} {
		r := httptest.NewRequest("POST", "http://localhost:8080/requests/execute", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if origin == "https://evil.example" {
			status(t, w, 403)
		} else {
			status(t, w, 200)
			if w.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Fatal("missing CORS")
			}
		}
	}
	r := httptest.NewRequest("OPTIONS", "http://localhost:8080/auth/logout", nil)
	r.Header.Set("Origin", "http://localhost:3000")
	r.Header.Set("Access-Control-Request-Method", "POST")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	status(t, w, 204)
}

func TestSessionPersistenceAndExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persistent.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.githubUser(t.Context(), 42, "person@example.com")
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
		t.Fatalf("session lost: %+v %v", got, err)
	}
	if _, err := s.db.Exec(`UPDATE sessions SET expires_at = ?`, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sessionUser(t.Context(), token); err == nil {
		t.Fatal("expired session accepted")
	}
}

func TestLegacyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL);
	INSERT INTO users VALUES(1, 'legacy@example.com', 'old-hash');
	CREATE TABLE sessions (token_hash BLOB PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id), expires_at INTEGER NOT NULL);
	INSERT INTO sessions VALUES(x'1234', 1, 9999999999);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, query := range []string{`SELECT count(*) FROM sessions`, `SELECT count(*) FROM pragma_table_info('users') WHERE name = 'password_hash'`} {
		var n int
		if err := s.db.QueryRow(query).Scan(&n); err != nil || n != 0 {
			t.Fatalf("migration failed: %d %v", n, err)
		}
	}
	var email string
	if err := s.db.QueryRow(`SELECT email FROM users WHERE id = 1`).Scan(&email); err != nil || email != "legacy@example.com" {
		t.Fatal("legacy user lost")
	}
	if _, err := s.githubUser(t.Context(), 42, "new@example.com"); err != nil {
		t.Fatal(err)
	}
}

func TestConfigAndThrottle(t *testing.T) {
	_, a, h, _ := setup(t, false)
	for _, config := range []GitHubConfig{
		{ClientID: "partial"},
		{ClientID: "id", ClientSecret: "secret", RedirectURL: "https://example.com/wrong"},
		{ClientID: "id", ClientSecret: "secret", RedirectURL: "http://example.com/auth/github/callback"},
	} {
		if a.ConfigureGitHub(config) == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	for range 10 {
		status(t, request(h, "GET", "/auth/github"), 302)
	}
	w := request(h, "GET", "/auth/github")
	status(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing retry hint")
	}
	status(t, request(h, "POST", "/requests/execute"), 200)
}
