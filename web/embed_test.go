package webui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	webui "github.com/spruce/lexicode/web"
)

func get(t *testing.T, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	webui.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Result()
}

// TestSPAFallback is the story S01 acceptance check that a deep client-side route survives a page
// reload. It passes whether or not the frontend has been built, because both the real index.html
// and the not-built fallback are HTML served with status 200.
func TestSPAFallback(t *testing.T) {
	for _, path := range []string{"/", "/runs/01J000000000000000000000", "/projects/a/board", "/nope"} {
		t.Run(path, func(t *testing.T) {
			resp := get(t, path)
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d, want 200", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("content-type = %q, want text/html", ct)
			}
		})
	}
}

func TestPostIsRejected(t *testing.T) {
	rec := httptest.NewRecorder()
	webui.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestFallbackPageExplainsHowToBuild(t *testing.T) {
	if webui.Built() {
		t.Skip("this binary carries a real frontend build; the fallback page is unreachable")
	}
	resp := get(t, "/")
	defer func() { _ = resp.Body.Close() }()
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), "make build") {
		t.Error("the not-built fallback page should tell the reader how to build the frontend")
	}
}
