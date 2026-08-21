// Package httpx is the kernel's HTTP surface (story S06): a thin mux over net/http 1.22
// routing patterns with the middleware chain every request passes through (request id,
// logging, panic recovery, per-namespace wrapping, CORS off by default), the one canonical
// application/problem+json writer, typed JSON decode helpers, and the SSE hub.
//
// Nothing here knows about auth, modules or services — the kernel and cmd/lexicode wire those
// in. That keeps the import direction clean: auth imports httpx for the problem writer, never
// the other way around.
package httpx

import (
	"encoding/json"
	"net/http"
)

// Stable problem type slugs shared by more than one package. Architecture §14: the frontend
// switches on these, so they are part of the API contract — never rename one. Slugs used by a
// single handler (auth's "invite_expired", say) stay next to that handler.
const (
	// TypeNotFound is a missing endpoint or a missing resource.
	TypeNotFound = "not_found"
	// TypeInvalidRequest is a malformed body or query the server refused to interpret.
	TypeInvalidRequest = "invalid_request"
	// TypeValidationFailed is a well-formed body whose fields fail validation; the problem
	// carries an errors array naming each field.
	TypeValidationFailed = "validation_failed"
	// TypeInternal is an unexpected server error. The real cause goes to the log, not the wire.
	TypeInternal = "internal"
)

// Problem is RFC 9457 application/problem+json. Type is a stable slug rather than a URL because
// the frontend switches on it (architecture §14). Errors is present only on validation problems.
type Problem struct {
	Type   string       `json:"type"`
	Title  string       `json:"title"`
	Status int          `json:"status"`
	Detail string       `json:"detail,omitempty"`
	Errors []FieldError `json:"errors,omitempty"`
}

// FieldError names one invalid field in a request body, for the frontend to render next to the
// input that caused it.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// WriteProblem writes a problem+json response. This is the one canonical writer: every error
// response in the process goes through it (or WriteValidation), so the shape can never drift
// between packages.
func WriteProblem(w http.ResponseWriter, status int, slug, title, detail string) {
	writeProblemBody(w, Problem{Type: slug, Title: title, Status: status, Detail: detail})
}

// WriteValidation writes a 400 validation_failed problem carrying one entry per invalid field.
func WriteValidation(w http.ResponseWriter, errs []FieldError) {
	writeProblemBody(w, Problem{
		Type:   TypeValidationFailed,
		Title:  "Validation failed",
		Status: http.StatusBadRequest,
		Detail: "One or more fields are invalid.",
		Errors: errs,
	})
}

func writeProblemBody(w http.ResponseWriter, p Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// WriteJSON writes a JSON success response. API responses are dynamic, so no-store: the SPA's
// cache is the query cache, not the HTTP one.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
