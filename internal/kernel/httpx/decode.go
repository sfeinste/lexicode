package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
)

// DefaultMaxBody bounds request bodies read by DecodeJSON. The largest legitimate JSON body in
// the API is a wiki page; a megabyte is an order of magnitude of headroom over that.
const DefaultMaxBody = 1 << 20

// Validator is implemented by request bodies that can check their own fields. DecodeJSON calls
// it after a successful decode; a non-empty return becomes a 400 validation_failed problem with
// one entry per field.
type Validator interface {
	Validate() []FieldError
}

// DecodeJSON reads r's JSON body into a T, answering the error responses itself: 400
// invalid_request for a body that is not JSON (or is too large), 400 validation_failed when T
// implements Validator and reports field errors. The boolean says whether to continue — on
// false the response has already been written.
func DecodeJSON[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	r.Body = http.MaxBytesReader(w, r.Body, DefaultMaxBody)
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			WriteProblem(w, http.StatusRequestEntityTooLarge, TypeInvalidRequest,
				"Request body too large", "The request body exceeds the size limit.")
			return v, false
		}
		WriteProblem(w, http.StatusBadRequest, TypeInvalidRequest,
			"Invalid request body", "The request body must be a JSON object.")
		return v, false
	}
	// &v first so a pointer-receiver Validate on a struct T is found; any(v) covers a T that
	// is itself a pointer (or has a value receiver).
	val, ok := any(&v).(Validator)
	if !ok {
		val, ok = any(v).(Validator)
	}
	if ok {
		if errs := val.Validate(); len(errs) > 0 {
			WriteValidation(w, errs)
			return v, false
		}
	}
	return v, true
}
