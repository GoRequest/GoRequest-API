package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/MarlonBBo/GoRequest/internal/service"
)

const MaxRequestBytes = 1 << 20

func Execute(executor *service.Executor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var input service.ExecuteRequest
		if err := decoder.Decode(&input); err != nil {
			decodeError(w, err)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			decodeError(w, err)
			return
		}
		response, err := executor.Execute(r.Context(), input)
		if err != nil {
			switch {
			case errors.Is(err, service.ErrInvalidRequest):
				writeError(w, http.StatusBadRequest, err.Error())
			case errors.Is(err, context.DeadlineExceeded):
				writeError(w, http.StatusGatewayTimeout, "HTTP request timed out")
			case errors.Is(err, service.ErrResponseTooLarge):
				writeError(w, http.StatusBadGateway, err.Error())
			default:
				writeError(w, http.StatusBadGateway, "HTTP request failed or destination is not allowed")
			}
			return
		}
		writeJSON(w, http.StatusOK, response)
	}
}

func decodeError(w http.ResponseWriter, err error) {
	var sizeErr *http.MaxBytesError
	if errors.As(err, &sizeErr) {
		writeError(w, http.StatusRequestEntityTooLarge, "request exceeds 1 MiB limit")
		return
	}
	writeError(w, http.StatusBadRequest, "body must be a single JSON object with valid request fields")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
