package httpapi

import (
	"regexp"
	"testing"
)

// catalogFileAliases are the aliases the catalog queries give
// source_catalog_file. A plan line "SCAN <alias>" with nothing after it is a
// full table scan; "SCAN <alias> USING ..." is an index walk and is allowed.
var catalogFileTableScan = regexp.MustCompile(`(?m)^\s*SCAN (source_catalog_file|ff|af|anf|aaf|aqf|sf|f)\s*$`)

// assertNoCatalogFileTableScan fails when the plan reads source_catalog_file
// without an index. A bare substring check for "SCAN source_catalog_file" misses
// the alias form the planner actually prints, so the check is anchored to a
// whole plan line instead.
func assertNoCatalogFileTableScan(t *testing.T, plan string) {
	t.Helper()
	if match := catalogFileTableScan.FindString(plan); match != "" {
		t.Fatalf("plan reads source_catalog_file without an index (%q):\n%s", match, plan)
	}
}
