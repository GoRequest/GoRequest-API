package main

import (
	"errors"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/MarlonBBo/GoRequest/internal/auth"
	"github.com/MarlonBBo/GoRequest/internal/handler"
	"github.com/MarlonBBo/GoRequest/internal/httpclient"
	"github.com/MarlonBBo/GoRequest/internal/service"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal("Unable to load .env; check its syntax")
	}
	databasePath := os.Getenv("AUTH_DB_PATH")
	if databasePath == "" {
		databasePath = "gorequest.db"
	}
	store, err := auth.Open(databasePath)
	if err != nil {
		log.Fatalf("Open authentication database: %v", err)
	}
	defer store.Close()
	secureCookies := os.Getenv("AUTH_INSECURE_COOKIES") != "true"
	accounts := auth.NewAPI(store, secureCookies)
	if err := accounts.ConfigureGitHub(auth.GitHubConfig{
		ClientID: os.Getenv("GITHUB_CLIENT_ID"), ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
		RedirectURL: os.Getenv("GITHUB_REDIRECT_URL"), FrontendOrigin: os.Getenv("FRONTEND_ORIGIN"),
	}); err != nil {
		log.Fatalf("Configure GitHub OAuth: %v", err)
	}
	mux := http.NewServeMux()
	accounts.RegisterRoutes(mux)
	executor := service.NewExecutor(httpclient.New())

	mux.Handle("POST /requests/execute", accounts.OptionalUser(handler.Execute(executor)))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	protected, err := auth.BrowserProtection(mux, os.Getenv("FRONTEND_ORIGIN"))
	if err != nil {
		log.Fatalf("Invalid FRONTEND_ORIGIN: %v", err)
	}
	server := &http.Server{
		Addr:              ":8080",
		Handler:           protected,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Println("Server listening on http://localhost:8080")
	if !secureCookies {
		log.Println("Development mode: session cookies allow plain HTTP")
	}
	log.Fatal(server.ListenAndServe())
}
