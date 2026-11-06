package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

type userContextKey struct{}

// CurrentUser returns the identity set by RequireUser.
func CurrentUser(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userContextKey{}).(User)
	return u, ok
}

type API struct {
	store     *Store
	secure    bool
	dummyHash string
	limits    limiter
	workers   chan struct{}
}

func NewAPI(store *Store, secureCookies bool) (*API, error) {
	// Unknown accounts still incur password verification to reduce timing disclosure.
	dummy, err := hashPassword("dummy-password-for-timing-only")
	if err != nil {
		return nil, err
	}
	return &API{store: store, secure: secureCookies, dummyHash: dummy,
		limits: limiter{clients: make(map[string]attempts)}, workers: make(chan struct{}, 2)}, nil
}

func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.Handle("POST /auth/signup", a.throttle(http.HandlerFunc(a.signup)))
	mux.Handle("POST /auth/login", a.throttle(http.HandlerFunc(a.login)))
	mux.HandleFunc("POST /auth/logout", a.logout)
	mux.Handle("GET /auth/me", a.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := CurrentUser(r.Context())
		respond(w, http.StatusOK, map[string]any{"user": u})
	})))
}

func (a *API) throttle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.limits.allow(r) {
			tooMany(w)
			return
		}
		select {
		case a.workers <- struct{}{}:
			defer func() { <-a.workers }()
			next.ServeHTTP(w, r)
		default:
			tooMany(w)
		}
	})
}

func tooMany(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	fail(w, http.StatusTooManyRequests, "too many authentication attempts; try again later")
}

func (a *API) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		u, err := a.store.sessionUser(r.Context(), a.token(r))
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "unable to check session")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, u)))
	})
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func readCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var input credentials
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, 415, "Content-Type must be application/json")
		return input, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	err = d.Decode(&input)
	if err == nil {
		if trailing := d.Decode(new(any)); trailing != io.EOF {
			err = trailing
			if err == nil {
				err = errors.New("trailing JSON")
			}
		}
	}
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			fail(w, 413, "credentials exceed 8 KiB limit")
		} else {
			fail(w, 400, "body must be a single JSON object with email and password")
		}
		return input, false
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email || len(input.Email) > 254 || !strings.Contains(input.Email, "@") {
		fail(w, 400, "a valid email address is required")
		return input, false
	}
	if len(input.Password) == 0 || len(input.Password) > 1024 {
		fail(w, 400, "password must contain 1 to 1024 bytes")
		return input, false
	}
	return input, true
}

func (a *API) signup(w http.ResponseWriter, r *http.Request) {
	input, ok := readCredentials(w, r)
	if !ok {
		return
	}
	if utf8.RuneCountInString(input.Password) < 12 {
		fail(w, 400, "password must contain at least 12 characters")
		return
	}
	hash, err := hashPassword(input.Password)
	if err != nil {
		fail(w, 500, "unable to create account")
		return
	}
	u, err := a.store.createUser(r.Context(), input.Email, hash)
	if errors.Is(err, errEmailExists) {
		fail(w, 409, "email already registered")
		return
	}
	if err != nil {
		fail(w, 500, "unable to create account")
		return
	}
	respond(w, http.StatusCreated, map[string]any{"user": u})
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	input, ok := readCredentials(w, r)
	if !ok {
		return
	}
	u, hash, err := a.store.credentials(r.Context(), input.Email)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		fail(w, 500, "unable to sign in")
		return
	}
	if missing {
		hash = a.dummyHash
	}
	valid := verifyPassword(input.Password, hash)
	if !valid || missing {
		fail(w, 401, "invalid email or password")
		return
	}
	token, expires, err := a.store.newSession(r.Context(), u.ID, a.token(r))
	if err != nil {
		fail(w, 500, "unable to sign in")
		return
	}
	a.cookie(w, token, expires, int(sessionLifetime.Seconds()))
	respond(w, 200, map[string]any{"user": u})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.store.revoke(r.Context(), a.token(r)); err != nil {
		fail(w, 500, "unable to sign out")
		return
	}
	a.cookie(w, "", time.Unix(1, 0), -1)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) cookieName() string {
	if a.secure {
		return "__Host-gorequest_session"
	}
	return "gorequest_session"
}

func (a *API) token(r *http.Request) string {
	c, err := r.Cookie(a.cookieName())
	if err != nil {
		return ""
	}
	return c.Value
}

func (a *API) cookie(w http.ResponseWriter, token string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: a.cookieName(), Value: token, Path: "/", HttpOnly: true,
		Secure: a.secure, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge})
}

func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// BrowserProtection applies CSRF checks and optionally permits one configured frontend origin.
func BrowserProtection(next http.Handler, frontendOrigin string) (http.Handler, error) {
	protection := http.NewCrossOriginProtection()
	if frontendOrigin != "" {
		if err := protection.AddTrustedOrigin(frontendOrigin); err != nil {
			return nil, err
		}
	}
	protection.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fail(w, 403, "cross-origin request denied") }))
	protected := protection.Handler(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if frontendOrigin != "" {
			w.Header().Add("Vary", "Origin")
			if r.Header.Get("Origin") == frontendOrigin {
				w.Header().Set("Access-Control-Allow-Origin", frontendOrigin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
		}
		protected.ServeHTTP(w, r)
	}), nil
}
