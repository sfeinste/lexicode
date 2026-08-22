package wiki_test

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// S33 acceptance: search finds a page by body text within 100ms on 500 pages.
//
// Methodology: 500 pages seeded through the store (each ~1KB of body, distinct per-page
// terms), then 50 timed calls straight into Service.Search — the service layer plus SQLite
// FTS5, excluding HTTP overhead, which is how the acceptance bound is meant (the search
// endpoint adds routing+JSON, microseconds at this payload size). Queries alternate a
// selective per-page term and a term matching every page (worst-case result assembly at the
// 25-hit cap). The assertion is on the MAXIMUM single query, not the average, and the run
// fails if any query returns nothing.
func TestSearchUnder100msOn500Pages(t *testing.T) {
	e := newEnv(t)
	c := e.owner()
	e.project(c, "PAY")
	p, err := e.st.Projects().ByKey(context.Background(), "PAY")
	if err != nil {
		t.Fatal(err)
	}
	seedPages(t, e.st, p.ID, 500)

	ctx := context.Background()
	// Warm once: the first query pays connection/statement setup, which every real search
	// after boot shares; it is measured too — the bound must hold cold.
	var worst time.Duration
	for i := 0; i < 50; i++ {
		q := fmt.Sprintf("capacitor of area %d", i*7) // phrase-ish, selective
		if i%2 == 1 {
			q = "conventions boring" // matches all 500 pages
		}
		start := time.Now()
		hits, err := e.svc.Search(ctx, "PAY", q)
		took := time.Since(start)
		if err != nil {
			t.Fatalf("search %q: %v", q, err)
		}
		if len(hits) == 0 {
			t.Fatalf("search %q returned nothing", q)
		}
		if took > worst {
			worst = took
		}
	}
	t.Logf("worst of 50 searches over 500 pages: %s", worst)
	if worst > 100*time.Millisecond {
		t.Fatalf("worst search took %s, want < 100ms", worst)
	}
}
