package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

const oauthLifetime = 10 * time.Minute

type GitHubConfig struct {
	ClientID, ClientSecret, RedirectURL, FrontendOrigin string
}

type githubOAuth struct {
	GitHubConfig
	client                         *http.Client
	authorizeURL, tokenURL, apiURL string
}

// ConfigureGitHub must run before RegisterRoutes. Empty credentials disable OAuth.
func (a *API) ConfigureGitHub(config GitHubConfig) error {
	if config.ClientID == "" && config.ClientSecret == "" && config.RedirectURL == "" {
		return nil
	}
	if config.ClientID == "" || config.ClientSecret == "" || config.RedirectURL == "" {
		return errors.New("GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET and GITHUB_REDIRECT_URL must all be set")
	}
	u, err := url.Parse(config.RedirectURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/auth/github/callback" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && !a.secure && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return errors.New("GITHUB_REDIRECT_URL must be an HTTPS URL ending in /auth/github/callback (local HTTP requires AUTH_INSECURE_COOKIES=true)")
	}
	if config.FrontendOrigin != "" {
		origin, err := url.Parse(config.FrontendOrigin)
		if err != nil || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" || (origin.Scheme != "http" && origin.Scheme != "https") {
			return errors.New("FRONTEND_ORIGIN must be an HTTP or HTTPS origin without a path")
		}
	}
	a.github = &githubOAuth{GitHubConfig: config,
		client:       &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		authorizeURL: "https://github.com/login/oauth/authorize", tokenURL: "https://github.com/login/oauth/access_token", apiURL: "https://api.github.com"}
	return nil
}

func randomOAuthToken() string {
	var secret [32]byte
	_, _ = rand.Read(secret[:])
	return base64.RawURLEncoding.EncodeToString(secret[:])
}

func (a *API) flowCookie(w http.ResponseWriter, state string, maxAge int) {
	name := "gorequest_oauth"
	if a.secure {
		name = "__Host-" + name
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: state, Path: "/", HttpOnly: true, Secure: a.secure,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge, Expires: time.Now().Add(time.Duration(maxAge) * time.Second)})
}

