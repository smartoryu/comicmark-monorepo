// Package httpx holds the HTTP plumbing shared by the server and its feature
// modules: the error envelope, request decoding, and the middleware chain.
//
// Feature modules are ordinary packages, so the helpers they need in order to
// answer a request cannot live in package main. Keeping them here also means the
// error shape and the middleware order are defined in exactly one place.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// Machine-readable error codes. Clients branch on these rather than on the
// human-readable message, so the wording can change freely.
const (
	CodeBadRequest     = "bad_request"
	CodeValidation     = "validation_failed"
	CodeUnauthorized   = "unauthorized"
	CodeForbidden      = "forbidden"
	CodeNotFound       = "not_found"
	CodeConflict       = "conflict"
	CodeRateLimited    = "rate_limited"
	CodeInternal       = "internal"
	CodeNotImplemented = "not_implemented"
)

// ErrorEnvelope is the single shape every error response uses, so clients need
// exactly one error parser.
type ErrorEnvelope struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail describes one failure.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Details carries field-level validation failures, or a machine-readable
	// reason such as "chapter_not_numeric".
	Details map[string]any `json:"details,omitempty"`
	// RequestID lets a user quote something that also appears in the server log.
	RequestID string `json:"request_id,omitempty"`
}

// Language is the canonical wording for failures that are not field-specific.
// Centralising it keeps the API's voice consistent across modules.
const (
	MsgInvalidPayload = "The request body could not be read."
	MsgValidation     = "Please check the highlighted fields."
	MsgUnauthorized   = "Sign in to continue."
	MsgForbidden      = "You do not have access to this."
	MsgNotFound       = "We could not find what you were looking for."
	MsgConflict       = "That conflicts with something that already exists."
	MsgRateLimited    = "Too many requests. Please slow down and try again shortly."
	MsgInternal       = "Something went wrong on our side."
)

// WriteJSON writes v as JSON with the given status.
//
// The body is marshalled before any header is written, so a marshal failure
// still produces a clean 500 rather than a truncated response.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to marshal response body",
			"error", err, "path", r.URL.Path, "status", status)
		WriteError(w, r, http.StatusInternalServerError, CodeInternal, MsgInternal)
		return
	}
	writeBody(w, r, status, body)
}

// WriteError writes an error envelope. Extra arguments provide the details map.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string, details ...map[string]any) {
	envelope := ErrorEnvelope{Error: ErrorDetail{
		Code:      code,
		Message:   message,
		RequestID: RequestIDFromContext(r.Context()),
	}}
	if len(details) > 0 {
		envelope.Error.Details = details[0]
	}

	body, err := json.Marshal(envelope)
	if err != nil {
		// Last resort: a body that cannot fail to marshal.
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"Something went wrong on our side."}}`))
		return
	}
	writeBody(w, r, status, body)
}

func writeBody(w http.ResponseWriter, r *http.Request, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		slog.DebugContext(r.Context(), "failed to write response body", "error", err)
	}
}

// WriteFieldErrors is the standard response for a request that failed
// validation: one message per offending field.
func WriteFieldErrors(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	details := make(map[string]any, len(fields))
	for field, problem := range fields {
		details[field] = problem
	}
	WriteError(w, r, http.StatusBadRequest, CodeValidation, MsgValidation, details)
}

// WriteFieldError reports a single invalid field.
func WriteFieldError(w http.ResponseWriter, r *http.Request, field, message string) {
	WriteFieldErrors(w, r, map[string]string{field: message})
}

// NoContent writes a 204.
func NoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// MaxBodyBytes bounds request bodies. ComicMark payloads are tiny, so a generous
// limit still rejects abuse.
const MaxBodyBytes = 64 << 10 // 64 KiB

// DecodeJSON reads a JSON request body into dst, rejecting unknown fields so a
// typo in a client payload fails loudly instead of being silently ignored.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return fmt.Errorf("decode json: body exceeds %d bytes", tooLarge.Limit)
		}
		return fmt.Errorf("decode json: %w", err)
	}

	// A second value means the client sent something other than a single object.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("decode json: body must contain a single JSON object")
	}
	return nil
}

// DecodeJSONOrFail reads the body and writes a 400 response itself when it is
// unusable, reporting whether the caller should continue.
//
// Handlers almost always want this shape, and doing it here keeps the error
// wording identical everywhere.
func DecodeJSONOrFail(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := DecodeJSON(w, r, dst); err != nil {
		slog.DebugContext(r.Context(), "rejected request body",
			"error", err, "path", r.URL.Path, "method", r.Method)
		WriteError(w, r, http.StatusBadRequest, CodeBadRequest, MsgInvalidPayload)
		return false
	}
	return true
}
