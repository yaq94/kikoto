package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/yexca/kikoto/backend/internal/account"
	"github.com/yexca/kikoto/backend/internal/contentpolicy"
	"github.com/yexca/kikoto/backend/internal/demopresentation"
)

const (
	// demoListeningDays is how far back the simulated history reaches.
	demoListeningDays = 400
	// demoListeningWorkLimit bounds the works the simulated history plays.
	demoListeningWorkLimit = 24
	// demoListeningRatedWorks is how many of the most played works carry a rating.
	demoListeningRatedWorks = 8
	// demoListeningRefreshInterval is how often a running Demo checks whether
	// its history should reach a new hour.
	demoListeningRefreshInterval = 10 * time.Minute
)

// demoShelfWorkSQL selects the works a Demo personal surface may show. A local
// presence alone is insufficient: the work must also pass Demo's all-age,
// permanently-free policy.
var demoShelfWorkSQL = contentpolicy.DemoEligibleWorkSQL("work") + `
	AND EXISTS (SELECT 1 FROM work_source_presence AS presence
		JOIN file_source AS source ON source.id = presence.file_source_id
		WHERE presence.work_id = work.id AND presence.presence_type = 'local'
			AND presence.availability = 'available' AND source.source_type = 'local_folder')`

// seedDemoListening replaces the Demo account's listening history with a
// simulated one over the admitted local works: sessions, days, and hours of
// the day from demoListeningDays ago through now, and ratings on the works it
// plays most. The server rejects every Demo write, so without it the
// listening dashboard would stay empty. Each day's listening depends only on
// its date, so rebuilding the history later keeps its past.
func seedDemoListening(ctx context.Context, tx *sql.Tx, now time.Time) error {
	var userID int64
	if err := tx.QueryRowContext(ctx, "SELECT id FROM user_account WHERE username = ?", account.DemoUsername).Scan(&userID); err != nil {
		return fmt.Errorf("find demo account: %w", err)
	}
	for _, table := range []string{"user_listening_session", "user_listening_import", "user_listening_day", "user_listening_hour"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE user_id = ?", userID); err != nil {
			return err
		}
	}

	type demoWork struct {
		id       int64
		affinity float64
	}
	rows, err := tx.QueryContext(ctx, "SELECT work.id, work.primary_code FROM work WHERE "+demoShelfWorkSQL+" ORDER BY work.id")
	if err != nil {
		return err
	}
	works := []demoWork{}
	for rows.Next() {
		var work demoWork
		var code string
		if err := rows.Scan(&work.id, &code); err != nil {
			_ = rows.Close()
			return err
		}
		work.affinity = demopresentation.ListeningAffinity(code)
		works = append(works, work)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(works) == 0 {
		return nil
	}
	sort.SliceStable(works, func(i, j int) bool { return works[i].affinity < works[j].affinity })
	if len(works) > demoListeningWorkLimit {
		works = works[:demoListeningWorkLimit]
	}

	insertSession, err := tx.PrepareContext(ctx, `INSERT INTO user_listening_session (user_id,session_id,work_id,listened_seconds,created_at,updated_at,dated_report) VALUES (?,?,?,?,?,?,1)`)
	if err != nil {
		return err
	}
	defer func() { _ = insertSession.Close() }()
	addDay, err := tx.PrepareContext(ctx, `INSERT INTO user_listening_day (user_id,work_id,day,listened_seconds,listen_count) VALUES (?,?,?,?,1)
		ON CONFLICT(user_id,work_id,day) DO UPDATE SET listened_seconds=listened_seconds+excluded.listened_seconds,listen_count=listen_count+1`)
	if err != nil {
		return err
	}
	defer func() { _ = addDay.Close() }()
	addHour, err := tx.PrepareContext(ctx, `INSERT INTO user_listening_hour (user_id,day,hour,listened_seconds) VALUES (?,?,?,?)
		ON CONFLICT(user_id,day,hour) DO UPDATE SET listened_seconds=listened_seconds+excluded.listened_seconds`)
	if err != nil {
		return err
	}
	defer func() { _ = addHour.Close() }()

	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	const timestamp = "2006-01-02 15:04:05"
	for offset := demoListeningDays; offset >= 0; offset-- {
		day := today.AddDate(0, 0, -offset)
		date := day.Format(time.DateOnly)
		for index, session := range demopresentation.ListeningDay(day, len(works)) {
			start := day.Add(time.Duration(session.StartSecond) * time.Second)
			end := start.Add(time.Duration(session.Seconds) * time.Second)
			// Today's later sessions have not happened yet.
			if end.After(now) {
				break
			}
			work := works[session.WorkRank].id
			if _, err := insertSession.ExecContext(ctx, userID, fmt.Sprintf("demo-%s-%d", date, index), work, session.Seconds, start.Format(timestamp), end.Format(timestamp)); err != nil {
				return err
			}
			if _, err := addDay.ExecContext(ctx, userID, work, date, session.Seconds); err != nil {
				return err
			}
			for second := session.StartSecond; second < session.StartSecond+session.Seconds; {
				hour := second / 3600
				amount := min((hour+1)*3600, session.StartSecond+session.Seconds) - second
				if _, err := addHour.ExecContext(ctx, userID, date, hour, amount); err != nil {
					return err
				}
				second += amount
			}
		}
	}

	// The works played most carry a rating, so the taste profile can compare
	// the account's ratings with the public ones.
	for rank, work := range works {
		if rank >= demoListeningRatedWorks {
			break
		}
		rating := 5 - int(work.affinity*1000)%3
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_work_state (user_id,work_id,rating) VALUES (?,?,?)
			ON CONFLICT(user_id,work_id) DO UPDATE SET rating = excluded.rating`, userID, work.id, rating); err != nil {
			return err
		}
	}
	return nil
}

// refreshDemoListening rebuilds the simulated history through now.
func (s *Server) refreshDemoListening(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := seedDemoListening(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

// RunDemoListeningRefresh keeps a long-running Demo's simulated history
// reaching the current hour. Startup seeds it once; this rebuilds it whenever
// the hour changes.
func (s *Server) RunDemoListeningRefresh(ctx context.Context) {
	if !s.cfg.IsDemo() {
		return
	}
	ticker := time.NewTicker(demoListeningRefreshInterval)
	defer ticker.Stop()
	seeded := time.Now().UTC().Truncate(time.Hour)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			hour := now.UTC().Truncate(time.Hour)
			if !hour.After(seeded) {
				continue
			}
			if err := s.refreshDemoListening(ctx, now); err != nil {
				if ctx.Err() == nil {
					slog.Warn("refresh demo listening history", "error", err)
				}
				continue
			}
			seeded = hour
		}
	}
}