func (a *API) githubStart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	state, verifier := randomOAuthToken(), randomOAuthToken()
	hash := sha256.Sum256([]byte(state))
	var previousHash []byte
	if previous := a.token(r); previous != "" {
		previous := sha256.Sum256([]byte(previous))
		previousHash = previous[:]
	}
	if _, err := a.store.db.ExecContext(r.Context(), `DELETE FROM oauth_flows WHERE expires_at <= ?`, time.Now().Unix()); err != nil {
		fail(w, 500, "unable to start GitHub sign-in")
		return
	}
	if _, err := a.store.db.ExecContext(r.Context(), `INSERT INTO oauth_flows(state_hash, verifier, previous_hash, expires_at) VALUES (?, ?, ?, ?)`, hash[:], verifier, previousHash, time.Now().Add(oauthLifetime).Unix()); err != nil {
		fail(w, 500, "unable to start GitHub sign-in")
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{"client_id": {a.github.ClientID}, "redirect_uri": {a.github.RedirectURL}, "scope": {"read:user user:email"}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	a.flowCookie(w, state, int(oauthLifetime.Seconds()))
	http.Redirect(w, r, a.github.authorizeURL+"?"+query.Encode(), http.StatusFound)
}

func (a *API) githubCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	name := "gorequest_oauth"
	if a.secure {
		name = "__Host-" + name
	}
	cookie, err := r.Cookie(name)
	state := r.URL.Query().Get("state")
	if err != nil || len(state) != 43 || subtle.ConstantTimeCompare([]byte(state), []byte(cookie.Value)) != 1 {
		fail(w, 400, "invalid OAuth state; start sign-in again")
		return
	}
	a.flowCookie(w, "", -1)
	hash := sha256.Sum256([]byte(state))
	var verifier string
	var previousHash []byte
	err = a.store.db.QueryRowContext(r.Context(), `DELETE FROM oauth_flows WHERE state_hash = ? AND expires_at > ? RETURNING verifier, previous_hash`, hash[:], time.Now().Unix()).Scan(&verifier, &previousHash)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, 400, "expired or already used OAuth state")
		return
	}
	if err != nil {
		fail(w, 500, "unable to complete GitHub sign-in")
		return
	}
	if r.URL.Query().Get("error") != "" {
		fail(w, 400, "GitHub authorization was denied")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 1024 {
		fail(w, 400, "missing or invalid GitHub authorization code")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	githubID, email, err := a.github.identity(ctx, code, verifier)
	if err != nil {
		fail(w, 502, "GitHub sign-in failed; ensure your GitHub primary email is verified and try again")
		return
	}
	u, err := a.store.githubUser(ctx, githubID, email)
	if errors.Is(err, errEmailExists) {
		fail(w, 409, "email belongs to an existing account; account migration is required")
		return
	}
	if err != nil {
		fail(w, 500, "unable to create GitHub account")
		return
	}
	token, expires, err := a.store.newSessionWithHash(ctx, u.ID, previousHash)
	if err != nil {
		fail(w, 500, "unable to create session")
		return
	}
	a.cookie(w, token, expires, int(sessionLifetime.Seconds()))
	if a.github.FrontendOrigin != "" {
		http.Redirect(w, r, a.github.FrontendOrigin+"/", http.StatusSeeOther)
		return
	}
	respond(w, 200, map[string]any{"user": u})
}

func (g *githubOAuth) identity(ctx context.Context, code, verifier string) (int64, string, error) {
	form := url.Values{"client_id": {g.ClientID}, "client_secret": {g.ClientSecret}, "code": {code}, "redirect_uri": {g.RedirectURL}, "code_verifier": {verifier}}
	r, err := http.NewRequestWithContext(ctx, "POST", g.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, "", err
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		TokenType   string `json:"token_type"`
	}
	if err := g.json(r, &token); err != nil {
		return 0, "", err
	}
	if token.Error != "" || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "bearer") {
		return 0, "", errors.New("invalid token response")
	}
	get := func(path string, target any) error {
		r, err := http.NewRequestWithContext(ctx, "GET", g.apiURL+path, nil)
		if err != nil {
			return err
		}
		r.Header.Set("Authorization", "Bearer "+token.AccessToken)
		return g.json(r, target)
	}
	var profile struct {
		ID int64 `json:"id"`
	}
	if err := get("/user", &profile); err != nil {
		return 0, "", err
	}
	if profile.ID <= 0 {
		return 0, "", errors.New("invalid GitHub user ID")
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := get("/user/emails?per_page=100", &emails); err != nil {
		return 0, "", err
	}
	for _, entry := range emails {
		email := strings.ToLower(strings.TrimSpace(entry.Email))
		address, err := mail.ParseAddress(email)
		if entry.Primary && entry.Verified && err == nil && address.Address == email && len(email) <= 254 {
			return profile.ID, email, nil
		}
	}
	return 0, "", errors.New("verified primary email required")
}

func (g *githubOAuth) json(r *http.Request, target any) error {
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", "GoRequest")
	response, err := g.client.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(body) > 1<<20 {
		return errors.New("GitHub response too large")
	}
	return json.Unmarshal(body, target)
}

func (s *Store) githubUser(ctx context.Context, githubID int64, email string) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var u User
	err = tx.QueryRowContext(ctx, `SELECT users.id, users.email FROM github_identities JOIN users ON users.id = user_id WHERE github_id = ?`, githubID).Scan(&u.ID, &u.Email)
	if err == nil {
		return u, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return User{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO users(email) VALUES (?) ON CONFLICT(email) DO NOTHING`, email)
	if err != nil {
		return User{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return User{}, err
	}
	if n == 0 {
		return User{}, errEmailExists
	}
	id, err := result.LastInsertId()
	if err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO github_identities(github_id, user_id) VALUES (?, ?)`, githubID, id); err != nil {
		return User{}, err
	}
	return User{ID: id, Email: email}, tx.Commit()
}
