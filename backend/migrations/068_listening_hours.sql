-- Listening time per hour of the day, in the same calendar a listening day is
-- counted in. Hours are recorded from this version on; earlier listening has
-- none.
CREATE TABLE user_listening_hour (
  user_id INTEGER NOT NULL REFERENCES user_account(id) ON DELETE CASCADE,
  day TEXT NOT NULL,
  hour INTEGER NOT NULL CHECK(hour BETWEEN 0 AND 23),
  listened_seconds REAL NOT NULL DEFAULT 0 CHECK(listened_seconds >= 0),
  PRIMARY KEY(user_id, day, hour)
);

-- A dated session's cumulative hour buckets, so a repeated report credits
-- only each bucket's increase.
CREATE TABLE user_listening_session_hour (
  user_id INTEGER NOT NULL,
  session_id TEXT NOT NULL,
  day TEXT NOT NULL,
  hour INTEGER NOT NULL CHECK(hour BETWEEN 0 AND 23),
  listened_seconds REAL NOT NULL CHECK(listened_seconds >= 0),
  PRIMARY KEY(user_id, session_id, day, hour),
  FOREIGN KEY(user_id, session_id) REFERENCES user_listening_session(user_id, session_id) ON DELETE CASCADE
);
