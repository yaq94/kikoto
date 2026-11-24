package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func catalogAvailabilityFixture(t *testing.T, upstream string, registered bool, status string, checked any) roleBoundaryFixture {
	t.Helper()
	f := newRoleBoundaryFixture(t)
	metadataReviewExec(t, f.server.db, `INSERT INTO file_source(id,code,display_name,source_type,enabled)
		VALUES(1,'example_remote_a','Example Remote A','openlist',1)`)
	var workID any
	if registered {
		metadataReviewExec(t, f.server.db, `INSERT INTO work(id,primary_code,title) VALUES(1,'','Example Work')`)
		workID = 1
	}
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,primary_code,title,work_id,availability_status,status_checked_at)
		VALUES(1,1,'/Example/One','','Example Work',?,?,?)`, workID, status, checked)
	metadataReviewExec(t, f.server.db, `INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes)
		VALUES(1,'saved.wav','Example Track','audio',16)`)
	metadataReviewExec(t, f.server.db, `INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(1,?,?)`, upstream, upstream)
	return f
}

func startCatalogAvailabilityWorker(t *testing.T, server *Server) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.RunCatalogAvailabilityWorker(ctx)
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("catalog availability worker did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

func waitCatalogAvailability(t *testing.T, fixture roleBoundaryFixture, id string, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		entry := reviewRequest(t, fixture.handler, fixture.cookies["user"], http.MethodGet, "/api/source-catalog/"+id, "", http.StatusOK)
		if ready(entry) {
			return entry
		}
		if time.Now().After(deadline) {
			t.Fatalf("catalog availability did not settle: %#v", entry)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSourceCatalogOpenChecksUnknownAndStaleAvailability(t *testing.T) {
	for _, registered := range []bool{false, true} {
		for _, stale := range []bool{false, true} {
			name := "unregistered/unknown"
			if registered {
				name = "registered/unknown"
			}
			status := "available"
			var checked any
			checkedAt := ""
			if stale {
				status, checked = "unavailable", "2000-01-01 00:00:00"
				checkedAt = "2000-01-01T00:00:00Z"
				name = "unregistered/stale"
				if registered {
					name = "registered/stale"
				}
			}
			t.Run(name, func(t *testing.T) {
				var calls atomic.Int32
				started, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var input struct {
						Path string `json:"path"`
					}
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
						return
					}
					if r.URL.Path != "/api/fs/list" || input.Path != "/Example/One" {
						http.Error(w, "unexpected scope", http.StatusInternalServerError)
						return
					}
					if calls.Add(1) == 1 {
						close(started)
					}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"name": "checked.wav", "size": 16, "type": 3}}}})
				}))
				t.Cleanup(upstream.Close)
				fixture := catalogAvailabilityFixture(t, upstream.URL, registered, status, checked)
				startCatalogAvailabilityWorker(t, fixture.server)
				t.Cleanup(unblock)
				entry := reviewRequest(t, fixture.handler, fixture.cookies["contributor"], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
				if entry["primaryCode"] != "" || entry["status"] != status {
					t.Fatalf("open did not retain the numberless card: %#v", entry)
				}
				tracks := reviewRequest(t, fixture.handler, fixture.cookies["user"], http.MethodGet, "/api/source-catalog/1/tracks?depth=1", "", http.StatusOK)["tracks"].([]any)
				if len(tracks) != 1 || tracks[0].(map[string]any)["path"] != "saved.wav" {
					t.Fatalf("pending check discarded the saved directory: %#v", tracks)
				}
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("opening an unknown or stale catalog entry did not check its source")
				}
				if entry["checkedAt"] != checkedAt {
					t.Fatalf("open reports a check that did not happen: %#v", entry)
				}
				unblock()
				entry = waitCatalogAvailability(t, fixture, "1", func(entry map[string]any) bool {
					return entry["status"] == "available" && entry["checkedAt"] != nil && entry["checkedAt"] != "" && entry["checkedAt"] != checkedAt
				})
				tracks = reviewRequest(t, fixture.handler, fixture.cookies["user"], http.MethodGet, "/api/source-catalog/1/tracks?depth=1", "", http.StatusOK)["tracks"].([]any)
				if len(tracks) != 1 || tracks[0].(map[string]any)["path"] != "checked.wav" || calls.Load() != 1 {
					t.Fatalf("check did not publish one complete directory: entry=%#v tracks=%#v calls=%d", entry, tracks, calls.Load())
				}
				if registered != (entry["workId"] != nil) {
					t.Fatalf("availability check changed registration: %#v", entry)
				}
			})
		}
	}
}

func TestCatalogAvailabilityCoalescesQueuedAndActiveReads(t *testing.T) {
	var firstCalls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if r.URL.Path != "/api/fs/list" {
			http.Error(w, "unexpected endpoint", http.StatusInternalServerError)
			return
		}
		if input.Path == "/Example/One" {
			if firstCalls.Add(1) == 1 {
				close(started)
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		} else if input.Path != "/Example/Two" {
			http.Error(w, "unexpected scope", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"name": "checked.wav", "size": 16, "type": 3}}}})
	}))
	t.Cleanup(upstream.Close)
	fixture := catalogAvailabilityFixture(t, upstream.URL, true, "unavailable", "2000-01-01 00:00:00")
	metadataReviewExec(t, fixture.server.db, `INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(2,1,'/Example/Two','Example Work 2');
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(2,'saved.wav','Example Track 2','audio',16)`)
	// Both queued reads select the same entry before any worker can claim it.
	for range 2 {
		entry := reviewRequest(t, fixture.handler, fixture.cookies["contributor"], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
		if entry["checkPending"] != true {
			t.Fatalf("queued check is not observable: %#v", entry)
		}
	}
	startCatalogAvailabilityWorker(t, fixture.server)
	t.Cleanup(unblock)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("catalog check did not reach its source")
	}
	// Standalone and saved-work readers share the active check, not another scan.
	reviewRequest(t, fixture.handler, fixture.cookies["contributor"], http.MethodGet, "/api/works/1/catalog-directories", "", http.StatusOK)
	reviewRequest(t, fixture.handler, fixture.cookies["contributor"], http.MethodGet, "/api/source-catalog/1/tracks?depth=1", "", http.StatusOK)
	reviewRequest(t, fixture.handler, fixture.cookies["contributor"], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
	reviewRequest(t, fixture.handler, fixture.cookies["contributor"], http.MethodGet, "/api/source-catalog/2", "", http.StatusOK)
	unblock()
	// The second distinct entry is submitted after the overlapping reads. Its
	// published result proves those earlier reads have had a chance to execute.
	waitCatalogAvailability(t, fixture, "2", func(entry map[string]any) bool {
		return entry["status"] == "available" && entry["checkedAt"] != "" && entry["checkPending"] == false
	})
	if firstCalls.Load() != 1 {
		t.Fatalf("overlapping reads checked the same directory %d times, want 1", firstCalls.Load())
	}
}

func TestSourceCatalogReadsRespectAvailabilityPermissionAndTTL(t *testing.T) {
	for _, tc := range []struct {
		name, role, status string
		fresh, disabled    bool
	}{
		{name: "read-only unknown", role: "user", status: "available"},
		{name: "read-only stale", role: "user", status: "unavailable"},
		{name: "fresh available", role: "contributor", status: "available", fresh: true},
		{name: "fresh unavailable", role: "contributor", status: "unavailable", fresh: true},
		{name: "disabled source", role: "contributor", status: "unavailable", disabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var firstCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Path string `json:"path"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					return
				}
				if r.URL.Path != "/api/fs/list" {
					http.Error(w, "unexpected endpoint", http.StatusInternalServerError)
					return
				}
				if input.Path == "/Example/One" {
					firstCalls.Add(1)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"name": "checked.wav", "size": 16, "type": 3}}}})
			}))
			t.Cleanup(upstream.Close)
			var checked any
			checkedAt := ""
			if tc.fresh {
				checkedAt = time.Now().UTC().Format(time.RFC3339)
				checked = checkedAt
			} else if tc.status == "unavailable" {
				checked, checkedAt = "2000-01-01 00:00:00", "2000-01-01T00:00:00Z"
			}
			fixture := catalogAvailabilityFixture(t, upstream.URL, false, tc.status, checked)
			status := tc.status
			if tc.disabled {
				metadataReviewExec(t, fixture.server.db, `UPDATE file_source SET enabled=0 WHERE id=1`)
				status = "disabled"
			}
			metadataReviewExec(t, fixture.server.db, `INSERT INTO file_source(id,code,display_name,source_type,enabled) VALUES(2,'example_remote_b','Example Remote B','openlist',1);
				INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(2,2,'/Example/Two','Example Work 2');
				INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(2,'saved.wav','Example Track 2','audio',16)`)
			metadataReviewExec(t, fixture.server.db, `INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(2,?,?)`, upstream.URL, upstream.URL)
			startCatalogAvailabilityWorker(t, fixture.server)
			entry := reviewRequest(t, fixture.handler, fixture.cookies[tc.role], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
			if entry["status"] != status || entry["checkedAt"] != checkedAt || entry["checkPending"] != false {
				t.Fatalf("read changed a recorded check or queued an unauthorized check: %#v", entry)
			}
			tracks := reviewRequest(t, fixture.handler, fixture.cookies[tc.role], http.MethodGet, "/api/source-catalog/1/tracks?depth=1", "", http.StatusOK)["tracks"].([]any)
			if len(tracks) != 1 || tracks[0].(map[string]any)["path"] != "saved.wav" {
				t.Fatalf("recorded directory is not readable: %#v", tracks)
			}
			reviewRequest(t, fixture.handler, fixture.cookies[tc.role], http.MethodPost, "/api/source-catalog/1/refresh", `{}`, http.StatusForbidden)
			// A distinct authorized check proves the worker can drain any work that
			// the earlier reads might have incorrectly scheduled.
			reviewRequest(t, fixture.handler, fixture.cookies["contributor"], http.MethodGet, "/api/source-catalog/2", "", http.StatusOK)
			waitCatalogAvailability(t, fixture, "2", func(entry map[string]any) bool {
				return entry["checkedAt"] != "" && entry["checkPending"] == false
			})
			entry = reviewRequest(t, fixture.handler, fixture.cookies["user"], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
			if firstCalls.Load() != 0 || entry["status"] != status || entry["checkedAt"] != checkedAt {
				t.Fatalf("read-only, fresh or disabled entry was checked: calls=%d entry=%#v", firstCalls.Load(), entry)
			}
		})
	}
}

