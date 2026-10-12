package personal

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// StatisticsRange selects the period a listening report covers: one of the
// named ranges, or a calendar year such as "2026".
type StatisticsRange string

const (
	RangeLast30Days   StatisticsRange = "30d"
	RangeLast12Months StatisticsRange = "12m"
	RangeAll          StatisticsRange = "all"
)

// Granularity is the length of one series period.
type Granularity string

const (
	GranularityDay   Granularity = "day"
	GranularityMonth Granularity = "month"
	GranularityYear  Granularity = "year"
)

// allTimeMonthlyLimit is the longest all-time span still charted by month;
// longer histories are charted by year so the chart stays readable.
const allTimeMonthlyLimit = 36

const topWorkLimit = 10

// ParseStatisticsRange accepts the public range names and calendar years. An
// empty value is the all-time report that clients without a range expect.
func ParseStatisticsRange(value string) (StatisticsRange, error) {
	period := StatisticsRange(value)
	switch period {
	case "":
		return RangeAll, nil
	case RangeLast30Days, RangeLast12Months, RangeAll:
		return period, nil
	}
	if _, ok := period.Year(); ok {
		return period, nil
	}
	return "", ErrInvalid
}

// Year is the calendar year a range names, when it names one.
func (period StatisticsRange) Year() (int, bool) {
	if len(period) != 4 {
		return 0, false
	}
	year, err := strconv.Atoi(string(period))
	if err != nil || year < 1970 {
		return 0, false
	}
	return year, true
}

// reportWindow is the calendar days a report covers, both ends included. An
// unbounded window is all time and has no first day.
type reportWindow struct {
	from, to time.Time
	bounded  bool
}

// rangeWindow resolves a range against the account's current day: the last
// 30 days, the last 12 calendar months, all time, or one calendar year. The
// current year ends today.
func rangeWindow(period StatisticsRange, today time.Time) (reportWindow, error) {
	switch period {
	case RangeAll:
		return reportWindow{to: today}, nil
	case RangeLast30Days:
		return reportWindow{from: today.AddDate(0, 0, -29), to: today, bounded: true}, nil
	case RangeLast12Months:
		return reportWindow{from: time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -11, 0), to: today, bounded: true}, nil
	}
	year, ok := period.Year()
	if !ok {
		return reportWindow{}, ErrInvalid
	}
	window := reportWindow{from: time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC), to: time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC), bounded: true}
	if year == today.Year() {
		window.to = today
	}
	return window, nil
}

type ListeningDay struct {
	Date            string  `json:"date"`
	ListenedSeconds float64 `json:"listenedSeconds"`
	ListenCount     int64   `json:"listenCount"`
}

// ListeningPeriod is one series bucket: a day (2006-01-02), month (2006-01),
// or year (2006) of the account's calendar.
type ListeningPeriod struct {
	Period          string  `json:"period"`
	ListenedSeconds float64 `json:"listenedSeconds"`
	ListenCount     int64   `json:"listenCount"`
}

// BusiestDay is the day of a range with the most listening and the work
// listened to most on it.
type BusiestDay struct {
	Date            string       `json:"date"`
	ListenedSeconds float64      `json:"listenedSeconds"`
	TopWork         *HistoryItem `json:"topWork,omitempty"`
}

// Highlights are the standout figures of one calendar year.
type Highlights struct {
	BusiestDay *BusiestDay `json:"busiestDay,omitempty"`
	// NewWorkCount is the works whose first dated listening falls in the year.
	NewWorkCount int `json:"newWorkCount"`
	// TopVoice and TopCircle lead the year by listening time.
	TopVoice  *BreakdownEntry `json:"topVoice,omitempty"`
	TopCircle *BreakdownEntry `json:"topCircle,omitempty"`
}

