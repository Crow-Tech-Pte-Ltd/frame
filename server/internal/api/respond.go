package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// Error is an API error rendered as {"error":{"code","message"}}.
type Error struct {
	Status     int
	Code       string
	Message    string
	RetryAfter int // seconds, for 429
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

func apiErr(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

func errBadRequest(msg string) *Error { return apiErr(http.StatusBadRequest, "bad_request", msg) }
func errNotFound(msg string) *Error   { return apiErr(http.StatusNotFound, "not_found", msg) }
func errForbidden(msg string) *Error  { return apiErr(http.StatusForbidden, "forbidden", msg) }
func errConflict(msg string) *Error   { return apiErr(http.StatusConflict, "conflict", msg) }

var (
	errUnauthorized       = apiErr(http.StatusUnauthorized, "unauthorized", "Missing or invalid session")
	errSessionDisabled    = apiErr(http.StatusUnauthorized, "account_disabled", "This account has been suspended by the server's admin") // bearer token of a suspended account (v1.2.1); login gets 403
	errInvalidCredentials = apiErr(http.StatusUnauthorized, "invalid_credentials", "Invalid credentials")
	errNotFriends         = apiErr(http.StatusForbidden, "not_friends", "You are not friends with this user")
	errBlocked            = apiErr(http.StatusForbidden, "blocked", "This user is blocked")
	errPayloadTooLarge    = apiErr(http.StatusRequestEntityTooLarge, "payload_too_large", "Request body too large")
	errUnsupportedMedia   = apiErr(http.StatusUnsupportedMediaType, "unsupported_media", "Unsupported media type")
	errInternal           = apiErr(http.StatusInternalServerError, "internal", "Internal server error")
	errUserNotFound       = errNotFound("User not found")
	errMomentNotFound     = errNotFound("Moment not found")
	errMessageNotFound    = errNotFound("Message not found")
)

func rateLimited(retryAfterSec int) *Error {
	return &Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "Too many requests, slow down", RetryAfter: max(1, retryAfterSec)}
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, e *Error) {
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(e.RetryAfter))
	}
	var b errorBody
	b.Error.Code, b.Error.Message = e.Code, e.Message
	writeJSON(w, e.Status, b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep "&" in invite URLs readable
	err := enc.Encode(v)
	b := buf.Bytes()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"Internal server error"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b) // Encode already appended a newline
}

func noContent(w http.ResponseWriter) error {
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// MaxJSONBody is the default request body limit.
const MaxJSONBody = 64 << 10

// decodeJSON reads exactly one JSON value (≤ 64 KiB) into dst.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, MaxJSONBody+1))
	if err := dec.Decode(dst); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return errPayloadTooLarge
		}
		if errors.Is(err, io.EOF) {
			return errBadRequest("Request body is empty")
		}
		if errors.Is(err, io.ErrUnexpectedEOF) && dec.InputOffset() >= MaxJSONBody {
			return errPayloadTooLarge
		}
		return errBadRequest("Invalid JSON body")
	}
	if dec.More() {
		return errBadRequest("Request body must be a single JSON object")
	}
	return nil
}
