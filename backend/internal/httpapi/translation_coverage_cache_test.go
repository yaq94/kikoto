package httpapi

import (
	"context"
	"testing"
	"time"
)

// The operator view walks the whole corpus plus dbstat. A page that polls it
// must not start one such read per request, so consecutive reads inside the
// snapshot window reuse the first answer.
func TestTranslationOperatorSnapshotIsReusedWithinItsWindow(t *testing.T) {
	s, host, admin, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example title")
	cfg := mustTranslationConfig(t, s)
	first, err := s.cachedTranslationOperatorSnapshot(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.coverage.Total != 1 {
		t.Fatalf("coverage total = %d, want 1", first.coverage.Total)
	}
	// A second read inside the window returns the same snapshot without
	// recomputing it. Bumping the corpus behind the cache proves the reuse: an
	// uncached read would now see two cards.
	seedPrefillCard(t, s, 2, "Second title")
	second, err := s.cachedTranslationOperatorSnapshot(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.coverage.Total != first.coverage.Total {
		t.Fatalf("second total = %d, want the cached %d", second.coverage.Total, first.coverage.Total)
	}
	// A status transition drops the snapshot, so the next read reflects the
	// corpus as it is now.
	s.invalidateTranslationCoverageCache()
	third, err := s.cachedTranslationOperatorSnapshot(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if third.coverage.Total != 2 {
		t.Fatalf("third total = %d, want 2 after invalidation", third.coverage.Total)
	}
	// The operator endpoint still answers after the cache is warm.
	view := translationRequest(t, host, admin, "GET", "/api/settings/translation/prefill", "", 200)
	if view["total"] != float64(2) {
		t.Fatalf("prefill view total = %v, want 2", view["total"])
	}
}

// A stale snapshot must expire on its own even without a status change, so a
// long-running pass cannot leave the operator view frozen.
func TestTranslationOperatorSnapshotExpires(t *testing.T) {
	s, _, _, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example title")
	cfg := mustTranslationConfig(t, s)
	if _, err := s.cachedTranslationOperatorSnapshot(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	seedPrefillCard(t, s, 2, "Second title")
	s.translationCoverageMu.Lock()
	s.translationCoverage.at = time.Now().Add(-2 * translationCoverageCacheTTL)
	s.translationCoverageMu.Unlock()
	next, err := s.cachedTranslationOperatorSnapshot(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if next.coverage.Total != 2 {
		t.Fatalf("expired snapshot total = %d, want 2", next.coverage.Total)
	}
}
