package httpapi

import (
	"context"
	"testing"
)

// A committed semantic change invalidates the operator snapshot immediately.
func TestTranslationOperatorSnapshotTracksCommittedCorpus(t *testing.T) {
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
	// Inserting a card changes the durable revision even without an API hook.
	seedPrefillCard(t, s, 2, "Second title")
	second, err := s.cachedTranslationOperatorSnapshot(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.coverage.Total != 2 {
		t.Fatalf("second total = %d, want 2 after committed corpus change", second.coverage.Total)
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

// Explicit invalidation remains available for status-only changes.
func TestTranslationOperatorSnapshotExplicitInvalidation(t *testing.T) {
	s, _, _, _ := translationHTTPFixture(t)
	seedPrefillCard(t, s, 1, "Example title")
	cfg := mustTranslationConfig(t, s)
	if _, err := s.cachedTranslationOperatorSnapshot(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	s.invalidateTranslationCoverageCache()
	seedPrefillCard(t, s, 2, "Second title")
	next, err := s.cachedTranslationOperatorSnapshot(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if next.coverage.Total != 2 {
		t.Fatalf("expired snapshot total = %d, want 2", next.coverage.Total)
	}
}
