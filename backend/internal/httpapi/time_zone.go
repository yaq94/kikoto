package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"strings"
	"time"
	// The zone database ships with the executable, so every IANA name resolves
	// in an image without system zone files.
	_ "time/tzdata"
)

const maxTimeZoneNameLength = 64

// deviceTimeZoneParameter carries the zone a client's device runs in.
const deviceTimeZoneParameter = "timeZone"

// validTimeZone reports whether name is an IANA zone the server can resolve.
// The process-local pseudo zone is not a name a client can use.
func validTimeZone(name string) bool {
	if name == "" || name == "Local" || len(name) > maxTimeZoneNameLength || strings.TrimSpace(name) != name {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// serverTimeZone is the zone the process runs in when its TZ names one, and
// UTC otherwise, which is what a container without TZ uses.
func serverTimeZone() string {
	if name := os.Getenv("TZ"); validTimeZone(name) {
		return name
	}
	return "UTC"
}

// ownTimeZone is the zone an account fixed for itself and whether it has one.
// An account without one follows the device it is used from.
func (s *Server) ownTimeZone(ctx context.Context, userID int64) (string, bool) {
	var name sql.NullString
	if err := s.db.QueryRowContext(ctx, "SELECT time_zone FROM user_preference WHERE user_id = ?", userID).Scan(&name); err != nil {
		return "", false
	}
	if !name.Valid || !validTimeZone(name.String) {
		return "", false
	}
	return name.String, true
}

// accountTimeZone is the zone a request counts an account's days in, and
// whether that zone follows the device: the account's own zone, else the zone
// the client names for its device, else the server's.
func (s *Server) accountTimeZone(r *http.Request, userID int64) (string, bool) {
	if name, ok := s.ownTimeZone(r.Context(), userID); ok {
		return name, false
	}
	if name := r.URL.Query().Get(deviceTimeZoneParameter); validTimeZone(name) {
		return name, true
	}
	return serverTimeZone(), true
}

// accountNow is the current time in the zone a request counts the account's
// days in.
func (s *Server) accountNow(r *http.Request, userID int64) time.Time {
	name, _ := s.accountTimeZone(r, userID)
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.Now().UTC()
	}
	return time.Now().In(location)
}