func TestSourceCatalogTracksOpenChecksUnknownAvailability(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			http.Error(w, "unexpected endpoint", http.StatusInternalServerError)
			return
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"name": "checked.wav", "size": 16, "type": 3}}}})
	}))
	t.Cleanup(upstream.Close)
	fixture := catalogAvailabilityFixture(t, upstream.URL, false, "available", nil)
	startCatalogAvailabilityWorker(t, fixture.server)
	reviewRequest(t, fixture.handler, fixture.cookies["contributor"], http.MethodGet, "/api/source-catalog/1/tracks?depth=1", "", http.StatusOK)
	entry := waitCatalogAvailability(t, fixture, "1", func(entry map[string]any) bool {
		return entry["checkedAt"] != "" && entry["checkPending"] == false
	})
	if entry["status"] != "available" || entry["workId"] != nil || calls.Load() != 1 {
		t.Fatalf("track read did not check the unregistered entry exactly once: %#v calls=%d", entry, calls.Load())
	}
}

func TestCatalogAvailabilityFailureReleasesEntryForExplicitRetry(t *testing.T) {
	var calls atomic.Int32
	var healthy atomic.Bool
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			http.Error(w, "unexpected endpoint", http.StatusInternalServerError)
			return
		}
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if !healthy.Load() {
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"name": "checked.wav", "size": 16, "type": 3}}}})
	}))
	t.Cleanup(upstream.Close)
	fixture := catalogAvailabilityFixture(t, upstream.URL, true, "unavailable", "2000-01-01 00:00:00")
	startCatalogAvailabilityWorker(t, fixture.server)
	t.Cleanup(unblock)
	reviewRequest(t, fixture.handler, fixture.cookies["user"], http.MethodPatch, "/api/source-catalog/1/user-state", `{"rating":4,"note":"Example retained note"}`, http.StatusOK)
	reviewRequest(t, fixture.handler, fixture.cookies["contributor"], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("catalog check did not reach its source")
	}
	reviewRequest(t, fixture.handler, fixture.cookies["contributor"], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
	unblock()
	entry := waitCatalogAvailability(t, fixture, "1", func(entry map[string]any) bool {
		return entry["checkedAt"] != "2000-01-01T00:00:00Z" && entry["checkPending"] == false
	})
	if entry["status"] != "unavailable" || entry["personalRating"] != float64(4) || entry["personalNote"] != "Example retained note" || calls.Load() != 1 {
		t.Fatalf("failed check changed personal state or was repeated: %#v calls=%d", entry, calls.Load())
	}
	tracks := reviewRequest(t, fixture.handler, fixture.cookies["user"], http.MethodGet, "/api/source-catalog/1/tracks?depth=1", "", http.StatusOK)["tracks"].([]any)
	if len(tracks) != 1 || tracks[0].(map[string]any)["path"] != "saved.wav" {
		t.Fatalf("failed check discarded the saved directory: %#v", tracks)
	}
	healthy.Store(true)
	reviewRequest(t, fixture.handler, fixture.cookies["admin"], http.MethodPost, "/api/source-catalog/1/refresh", `{}`, http.StatusOK)
	entry = reviewRequest(t, fixture.handler, fixture.cookies["user"], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
	if entry["status"] != "available" || entry["checkPending"] != false || entry["personalNote"] != "Example retained note" || calls.Load() != 2 {
		t.Fatalf("explicit retry could not restore the failed entry: %#v calls=%d", entry, calls.Load())
	}
}

func TestCatalogAvailabilityCancellationKeepsStateAndAllowsRetry(t *testing.T) {
	var calls atomic.Int32
	started, stopped := make(chan struct{}), make(chan struct{})
	finish := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			http.Error(w, "unexpected endpoint", http.StatusInternalServerError)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-r.Context().Done():
				close(stopped)
			case <-finish:
			}
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"name": "checked.wav", "size": 16, "type": 3}}}})
	}))
	t.Cleanup(upstream.Close)
	fixture := catalogAvailabilityFixture(t, upstream.URL, false, "unavailable", "2000-01-01 00:00:00")
	stopWorker := startCatalogAvailabilityWorker(t, fixture.server)
	t.Cleanup(func() { close(finish) })
	readCtx, cancelRead := context.WithCancel(context.Background())
	defer cancelRead()
	request := httptest.NewRequest(http.MethodGet, "/api/source-catalog/1", nil).WithContext(readCtx)
	request.AddCookie(fixture.cookies["contributor"])
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("catalog read returned %d: %s", response.Code, response.Body)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("catalog check did not start")
	}
	cancelRead()
	entry := reviewRequest(t, fixture.handler, fixture.cookies["user"], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
	if entry["checkPending"] != true {
		t.Fatalf("reader cancellation cancelled the shared check: %#v", entry)
	}
	stopWorker()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("worker cancellation did not reach upstream")
	}
	entry = reviewRequest(t, fixture.handler, fixture.cookies["user"], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
	if entry["checkPending"] != false || entry["status"] != "unavailable" || entry["checkedAt"] != "2000-01-01T00:00:00Z" {
		t.Fatalf("cancelled check changed its observation or retained its marker: %#v", entry)
	}
	startCatalogAvailabilityWorker(t, fixture.server)
	reviewRequest(t, fixture.handler, fixture.cookies["contributor"], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
	entry = waitCatalogAvailability(t, fixture, "1", func(entry map[string]any) bool {
		return entry["checkedAt"] != "2000-01-01T00:00:00Z" && entry["checkPending"] == false
	})
	if entry["status"] != "available" || calls.Load() != 2 {
		t.Fatalf("cancelled check cannot be retried: %#v calls=%d", entry, calls.Load())
	}
}

func TestSourceCatalogSuccessfulTrackRangeDoesNotRenewAvailability(t *testing.T) {
	var gets atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/fs/get" {
			gets.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"name": "saved.wav", "raw_url": upstream.URL + "/audio"}})
			return
		}
		http.ServeContent(w, r, "saved.wav", time.Unix(0, 0), bytes.NewReader([]byte("0123456789abcdef")))
	}))
	t.Cleanup(upstream.Close)
	fixture := catalogAvailabilityFixture(t, upstream.URL, false, "unavailable", "2000-01-01 00:00:00")
	request := httptest.NewRequest(http.MethodGet, "/api/source-catalog/1/media?path=saved.wav", nil)
	request.AddCookie(fixture.cookies["contributor"])
	request.Header.Set("Range", "bytes=0-3")
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "0123" {
		t.Fatalf("track range returned %d: %q", response.Code, response.Body.String())
	}
	entry := reviewRequest(t, fixture.handler, fixture.cookies["user"], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
	if entry["status"] != "unavailable" || entry["checkedAt"] != "2000-01-01T00:00:00Z" || gets.Load() != 1 {
		t.Fatalf("one track renewed the directory verdict: %#v gets=%d", entry, gets.Load())
	}
}

