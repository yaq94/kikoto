package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
)

type catalogSidecarEvidence struct {
	VoiceActors []string `json:"voiceActors"`
	Circles     []string `json:"circles"`
	Title       string   `json:"title,omitempty"`
}

// loadWorkMatchSidecar reads the stored credit evidence for one catalog card.
// It returns an empty value rather than failing the decision when a card has
// no sidecar: credits are optional evidence, not a prerequisite.
func loadWorkMatchSidecar(ctx context.Context, tx *sql.Tx, id int64) catalogSidecarEvidence {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT sidecar_json FROM source_catalog_entry WHERE id=?`, id).Scan(&raw); err != nil || raw == "" {
		return catalogSidecarEvidence{}
	}
	var cache catalogSidecarCache
	if json.Unmarshal([]byte(raw), &cache) != nil {
		return catalogSidecarEvidence{}
	}
	evidence := catalogSidecarEvidence{}
	for _, sidecar := range cache.Sidecars {
		evidence.VoiceActors = append(evidence.VoiceActors, sidecar.VoiceActors...)
		evidence.Circles = append(evidence.Circles, sidecar.Circles...)
		if evidence.Title == "" {
			evidence.Title = sidecar.Title
		}
	}
	evidence.VoiceActors = uniqueSortedCatalogNames(evidence.VoiceActors)
	evidence.Circles = uniqueSortedCatalogNames(evidence.Circles)
	return evidence
}

func uniqueSortedCatalogNames(names []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		key := strings.ToLower(trimmed)
		if trimmed == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, trimmed)
	}
	sort.Strings(result)
	return result
}

func equalCatalogSidecarNames(left, right []string) bool {
	left, right = uniqueSortedCatalogNames(left), uniqueSortedCatalogNames(right)
	if len(left) == 0 || len(left) != len(right) {
		return false
	}
	for index := range left {
		if !strings.EqualFold(left[index], right[index]) {
			return false
		}
	}
	return true
}

// sidecarVoiceIDs maps names to stable synthetic ids for the shared evidence
// comparison. The ids never enter storage; they only let a sidecar name set
// participate in the same equality rule as person rows.
func sidecarVoiceIDs(names []string) []int64 {
	names = uniqueSortedCatalogNames(names)
	ids := make([]int64, 0, len(names))
	for _, name := range names {
		ids = append(ids, catalogNameID(name))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// catalogNameID is a stable, collision-resistant mapping from a displayed
// credit to a comparison id. It is deliberately not a database identity.
func catalogNameID(name string) int64 {
	var hash uint64 = 1469598103934665603
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		hash ^= uint64(r)
		hash *= 1099511628211
	}
	return int64(hash & 0x7fffffffffffffff)
}

// catalogCreditNames loads readable credit names for one work. The caller
// bounds the result; names are compared case-insensitively.
func catalogCreditNames(ctx context.Context, tx *sql.Tx, query string, work int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, work)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(names) > 100 {
		return nil, errAssociationLimit
	}
	return uniqueSortedCatalogNames(names), nil
}
