package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yexca/kikoto/backend/internal/config"
	"github.com/yexca/kikoto/backend/internal/personal"
)

func TestDemoListeningSimulatesHistoryOnAdmittedLocalWorksOnly(t *testing.T) {
	db := openMigratedTestDB(t)
	s := NewServer(db, config.Config{Mode: config.ModeDemo})
	ctx := context.Background()
	if err := s.BootstrapDemo(ctx); err != nil {
		t.Fatal(err)
	}
	// Works 1 and 2 are admitted and local; 3 is adult, 4 is paid, and 5 has no local files.
	if _, err := db.Exec(`
		INSERT INTO file_source (id, code, display_name, source_type) VALUES (1, 'example_local', 'Example Local', 'local_folder');
		INSERT INTO work (id, primary_code, title, age_rating, is_permanently_free) VALUES
			(1, 'RJ00000001', 'Example One', 'general', 1),
			(2, 'RJ00000002', 'Example Two', 'general', 1),
			(3, 'RJ00000003', 'Example Three', 'adult', 1),
			(4, 'RJ00000004', 'Example Four', 'general', 0),
			(5, 'RJ00000005', 'Example Five', 'general', 1);
		INSERT INTO work_source_presence (work_id, file_source_id, presence_type, availability) VALUES
			(1, 1, 'local', 'available'), (2, 1, 'local', 'available'),
			(3, 1, 'local', 'available'), (4, 1, 'local', 'available');
	`); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := db.QueryRow("SELECT id FROM user_account WHERE username = '__demo__'").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	count := func(query string, args ...any) float64 {
		t.Helper()
		var value float64
		if err := db.QueryRow(query, args...).Scan(&value); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return value
	}

	noon := time.Date(2026, time.March, 7, 12, 0, 0, 0, time.UTC)
	var seconds float64
	for run := 0; run < 2; run++ {
		if err := s.refreshDemoListening(ctx, noon); err != nil {
			t.Fatal(err)
		}
		if got := count(`SELECT COUNT(*) FROM user_listening_day WHERE user_id <> ? OR work_id NOT IN (1, 2)`, userID); got != 0 {
			t.Fatalf("run %d: %v listening days outside the admitted local works", run, got)
		}
		total := count(`SELECT COALESCE(SUM(listened_seconds), 0) FROM user_listening_day`)
		// Rebuilding the history for the same moment reproduces it.
		if total <= 0 || (run > 0 && total != seconds) {
			t.Fatalf("run %d: listened seconds = %v, previous %v", run, total, seconds)
		}
		seconds = total
		// Sessions, days, and hours describe the same listening.
		if sessions, hours := count(`SELECT SUM(listened_seconds) FROM user_listening_session`), count(`SELECT SUM(listened_seconds) FROM user_listening_hour`); sessions != total || hours != total {
			t.Fatalf("run %d: sessions %v, hours %v, days %v", run, sessions, hours, total)
		}
		if plays, sessions := count(`SELECT SUM(listen_count) FROM user_listening_day`), count(`SELECT COUNT(*) FROM user_listening_session`); plays != sessions {
			t.Fatalf("run %d: plays %v, sessions %v", run, plays, sessions)
		}
		// Nothing is dated after the moment the history is built for, or before its reach.
		if got := count(`SELECT COUNT(*) FROM user_listening_session WHERE updated_at > '2026-03-07 12:00:00'`) + count(`SELECT COUNT(*) FROM user_listening_day WHERE day > '2026-03-07' OR day < '2025-01-31'`); got != 0 {
			t.Fatalf("run %d: %v rows outside the history's span", run, got)
		}
		if got := count(`SELECT COUNT(*) FROM user_listening_hour WHERE listened_seconds > 3600`); got != 0 {
			t.Fatalf("run %d: %v hours hold more than an hour", run, got)
		}
		if got := count(`SELECT COUNT(*) FROM user_work_state WHERE user_id = ? AND rating BETWEEN 3 AND 5 AND work_id IN (1, 2)`, userID); got != 2 {
			t.Fatalf("run %d: rated works = %v", run, got)
		}
	}

	// The listening report reads the simulated history like any other.
	stats, err := (personal.Store{DB: db}).StatisticsFor(ctx, userID, personal.RangeLast30Days, noon)
	if err != nil || stats.ListenedSeconds <= 0 || stats.WorkCount == 0 || len(stats.Hours) != 24 || len(stats.Years) != 2 {
		t.Fatalf("statistics = %+v, %v", stats, err)
	}
	// Demo serves the dashboard's reads to its visitors, who carry no session.
	for path, field := range map[string]string{
		"/api/listening-statistics?range=all": "listenedSeconds",
		"/api/listening-profile?range=all":    "workCount",
		"/api/listening-calendar?range=2026":  "days",
	} {
		response := httptest.NewRecorder()
		s.Routes().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s, %v", path, response.Code, response.Body.String(), err)
		}
		// The field is a positive number or a list with entries.
		number, _ := body[field].(float64)
		list, _ := body[field].([]any)
		if number <= 0 && len(list) == 0 {
			t.Fatalf("GET %s %s = %v", path, field, body[field])
		}
	}
	// A visitor's own listening is still refused.
	response := httptest.NewRecorder()
	s.Routes().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/playback-reports", strings.NewReader(`{"progress":[],"history":[]}`)))
	if response.Code != http.StatusForbidden {
		t.Fatalf("POST playback report = %d", response.Code)
	}

	// A later moment moves the history's span forward and keeps the days both spans share.
	const shared = `SELECT COALESCE(SUM(listened_seconds), 0) FROM user_listening_day WHERE day BETWEEN '2025-03-01' AND '2026-03-06'`
	before := count(shared)
	if err := s.refreshDemoListening(ctx, noon.AddDate(0, 0, 3)); err != nil {
		t.Fatal(err)
	}
	if after := count(shared); before <= 0 || after != before {
		t.Fatalf("past listening changed from %v to %v", before, after)
	}
	if got := count(`SELECT COUNT(*) FROM user_listening_day WHERE day < '2025-02-03' OR day > '2026-03-10'`); got != 0 {
		t.Fatalf("%v days outside the later span", got)
	}

	// A work that leaves admission leaves the history at the next rebuild.
	if _, err := db.Exec("UPDATE work SET is_permanently_free = 0 WHERE id IN (1, 2)"); err != nil {
		t.Fatal(err)
	}
	if err := s.refreshDemoListening(ctx, noon); err != nil {
		t.Fatal(err)
	}
	if got := count(`SELECT (SELECT COUNT(*) FROM user_listening_day) + (SELECT COUNT(*) FROM user_listening_session) + (SELECT COUNT(*) FROM user_listening_hour)`); got != 0 {
		t.Fatalf("history without admitted works = %v rows", got)
	}
}