func TestSourceCatalogRefreshDeadlinePreservesSavedDirectory(t *testing.T) {
	started := make(chan struct{}, 1)
	finish := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			http.Error(w, "unexpected endpoint", http.StatusInternalServerError)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-finish:
		}
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(func() { close(finish) })
	fixture := catalogAvailabilityFixture(t, upstream.URL, false, "unavailable", "2000-01-01 00:00:00")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/api/source-catalog/1/refresh", bytes.NewBufferString(`{}`)).WithContext(ctx)
	request.AddCookie(fixture.cookies["admin"])
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	select {
	case <-started:
	default:
		t.Fatal("deadline case did not reach upstream")
	}
	if response.Code != http.StatusBadGateway {
		t.Fatalf("deadline returned %d: %s", response.Code, response.Body)
	}
	entry := reviewRequest(t, fixture.handler, fixture.cookies["user"], http.MethodGet, "/api/source-catalog/1", "", http.StatusOK)
	tracks := reviewRequest(t, fixture.handler, fixture.cookies["user"], http.MethodGet, "/api/source-catalog/1/tracks?depth=1", "", http.StatusOK)["tracks"].([]any)
	if entry["status"] != "unavailable" || entry["checkedAt"] != "2000-01-01T00:00:00Z" || len(tracks) != 1 || tracks[0].(map[string]any)["path"] != "saved.wav" {
		t.Fatalf("deadline discarded recorded state: entry=%#v tracks=%#v", entry, tracks)
	}
}
