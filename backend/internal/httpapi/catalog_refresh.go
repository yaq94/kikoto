package httpapi

import (
	"context"
	"errors"
)

const catalogRefreshConcurrency = 3
const catalogRefreshQueueLimit = 64

var errCatalogRefreshBusy = errors.New("catalog refresh queue is full")

// Each source has one active request. The shared waiting budget preserves
// serialized refreshes without retaining an unbounded number of HTTP requests.
func (s *Server) acquireCatalogRefresh(ctx context.Context, sourceID int64) (func(), error) {
	waiting := false
	for {
		s.catalogRefreshMu.Lock()
		if err := ctx.Err(); err != nil {
			if waiting {
				s.catalogRefreshWaiters--
			}
			s.catalogRefreshMu.Unlock()
			return nil, err
		}
		_, busy := s.catalogRefreshSources[sourceID]
		if !busy && len(s.catalogRefreshSources) < catalogRefreshConcurrency {
			if waiting {
				s.catalogRefreshWaiters--
			}
			if s.catalogRefreshSources == nil {
				s.catalogRefreshSources = make(map[int64]struct{})
			}
			s.catalogRefreshSources[sourceID] = struct{}{}
			s.catalogRefreshMu.Unlock()
			return func() {
				s.catalogRefreshMu.Lock()
				delete(s.catalogRefreshSources, sourceID)
				if s.catalogRefreshChanged != nil {
					close(s.catalogRefreshChanged)
					s.catalogRefreshChanged = nil
				}
				s.catalogRefreshMu.Unlock()
			}, nil
		}
		if !waiting {
			if s.catalogRefreshWaiters >= catalogRefreshQueueLimit {
				s.catalogRefreshMu.Unlock()
				return nil, errCatalogRefreshBusy
			}
			s.catalogRefreshWaiters++
			waiting = true
		}
		if s.catalogRefreshChanged == nil {
			s.catalogRefreshChanged = make(chan struct{})
		}
		changed := s.catalogRefreshChanged
		s.catalogRefreshMu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
	}
}
