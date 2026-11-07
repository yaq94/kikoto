package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCatalogRefreshBusySourceDoesNotQueueOrBlockOtherSources(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Path == "/Example/Slow" {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"name": "track.mp3", "type": 3, "size": 100}}}})
	}))
	defer func() { unblock(); upstream.Close() }()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"name": "track.mp3", "type": 3, "size": 100}}}})
	}))
	defer fast.Close()
	f := newRoleBoundaryFixture(t)
	metadataReviewExec(t, f.server.db, `INSERT INTO file_source(id,code,display_name,source_type) VALUES(71,'example-slow','Example Slow','openlist'),(72,'example-fast','Example Fast','openlist');
	 INSERT INTO source_catalog_entry(id,file_source_id,root_path,title) VALUES(1,71,'/Example/Slow','Example Slow'),(2,72,'/Example/Fast','Example Fast')`)
	for id, endpoint := range map[int]string{71: upstream.URL, 72: fast.URL} {
		metadataReviewExec(t, f.server.db, `INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(?,?,?)`, id, endpoint, endpoint)
	}
	metadataReviewExec(t, f.server.db, `UPDATE file_source SET config_json='{"openListRoots":["/Example/Slow"]}' WHERE id=71`)
	call := func(ctx context.Context, target string) <-chan int {
		done := make(chan int, 1)
		go func() {
			req := httptest.NewRequest(http.MethodPost, target, nil).WithContext(ctx)
			req.AddCookie(f.cookies["admin"])
			res := httptest.NewRecorder()
			f.handler.ServeHTTP(res, req)
			done <- res.Code
		}()
		return done
	}
	first := call(context.Background(), "/api/source-catalog/1/refresh")
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first request did not reach provider")
	}
	assertPrompt := func(target string, want int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done := call(ctx, target)
		select {
		case code := <-done:
			if code != want {
				t.Errorf("%s status=%d want=%d", target, code, want)
			}
		case <-time.After(2 * time.Second):
			unblock()
			<-done
			t.Errorf("%s waited behind another source request", target)
		}
	}
	assertPrompt("/api/source-catalog/2/refresh", http.StatusOK)
	assertPrompt("/api/source-catalog/1/refresh", http.StatusConflict)
	assertPrompt("/api/remote-sources/71/catalog/scan", http.StatusConflict)
	unblock()
	if code := <-first; code != http.StatusOK {
		t.Fatalf("first refresh=%d", code)
	}
	assertPrompt("/api/source-catalog/1/refresh", http.StatusOK)
}
