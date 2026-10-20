package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogDirectoryListingChecksUnknownAndStaleTabs(t *testing.T) {
	var calls atomic.Int32
	var pathsMu sync.Mutex
	paths := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		pathsMu.Lock()
		paths = append(paths, r.URL.Path+" "+input.Path)
		pathsMu.Unlock()
		if r.URL.Path != "/api/fs/list" || (input.Path != "/Example/One" && input.Path != "/Example/Two") {
			http.Error(w, "wrong scope", http.StatusInternalServerError)
			return
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"total": 1, "content": []map[string]any{{"name": "checked.wav", "size": 100, "type": 3}}}})
	}))
	defer upstream.Close()
	f := newRoleBoundaryFixture(t)
	if _, err := f.server.db.Exec(`INSERT INTO work(id,primary_code,title) VALUES(1,'RJ00000000','Example Work');
		INSERT INTO file_source(id,code,display_name,source_type) VALUES(1,'example_catalog','Example Catalog','openlist');
		INSERT INTO source_catalog_entry(id,file_source_id,root_path,primary_code,title,work_id,availability_status,status_checked_at) VALUES
		(1,1,'/Example/One','RJ00000000','Example One',1,'available',NULL),
		(2,1,'/Example/Two','RJ00000000','Example Two',1,'available',datetime('now','-2 days'));
		INSERT INTO source_catalog_file(catalog_entry_id,relative_path,title,kind,size_bytes) VALUES(2,'old.wav','Old track','audio',100);`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.db.Exec("INSERT INTO file_source_endpoint(file_source_id,api_url,base_url) VALUES(1,?,?)", upstream.URL, upstream.URL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.server.RunCatalogAvailabilityWorker(ctx)

	directories := reviewRequest(t, f.handler, f.cookies["user"], http.MethodGet, "/api/works/1/catalog-directories", "", http.StatusOK)["directories"].([]any)
	if len(directories) != 2 {
		t.Fatalf("directories = %#v", directories)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var first, second, checked string
		if err := f.server.db.QueryRow(`SELECT
			(SELECT availability_status FROM source_catalog_entry WHERE id=1),
			(SELECT availability_status FROM source_catalog_entry WHERE id=2),
			(SELECT status_checked_at FROM source_catalog_entry WHERE id=2)`).Scan(&first, &second, &checked); err != nil {
			t.Fatal(err)
		}
		if first == "available" && second == "available" && checked != "" && calls.Load() >= 2 {
			break
		}
		if time.Now().After(deadline) {
			pathsMu.Lock()
			t.Fatalf("background checks did not finish: first=%s second=%s checked=%s calls=%d paths=%v", first, second, checked, calls.Load(), paths)
			pathsMu.Unlock()
		}
		time.Sleep(25 * time.Millisecond)
	}
	var path string
	if err := f.server.db.QueryRow("SELECT relative_path FROM source_catalog_file WHERE catalog_entry_id=2").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != "checked.wav" {
		t.Fatalf("stale tab kept the old snapshot: %s", path)
	}
}

func TestCatalogDirectoryNeedsCheck(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		status    string
		checkedAt string
		want      bool
	}{
		{"unknown is always checked", "unknown", "", true},
		{"missing timestamp is checked", "available", "", true},
		{"recent available is not checked", "available", now.Add(-time.Hour).Format(time.RFC3339), false},
		{"expired available is checked", "available", now.Add(-25 * time.Hour).Format(time.RFC3339), true},
		{"failed source is checked after a day", "error", now.Add(-48 * time.Hour).Format(time.RFC3339), true},
		{"disabled source is never checked", "disabled", "", false},
	} {
		if got := catalogDirectoryNeedsCheck(tc.status, tc.checkedAt, now); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
