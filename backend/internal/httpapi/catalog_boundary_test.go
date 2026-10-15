package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCatalogRefreshBoundsUntrustedMetadata(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		handle http.HandlerFunc
	}{
		{"oversized", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"code":200,"message":"` + strings.Repeat("x", 2<<20) + `","data":{"content":[],"total":0}}`))
		}},
		{"private redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.2/audio", http.StatusFound)
		}},
		{"traversal", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"code":200,"data":{"total":1,"content":[{"name":"../outside.mp3","type":3,"size":16}]}}`))
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			remote := httptest.NewServer(scenario.handle)
			defer remote.Close()
			_, handler, cookie := openListBoundaryApp(t, remote.URL)
			result := reviewRequest(t, handler, cookie, http.MethodPost, "/api/source-catalog/1/refresh", `{}`, 502)
			if strings.Contains(mustJSON(result), "127.0.0.2") {
				t.Fatal("upstream address leaked")
			}
			tracks := reviewRequest(t, handler, cookie, http.MethodGet, "/api/source-catalog/1/tracks", "", 200)["tracks"].([]any)
			if len(tracks) != 1 || tracks[0].(map[string]any)["path"] != "track.mp3" {
				t.Fatal("failed refresh discarded known directory")
			}
		})
	}
}

func TestCatalogRefreshCancellationReachesUpstream(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	finish := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-finish:
		}
		close(stopped)
	}))
	defer remote.Close()
	defer close(finish)
	_, handler, cookie := openListBoundaryApp(t, remote.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/api/source-catalog/1/refresh", strings.NewReader(`{}`)).WithContext(ctx)
	request.AddCookie(cookie)
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeHTTP(httptest.NewRecorder(), request) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not reach configured private origin")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled refresh did not return")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("upstream did not cancel")
	}
}
