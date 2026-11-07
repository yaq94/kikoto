package httpapi

import (
	"context"
	"errors"
)

const catalogRefreshConcurrency = 3

var errCatalogRefreshBusy = errors.New("catalog refresh is already running or at capacity")

// Explicit refreshes do not create an in-memory waiting queue. Each source has
// one active request and different sources share a small global request budget.
func (s *Server) acquireCatalogRefresh(ctx context.Context, sourceID int64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.catalogRefreshMu.Lock()
	defer s.catalogRefreshMu.Unlock()
	if _, busy := s.catalogRefreshSources[sourceID]; busy || len(s.catalogRefreshSources) >= catalogRefreshConcurrency {
		return nil, errCatalogRefreshBusy
	}
	if s.catalogRefreshSources == nil {
		s.catalogRefreshSources = make(map[int64]struct{})
	}
	s.catalogRefreshSources[sourceID] = struct{}{}
	return func() {
		s.catalogRefreshMu.Lock()
		delete(s.catalogRefreshSources, sourceID)
		s.catalogRefreshMu.Unlock()
	}, nil
}
