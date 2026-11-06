package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/MarlonBBo/GoRequest/internal/auth"
	"github.com/MarlonBBo/GoRequest/internal/handler"
	"github.com/MarlonBBo/GoRequest/internal/httpclient"
	"github.com/MarlonBBo/GoRequest/internal/service"
)

func main() {
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
	accounts, err := auth.NewAPI(store, secureCookies)
	if err != nil {
		log.Fatalf("Initialize authentication: %v", err)
	}
	mux := http.NewServeMux()
	accounts.RegisterRoutes(mux)
	executor := service.NewExecutor(httpclient.New())

	mux.Handle("POST /requests/execute", accounts.RequireUser(handler.Execute(executor)))
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
