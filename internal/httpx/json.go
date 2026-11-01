package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"dash.cha.sh/internal/msg"
)

// MaxBodyBytes bounds JSON request bodies. API requests are small; layouts,
// the largest, use DecodeJSONLimit with their own bound.
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

// ErrorDetail carries a stable machine code and a human message. A message
// from the catalog (internal/msg) also carries its sentence ID and
// arguments, so it can be translated; Message stays the English text.
type ErrorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	ID      string         `json:"id,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
}

// Detail is the ErrorDetail for code with the catalog sentence m.
func Detail(code string, m msg.M) ErrorDetail {
	return ErrorDetail{Code: code, Message: m.Text(), ID: m.ID, Args: m.Args}
}

// Error writes an API error with a message that isn't in the catalog yet.
// New code uses Say.
func Error(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}

// Say writes an API error with the catalog sentence m: its English text,
// ID and arguments.
func Say(w http.ResponseWriter, status int, code string, m msg.M) {
	WriteJSON(w, status, ErrorBody{Error: Detail(code, m)})
}

// DecodeJSON reads a single JSON object into dst. On failure it writes the
// error response and returns false. Requiring application/json also means a
// cross-site form post can't reach the handler without a CORS preflight.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return DecodeJSONLimit(w, r, dst, MaxBodyBytes)
}

// DecodeJSONLimit is DecodeJSON with a body limit other than MaxBodyBytes.
func DecodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		Say(w, http.StatusUnsupportedMediaType, "unsupported_media_type", msg.New("request.not_json"))
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			Say(w, http.StatusRequestEntityTooLarge, "too_large", msg.New("request.too_large"))
		} else {
			Say(w, http.StatusBadRequest, "bad_json", msg.New("request.bad_json"))
		}
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		Say(w, http.StatusBadRequest, "bad_json", msg.New("request.not_single_object"))
		return false
	}
	return true
}