type Statistics struct {
	Range           StatisticsRange `json:"range"`
	ListenedSeconds float64         `json:"listenedSeconds"`
	ListenCount     int64           `json:"listenCount"`
	WorkCount       int             `json:"workCount"`
	ActiveDays      int             `json:"activeDays"`
	Granularity     Granularity     `json:"granularity"`
	// Today is the calendar day the report ends on.
	Today string `json:"today"`
	// Series covers the whole range oldest first; periods without listening are zero.
	Series []ListeningPeriod `json:"series"`
	// Daily is the sparse last 30 days for clients that do not read Series.
	Daily    []ListeningDay `json:"daily"`
	TopWorks []HistoryItem  `json:"topWorks"`
	// Years are the calendar years with dated listening, latest first.
	Years []string `json:"years"`
	// Hours is the range's listening per hour of the day, midnight first, for
	// the listening whose hours were recorded; absent when none was.
	Hours []float64 `json:"hours,omitempty"`
	// Highlights describe a calendar year; other ranges have none.
	Highlights *Highlights `json:"highlights,omitempty"`
}

// Statistics is the all-time listening report.
func (s Store) Statistics(ctx context.Context, user int64) (Statistics, error) {
	return s.StatisticsFor(ctx, user, RangeAll, time.Now())
}

// StatisticsFor reports one range, read in now's own time zone. A named range
// ends on now's calendar day and a year on its last day. A bounded range
// counts only dated listening; all time also includes imported per-work
// totals, which have no dates and therefore never appear in the series.
func (s Store) StatisticsFor(ctx context.Context, user int64, period StatisticsRange, now time.Time) (Statistics, error) {
	today := calendarDay(now)
	result := Statistics{Range: period, Series: []ListeningPeriod{}, Daily: []ListeningDay{}, TopWorks: []HistoryItem{}, Years: []string{}}
	window, err := rangeWindow(period, today)
	if err != nil {
		return result, err
	}
	result.Granularity = GranularityMonth
	switch {
	case period == RangeLast30Days:
		result.Granularity = GranularityDay
	case !window.bounded:
		first, err := s.firstListeningDay(ctx, user, today)
		if err != nil {
			return result, err
		}
		window.from = time.Date(first.Year(), first.Month(), 1, 0, 0, 0, 0, time.UTC)
		if monthsBetween(window.from, today) >= allTimeMonthlyLimit {
			window.from = time.Date(first.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
			result.Granularity = GranularityYear
		}
	}
	result.Today = window.to.Format(time.DateOnly)
	from, to := window.from.Format(time.DateOnly), window.to.Format(time.DateOnly)

	if window.bounded {
		err = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(listened_seconds),0),COALESCE(SUM(listen_count),0),COUNT(DISTINCT work_id),COUNT(DISTINCT day) FROM `+reportDays+` WHERE day >= ? AND day <= ?`, user, from, to).Scan(&result.ListenedSeconds, &result.ListenCount, &result.WorkCount, &result.ActiveDays)
	} else {
		err = s.DB.QueryRowContext(ctx, reportHistoryCTE+`SELECT COALESCE(SUM(listened_seconds),0),COALESCE(SUM(listen_count),0),COUNT(*) FROM history`, user, user).Scan(&result.ListenedSeconds, &result.ListenCount, &result.WorkCount)
		if err == nil {
			err = s.DB.QueryRowContext(ctx, `SELECT COUNT(DISTINCT day) FROM user_listening_day WHERE user_id = ?`, user).Scan(&result.ActiveDays)
		}
	}
	if err != nil {
		return result, err
	}
	if result.Series, err = s.listeningSeries(ctx, user, result.Granularity, window.from, window.to); err != nil {
		return result, err
	}
	if result.Daily, err = s.listeningDays(ctx, user, today.AddDate(0, 0, -29), today); err != nil {
		return result, err
	}
	if result.TopWorks, err = s.topWorks(ctx, user, window); err != nil {
		return result, err
	}
	if result.Years, err = s.listeningYears(ctx, user); err != nil {
		return result, err
	}
	if result.Hours, err = s.listeningHours(ctx, user, window); err != nil {
		return result, err
	}
	if _, ok := period.Year(); ok {
		result.Highlights, err = s.highlights(ctx, user, period, window, now)
	}
	return result, err
}

// calendarDay is the calendar day now falls on in its own time zone, as a
// UTC midnight for day arithmetic. Listening days are stored as the calendar
// day they were recorded under.
func calendarDay(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func (s Store) firstListeningDay(ctx context.Context, user int64, today time.Time) (time.Time, error) {
	var first sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT MIN(day) FROM user_listening_day WHERE user_id = ?`, user).Scan(&first); err != nil {
		return today, err
	}
	if !first.Valid {
		return today, nil
	}
	day, err := time.Parse(time.DateOnly, first.String)
	if err != nil || day.After(today) {
		return today, nil
	}
	return day, nil
}

