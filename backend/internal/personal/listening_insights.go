package personal

import (
	"context"
	"time"
)

// calendarWeeks is the number of whole weeks before the current one a
// listening calendar covers.
const calendarWeeks = 52

const breakdownLimit = 10

// Calendar is the dated listening of the weeks ending with the current one.
type Calendar struct {
	// From is the Monday the calendar starts on and To is today, both UTC days.
	From string `json:"from"`
	To   string `json:"to"`
	// Days is sparse and oldest first; a day without listening is absent.
	Days []ListeningDay `json:"days"`
}

// CalendarFor reports dated listening per UTC day from the Monday 52 weeks
// before the current week through now. Imported totals have no dates and
// never appear.
func (s Store) CalendarFor(ctx context.Context, user int64, now time.Time) (Calendar, error) {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	monday := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	from := monday.AddDate(0, 0, -7*calendarWeeks)
	result := Calendar{From: from.Format(time.DateOnly), To: today.Format(time.DateOnly)}
	var err error
	result.Days, err = s.recentListeningDays(ctx, user, from)
	return result, err
}

// BreakdownEntry is the listening of every work that carries one tag, credits
// one voice, or belongs to one circle.
type BreakdownEntry struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// ExternalID is a circle's provider id, or empty when it has none.
	ExternalID      string  `json:"externalId,omitempty"`
	ListenedSeconds float64 `json:"listenedSeconds"`
	ListenCount     int64   `json:"listenCount"`
	WorkCount       int     `json:"workCount"`
}

// Breakdown ranks one range's listening by shared tag, voice, and circle. A
// work counts in full toward each of its tags and voices, so the entries of a
// dimension do not add up to ListenedSeconds.
type Breakdown struct {
	Range           StatisticsRange  `json:"range"`
	ListenedSeconds float64          `json:"listenedSeconds"`
	Tags            []BreakdownEntry `json:"tags"`
	Voices          []BreakdownEntry `json:"voices"`
	Circles         []BreakdownEntry `json:"circles"`
}

// BreakdownFor ranks the range ending at now (UTC) with the same scope as
// StatisticsFor: a bounded range counts only dated listening, and all time
// also includes imported per-work totals.
func (s Store) BreakdownFor(ctx context.Context, user int64, period StatisticsRange, now time.Time) (Breakdown, error) {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	result := Breakdown{Range: period, Tags: []BreakdownEntry{}, Voices: []BreakdownEntry{}, Circles: []BreakdownEntry{}}
	var listened string
	var args []any
	switch period {
	case RangeAll:
		listened = historyCTE + `, listened AS (SELECT work_id,listened_seconds,listen_count FROM history) `
		args = []any{user, user}
	case RangeLast30Days, RangeLast12Months:
		listened = `WITH listened AS (SELECT work_id,SUM(listened_seconds) listened_seconds,SUM(listen_count) listen_count FROM user_listening_day WHERE user_id = ? AND day >= ? GROUP BY work_id) `
		args = []any{user, boundedRangeStart(period, today).Format(time.DateOnly)}
	default:
		return result, ErrInvalid
	}
	if err := s.DB.QueryRowContext(ctx, listened+`SELECT COALESCE(SUM(listened_seconds),0) FROM listened`, args...).Scan(&result.ListenedSeconds); err != nil {
		return result, err
	}
	const ranking = ` GROUP BY entity_id ORDER BY SUM(listened_seconds) DESC,SUM(listen_count) DESC,entity_id LIMIT ?`
	var err error
	// A work can hold one shared tag from several sources.
	if result.Tags, err = s.breakdownEntries(ctx, listened+`, entity AS (SELECT DISTINCT l.work_id,t.id entity_id,t.display_name name,'' external_id,l.listened_seconds,l.listen_count
 FROM listened l JOIN work_tag r ON r.work_id = l.work_id JOIN tag t ON t.id = r.tag_id WHERE t.namespace IN ('dlsite','metadata') AND TRIM(t.display_name) <> '')
 SELECT entity_id,MAX(name),MAX(external_id),SUM(listened_seconds),SUM(listen_count),COUNT(*) FROM entity`+ranking, args); err != nil {
		return result, err
	}
	if result.Voices, err = s.breakdownEntries(ctx, listened+`SELECT p.id entity_id,p.display_name,'',SUM(l.listened_seconds),SUM(l.listen_count),COUNT(*)
 FROM listened l JOIN work_credit c ON c.work_id = l.work_id AND c.role = 'voice_actor' JOIN person p ON p.id = c.person_id`+ranking, args); err != nil {
		return result, err
	}
	result.Circles, err = s.breakdownEntries(ctx, listened+`SELECT c.party_id entity_id,MAX(c.display_name),MAX(c.external_id),SUM(l.listened_seconds),SUM(l.listen_count),COUNT(*)
 FROM listened l JOIN work_primary_circle c ON c.work_id = l.work_id`+ranking, args)
	return result, err
}

func (s Store) breakdownEntries(ctx context.Context, query string, args []any) ([]BreakdownEntry, error) {
	rows, err := s.DB.QueryContext(ctx, query, append(append([]any{}, args...), breakdownLimit)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	entries := []BreakdownEntry{}
	for rows.Next() {
		var entry BreakdownEntry
		if err = rows.Scan(&entry.ID, &entry.Name, &entry.ExternalID, &entry.ListenedSeconds, &entry.ListenCount, &entry.WorkCount); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}
