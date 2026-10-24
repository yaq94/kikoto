package httpapi

import "time"

// Catalog schedules follow a wall clock in Asia/Shanghai. They do not depend on
// when the previous scan finished, so a long-running or missed scan cannot move
// the daily or weekly boundary.
var catalogScheduleLocation = func() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return location
}()

const catalogScheduleHour = 3

// nextCatalogDailyDue returns the next local 03:00 strictly after now.
func nextCatalogDailyDue(now time.Time) time.Time {
	local := now.In(catalogScheduleLocation)
	next := time.Date(local.Year(), local.Month(), local.Day(), catalogScheduleHour, 0, 0, 0, catalogScheduleLocation)
	if !local.Before(next) {
		next = next.AddDate(0, 0, 1)
	}
	return next.UTC()
}

// nextCatalogWeeklyDue returns the next local Sunday 03:00 strictly after now.
func nextCatalogWeeklyDue(now time.Time) time.Time {
	local := now.In(catalogScheduleLocation)
	next := time.Date(local.Year(), local.Month(), local.Day(), catalogScheduleHour, 0, 0, 0, catalogScheduleLocation)
	if !local.Before(next) {
		next = next.AddDate(0, 0, 1)
	}
	next = next.AddDate(0, 0, (7-int(next.Weekday()))%7)
	return next.UTC()
}
