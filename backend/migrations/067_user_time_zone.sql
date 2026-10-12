-- Per-user time zone as an IANA name. NULL follows the instance default in
-- app_setting `default_time_zone`, which falls back to the server's own zone.
-- The zone decides where a day starts for listening recorded from then on;
-- stored listening days keep the day they were recorded under.
ALTER TABLE user_preference ADD COLUMN time_zone TEXT;
