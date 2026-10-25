-- One persisted wall-clock schedule per OpenList source. The old
-- source_catalog_daily: and source_catalog_full: settings are read only here,
-- to carry a still-valid boundary into the new row; no runtime query reads
-- them after this migration.
CREATE TABLE source_catalog_schedule (
  file_source_id INTEGER PRIMARY KEY REFERENCES file_source(id) ON DELETE CASCADE,
  daily_due_at INTEGER NOT NULL,
  weekly_due_at INTEGER NOT NULL,
  deferred_until INTEGER NOT NULL DEFAULT 0,
  deferred_reason TEXT NOT NULL DEFAULT '',
  manual_pending INTEGER NOT NULL DEFAULT 0 CHECK(manual_pending IN (0,1)),
  resume_full_pending INTEGER NOT NULL DEFAULT 0 CHECK(resume_full_pending IN (0,1)),
  pending_full_run_id INTEGER REFERENCES workflow_run(id) ON DELETE SET NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX source_catalog_schedule_due
  ON source_catalog_schedule(deferred_until, daily_due_at, weekly_due_at);

-- Asia/Shanghai has no daylight saving time, so local wall clock is UTC+8.
-- The next daily boundary is today's 03:00 when it is still ahead, otherwise
-- tomorrow's. The weekly boundary is the next Sunday 03:00 after that daily
-- candidate. strftime('%s', ...) reads the assembled timestamp as UTC, so the
-- local 03:00 epoch subtracts eight hours.
INSERT INTO source_catalog_schedule(
  file_source_id, daily_due_at, weekly_due_at, deferred_until, deferred_reason,
  manual_pending, resume_full_pending, pending_full_run_id
)
SELECT source.id,
  CASE
    WHEN daily_setting.value_json IS NOT NULL
      AND TRIM(REPLACE(daily_setting.value_json, '"', '')) <> ''
      AND TRIM(REPLACE(daily_setting.value_json, '"', '')) NOT GLOB '*[^0-9]*'
      AND CAST(TRIM(REPLACE(daily_setting.value_json, '"', '')) AS INTEGER) > strftime('%s', 'now')
    THEN CAST(TRIM(REPLACE(daily_setting.value_json, '"', '')) AS INTEGER)
    WHEN time('now', '+8 hours') < '03:00:00'
    THEN strftime('%s', date('now', '+8 hours') || ' 03:00:00') - 28800
    ELSE strftime('%s', date('now', '+8 hours', '+1 day') || ' 03:00:00') - 28800
  END,
  CASE
    WHEN weekly_setting.value_json IS NOT NULL
      AND TRIM(REPLACE(weekly_setting.value_json, '"', '')) <> ''
      AND TRIM(REPLACE(weekly_setting.value_json, '"', '')) NOT GLOB '*[^0-9]*'
      AND CAST(TRIM(REPLACE(weekly_setting.value_json, '"', '')) AS INTEGER) > strftime('%s', 'now')
    THEN CAST(TRIM(REPLACE(weekly_setting.value_json, '"', '')) AS INTEGER)
    ELSE strftime(
      '%s',
      date(
        'now',
        '+8 hours',
        CASE WHEN time('now', '+8 hours') >= '03:00:00' THEN '+1 day' ELSE '+0 day' END
      ) || ' 03:00:00'
    ) - 28800 + (
      (7 - CAST(strftime(
        '%w',
        date(
          'now',
          '+8 hours',
          CASE WHEN time('now', '+8 hours') >= '03:00:00' THEN '+1 day' ELSE '+0 day' END
        )
      ) AS INTEGER)) % 7
    ) * 86400
  END,
  0,
  '',
  0,
  0,
  NULL
FROM file_source AS source
LEFT JOIN app_setting AS daily_setting
  ON daily_setting.key = 'source_catalog_daily:' || source.id
LEFT JOIN app_setting AS weekly_setting
  ON weekly_setting.key = 'source_catalog_full:' || source.id
WHERE source.enabled = 1 AND source.source_type = 'openlist';
