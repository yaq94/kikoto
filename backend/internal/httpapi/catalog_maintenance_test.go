package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// A hold must stop the index at a directory boundary and defer the job to the
// hold end. Without the deferral the worker immediately re-queues, pauses
// again, and burns the writer for the whole maintenance window.
func TestCatalogMaintenanceHoldPausesAndDefersResume(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": {looseAudio("Alpha.mp3")},
	})
	hold := reviewRequest(t, f.handler, f.cookie, http.MethodPost, "/api/catalog/maintenance", `{"holdSeconds":120,"reason":"deploy"}`, 200)
	if hold["active"] != true {
		t.Fatalf("hold = %#v, want active", hold)
	}
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	progress := waitCatalogHold(t, f)
	if progress["status"] != "queued" || progress["pauseReason"] != "maintenance" {
		t.Fatalf("progress = %#v, want queued/maintenance", progress)
	}

	var available, status string
	runID := int64(progress["runId"].(float64))
	if err := f.server.db.QueryRow(`SELECT COALESCE(available_at,''),status FROM workflow_job WHERE workflow_run_id=?`, runID).Scan(&available, &status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("job status = %q, want queued", status)
	}
	resume, err := time.Parse("2006-01-02 15:04:05", available)
	if err != nil {
		t.Fatalf("available_at = %q, want a timestamp", available)
	}
	if time.Until(resume) < 60*time.Second {
		t.Fatalf("job resumes at %s, before the hold ends", available)
	}

	// Releasing the hold must let the next step finish the scan.
	reviewRequest(t, f.handler, f.cookie, http.MethodDelete, "/api/catalog/maintenance", "", 200)
	state := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/catalog/maintenance", "", 200)
	if state["active"] == true {
		t.Fatalf("hold survived DELETE: %#v", state)
	}
	if _, err := f.server.db.Exec(`UPDATE workflow_job SET available_at=NULL WHERE workflow_run_id=?`, runID); err != nil {
		t.Fatal(err)
	}
	if p := f.drain(t); p["status"] != "complete" {
		t.Fatalf("resumed scan = %#v, want complete", p)
	}
}

// Timing is diagnostic: every phase of a run must be attributable, and an
// unknown run must not fabricate rows.
func TestCatalogPhaseTimingsRecordEachPhase(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{
		"/":        {looseDir("Creator")},
		"/Creator": {looseAudio("Alpha.mp3")},
	})
	f.request(t, http.MethodPost, "/catalog/index", `{"intervalMs":100}`, 202)
	progress := waitCatalogHold(t, f)
	if progress["status"] != "complete" {
		t.Fatalf("scan = %#v", progress)
	}
	runID := int64(progress["runId"].(float64))
	body := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/catalog/phases?runId="+strconvFormat(runID), "", 200)
	phases, _ := body["phases"].([]any)
	seen := map[string]bool{}
	for _, raw := range phases {
		seen[raw.(map[string]any)["phase"].(string)] = true
	}
	for _, want := range []string{"scan", "publish", "cleanup", "lifecycle", "settle"} {
		if !seen[want] {
			t.Fatalf("phase %q missing from %#v", want, seen)
		}
	}
	empty := reviewRequest(t, f.handler, f.cookie, http.MethodGet, "/api/catalog/phases?runId=999999", "", 200)
	if got := len(empty["phases"].([]any)); got != 0 {
		t.Fatalf("unknown run returned %d phases", got)
	}
}

func strconvFormat(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// The hold is a setting, not scan state: an unreadable value must not block
// indexing forever.
func TestCatalogMaintenanceIgnoresAMalformedSetting(t *testing.T) {
	f := newLooseWorkFixture(t, map[string][]map[string]any{"/": {looseAudio("a.mp3")}})
	if _, err := f.server.db.Exec(`INSERT INTO app_setting(key,value_json) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json`, catalogMaintenanceSetting, "{not json"); err != nil {
		t.Fatal(err)
	}
	held, _, err := f.server.catalogMaintenanceActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatal("a malformed hold blocked the index")
	}
}

// A held job stays queued on purpose, so the generic drain helper would wait
// forever for a terminal status. Stop as soon as the hold is recorded.
func waitCatalogHold(t *testing.T, f *looseWorkFixture) map[string]any {
	t.Helper()
	for range 100 {
		if err := f.server.runNextQueuedWorkflowJob(context.Background()); err != nil {
			t.Fatal(err)
		}
		p := f.request(t, http.MethodGet, "/catalog/index", "", 200)
		if p["pauseReason"] == "maintenance" {
			return p
		}
		if p["status"] != "queued" && p["status"] != "running" {
			return p
		}
	}
	t.Fatal("scan did not reach the maintenance pause")
	return nil
}