func monthsBetween(from, to time.Time) int {
	return (to.Year()-from.Year())*12 + int(to.Month()) - int(from.Month())
}

func periodKey(granularity Granularity, day time.Time) string {
	switch granularity {
	case GranularityYear:
		return day.Format("2006")
	case GranularityMonth:
		return day.Format("2006-01")
	default:
		return day.Format(time.DateOnly)
	}
}

func nextPeriod(granularity Granularity, day time.Time) time.Time {
	switch granularity {
	case GranularityYear:
		return day.AddDate(1, 0, 0)
	case GranularityMonth:
		return day.AddDate(0, 1, 0)
	default:
		return day.AddDate(0, 0, 1)
	}
}

// listeningSeries sums dated listening into every period from from through
// to, so a period without listening is an explicit zero.
func (s Store) listeningSeries(ctx context.Context, user int64, granularity Granularity, from, to time.Time) ([]ListeningPeriod, error) {
	length := map[Granularity]int{GranularityDay: 10, GranularityMonth: 7, GranularityYear: 4}[granularity]
	rows, err := s.DB.QueryContext(ctx, `SELECT substr(day,1,?) period,SUM(listened_seconds),SUM(listen_count) FROM user_listening_day WHERE user_id = ? AND day >= ? AND day <= ? GROUP BY period`, length, user, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	totals := map[string]ListeningPeriod{}
	for rows.Next() {
		var item ListeningPeriod
		if err = rows.Scan(&item.Period, &item.ListenedSeconds, &item.ListenCount); err != nil {
			return nil, err
		}
		totals[item.Period] = item
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	series := []ListeningPeriod{}
	last := periodKey(granularity, to)
	for day := from; ; day = nextPeriod(granularity, day) {
		key := periodKey(granularity, day)
		item := totals[key]
		item.Period = key
		series = append(series, item)
		if key == last || day.After(to) {
			return series, nil
		}
	}
}

// listeningDays sums dated listening per day from from through to, oldest
// first; a day without listening is absent.
func (s Store) listeningDays(ctx context.Context, user int64, from, to time.Time) ([]ListeningDay, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT day,SUM(listened_seconds),SUM(listen_count) FROM user_listening_day WHERE user_id = ? AND day >= ? AND day <= ? GROUP BY day ORDER BY day`, user, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	days := []ListeningDay{}
	for rows.Next() {
		var day ListeningDay
		if err = rows.Scan(&day.Date, &day.ListenedSeconds, &day.ListenCount); err != nil {
			return nil, err
		}
		days = append(days, day)
	}
	return days, rows.Err()
}

func (s Store) listeningYears(ctx context.Context, user int64) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT substr(day,1,4) FROM user_listening_day WHERE user_id = ? ORDER BY 1 DESC`, user)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	years := []string{}
	for rows.Next() {
		var year string
		if err = rows.Scan(&year); err != nil {
			return nil, err
		}
		years = append(years, year)
	}
	return years, rows.Err()
}

// listeningHours sums a window's recorded hours into the 24 hours of the day,
// or returns nil when the window has none.
func (s Store) listeningHours(ctx context.Context, user int64, window reportWindow) ([]float64, error) {
	query, args := `SELECT hour,SUM(listened_seconds) FROM user_listening_hour WHERE user_id = ?`, []any{user}
	if window.bounded {
		query += ` AND day >= ? AND day <= ?`
		args = append(args, window.from.Format(time.DateOnly), window.to.Format(time.DateOnly))
	}
	rows, err := s.DB.QueryContext(ctx, query+` GROUP BY hour`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var hours []float64
	for rows.Next() {
		var hour int
		var seconds float64
		if err = rows.Scan(&hour, &seconds); err != nil {
			return nil, err
		}
		if hour < 0 || hour > 23 || seconds <= 0 {
			continue
		}
		if hours == nil {
			hours = make([]float64, 24)
		}
		hours[hour] = seconds
	}
	return hours, rows.Err()
}

// highlights picks a year's standout figures.
func (s Store) highlights(ctx context.Context, user int64, period StatisticsRange, window reportWindow, now time.Time) (*Highlights, error) {
	from, to := window.from.Format(time.DateOnly), window.to.Format(time.DateOnly)
	result := &Highlights{}
	// A work is new in the year its first dated listening falls in.
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT MIN(day) first FROM `+reportDays+` GROUP BY work_id) WHERE first >= ? AND first <= ?`, user, from, to).Scan(&result.NewWorkCount); err != nil {
		return nil, err
	}
	busiest := BusiestDay{}
	err := s.DB.QueryRowContext(ctx, `SELECT day,SUM(listened_seconds) seconds FROM user_listening_day WHERE user_id = ? AND day >= ? AND day <= ? GROUP BY day HAVING seconds > 0 ORDER BY seconds DESC,day DESC LIMIT 1`, user, from, to).Scan(&busiest.Date, &busiest.ListenedSeconds)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		rows, err := s.DB.QueryContext(ctx, `SELECT w.id,w.primary_code,w.title,d.listened_seconds,d.listen_count,'' FROM (SELECT work_id,SUM(listened_seconds) listened_seconds,SUM(listen_count) listen_count FROM `+reportDays+` WHERE day = ? GROUP BY work_id) d JOIN work w ON w.id = d.work_id ORDER BY d.listened_seconds DESC,d.listen_count DESC,w.id LIMIT 1`, user, busiest.Date)
		if err != nil {
			return nil, err
		}
		works, err := historyItems(rows)
		if err != nil {
			return nil, err
		}
		if len(works) > 0 {
			busiest.TopWork = &works[0]
		}
		result.BusiestDay = &busiest
	}
	breakdown, err := s.BreakdownFor(ctx, user, period, RankTime, now)
	if err != nil {
		return nil, err
	}
	if len(breakdown.Voices) > 0 {
		result.TopVoice = &breakdown.Voices[0]
	}
	if len(breakdown.Circles) > 0 {
		result.TopCircle = &breakdown.Circles[0]
	}
	return result, nil
}

// topWorks ranks by listening time in the window, then by plays. Last played
// always reflects the whole history.
func (s Store) topWorks(ctx context.Context, user int64, window reportWindow) ([]HistoryItem, error) {
	var rows *sql.Rows
	var err error
	if window.bounded {
		rows, err = s.DB.QueryContext(ctx, reportHistoryCTE+`, period AS (SELECT work_id,SUM(listened_seconds) listened_seconds,SUM(listen_count) listen_count FROM `+reportDays+` WHERE day >= ? AND day <= ? GROUP BY work_id)
 SELECT w.id,w.primary_code,w.title,p.listened_seconds,p.listen_count,COALESCE(h.last_played_at,'') FROM period p JOIN work w ON w.id = p.work_id LEFT JOIN history h ON h.work_id = p.work_id ORDER BY p.listened_seconds DESC,p.listen_count DESC,w.id LIMIT ?`, user, user, user, window.from.Format(time.DateOnly), window.to.Format(time.DateOnly), topWorkLimit)
	} else {
		rows, err = s.DB.QueryContext(ctx, reportHistoryCTE+`SELECT w.id,w.primary_code,w.title,h.listened_seconds,h.listen_count,h.last_played_at FROM history h JOIN work w ON w.id = h.work_id ORDER BY h.listened_seconds DESC,h.listen_count DESC,w.id LIMIT ?`, user, user, topWorkLimit)
	}
	if err != nil {
		return nil, err
	}
	return historyItems(rows)
}
