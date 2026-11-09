package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type userContextKey struct{}

// CurrentUser returns the identity set by RequireUser.
func CurrentUser(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userContextKey{}).(User)
	return u, ok
}

type API struct {
	store   *Store
	secure  bool
	limits  limiter
	workers chan struct{}
	github  *githubOAuth
}

func NewAPI(store *Store, secureCookies bool) *API {
	return &API{store: store, secure: secureCookies,
		limits: limiter{clients: make(map[string]attempts)}, workers: make(chan struct{}, 2)}
}

func (a *API) RegisterRoutes(mux *http.ServeMux) {
	if a.github != nil {
		mux.Handle("GET /auth/github", a.throttle(http.HandlerFunc(a.githubStart)))
		mux.Handle("GET /auth/github/callback", a.throttle(http.HandlerFunc(a.githubCallback)))
	}
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
	return a.userMiddleware(next, false)
}

// OptionalUser makes signed-in identity available while allowing guests and expired sessions.
func (a *API) OptionalUser(next http.Handler) http.Handler {
	return a.userMiddleware(next, true)
}

func (a *API) userMiddleware(next http.Handler, allowGuest bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		u, err := a.store.sessionUser(r.Context(), a.token(r))
		if errors.Is(err, sql.ErrNoRows) {
			if allowGuest {
				next.ServeHTTP(w, r)
				return
			}
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
		Secure: a.secure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: maxAge})
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
