package github

import (
	"context"
	"net/http"
	"sync"
)

// Conditional-request plumbing for the poller (architecture §7: "requests use If-None-Match; a
// 304 costs no rate limit"). The frozen ForgeProvider port has no etag parameter, and the
// poller lives in this package — so the etag rides the context instead: the poller puts an
// *etagState in the context of one forge list call, and the condTransport (sitting between the
// go-github client and the module's rate-limit transport) applies it to the FIRST request of
// that call only. Pagination requests after the first are untouched: the stored etag belongs
// to page one's URL, and a changed listing must be fetched in full anyway.
//
// On a 304 go-github surfaces a generic *ErrorResponse; the poller checks NotModified() before
// treating the error as real.

type etagKey struct{}

// etagState carries one conditional exchange: Send is the stored etag to offer, got is the
// etag of the response, notModified reports a 304.
type etagState struct {
	send string

	mu          sync.Mutex
	used        bool // the first request happened; later (pagination) requests pass through
	got         string
	notModified bool
}

// withEtag arms ctx with a conditional exchange offering etag (may be empty: the exchange then
// only records the response etag).
func withEtag(ctx context.Context, s *etagState) context.Context {
	return context.WithValue(ctx, etagKey{}, s)
}

// Result returns what the exchange learned: the response's etag (empty when none arrived) and
// whether the listing was unchanged.
func (s *etagState) Result() (etag string, notModified bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.got, s.notModified
}

// condTransport applies the context's etagState to the first request that passes through it.
// It wraps the module's rate-limit transport, so retries below it keep the header.
type condTransport struct{ base http.RoundTripper }

func (t *condTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s, _ := req.Context().Value(etagKey{}).(*etagState)
	if s == nil {
		return t.base.RoundTrip(req)
	}
	s.mu.Lock()
	first := !s.used
	s.used = true
	send := s.send
	s.mu.Unlock()
	if !first {
		return t.base.RoundTrip(req)
	}

	if send != "" {
		req = req.Clone(req.Context())
		req.Header.Set("If-None-Match", send)
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if et := resp.Header.Get("ETag"); et != "" {
		s.got = et
	}
	s.notModified = resp.StatusCode == http.StatusNotModified
	s.mu.Unlock()
	return resp, nil
}
