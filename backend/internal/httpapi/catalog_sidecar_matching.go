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
	left, right = foldCatalogNames(left), foldCatalogNames(right)
	if len(left) == 0 || len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// foldCatalogNames compares the same spelling under one case rule. Sorting the
// displayed strings first would let "Alice/bob" and "alice/Bob" compare in a
// different order than their folded forms.
func foldCatalogNames(names []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, name := range names {
		folded := strings.ToLower(strings.TrimSpace(name))
		if folded == "" || seen[folded] {
			continue
		}
		seen[folded] = true
		result = append(result, folded)
	}
	sort.Strings(result)
	return result
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
