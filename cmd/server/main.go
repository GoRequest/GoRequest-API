package main

import (
	"log"
	"net/http"
	"time"

	"github.com/MarlonBBo/GoRequest/internal/handler"
	"github.com/MarlonBBo/GoRequest/internal/httpclient"
	"github.com/MarlonBBo/GoRequest/internal/service"
)

func main() {
	mux := http.NewServeMux()
	executor := service.NewExecutor(httpclient.New())

	mux.HandleFunc("POST /requests/execute", handler.Execute(executor))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	server := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Println("Server listening on http://localhost:8080")
	log.Fatal(server.ListenAndServe())
}
