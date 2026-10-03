package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

// MaxBodyBytes bounds JSON request bodies. API requests are small; layouts,
// the largest, go through their own endpoint later with their own limit.
const MaxBodyBytes = 64 << 10

// WriteJSON writes v as a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ErrorBody is the shape of every API error.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries a stable machine code and a human message.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error writes an API error.
func Error(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}

// DecodeJSON reads a single JSON object into dst. On failure it writes the
// error response and returns false. Requiring application/json also means a
// cross-site form post can't reach the handler without a CORS preflight.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		Error(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "request body must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			Error(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
		} else {
			Error(w, http.StatusBadRequest, "bad_json", "malformed JSON request body")
		}
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		Error(w, http.StatusBadRequest, "bad_json", "request body must be a single JSON object")
		return false
	}
	return true
}
