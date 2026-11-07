package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

func TestBenchmarkQueryFailureIsNotAZeroLatencySuccess(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, name := range []string{"warmup", "measurement"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			result := timeQuery(3, func() (int, error) {
				calls++
				if name == "measurement" && calls < 3 {
					return countQuery(db, "SELECT 1")
				}
				return countQuery(db, "SELECT count(*) FROM synthetic_missing_table")
			})
			// A later successful EXPLAIN must not erase a failed measurement.
			result.Plan = explainPlan(db, "SELECT 1")
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), "synthetic_missing_table") || strings.Contains(string(encoded), `"p95Ms":0`) {
				t.Fatalf("failed query advertised success: %s", encoded)
			}
			report := sizeReport{ExactCode: result, Languages: []languageReport{{Language: "en", FTS: timeQuery(1, func() (int, error) { return countQuery(db, "SELECT count(*) FROM another_missing_table") })}}}
			if err := report.queryError(); err == nil || !strings.Contains(err.Error(), "synthetic_missing_table") || !strings.Contains(err.Error(), "another_missing_table") {
				t.Fatalf("query failures were lost: %v", err)
			}
		})
	}
}
