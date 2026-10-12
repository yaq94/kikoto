package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/yexca/kikoto/backend/internal/testfixture"
)

func TestCircleWorksPageFollowsTheRequestedOrder(t *testing.T) {
	db := openMigratedTestDB(t)
	ctx := context.Background()
	var providerID int64
	if err := db.QueryRow("SELECT id FROM metadata_provider WHERE code = 'dlsite'").Scan(&providerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO party (id, display_name) VALUES (1, 'Example Circle')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO party_external_id (party_id, provider_id, id_type, external_id, is_primary)
		VALUES (1, ?, 'maker_id', 'RG00000001', 1)
	`, providerID); err != nil {
		t.Fatal(err)
	}
	first, second, third, catalogOnly := testfixture.WorkCodeAt(0), testfixture.WorkCodeAt(1), testfixture.WorkCodeAt(2), testfixture.WorkCodeAt(3)
	works := []struct {
		id      int64
		code    string
		title   string
		release string
		rating  float64
		sales   int64
	}{
		{1, first, "Beta", "2024-03-01", 4.1, 300},
		{2, second, "alpha", "2024-01-01", 4.9, 100},
		{3, third, "Gamma", "2024-02-01", 4.5, 200},
	}
	for _, work := range works {
		if _, err := db.Exec(`
			INSERT INTO work (id, primary_code, title, release_date, rating_average, sales_count)
			VALUES (?, ?, ?, ?, ?, ?)
		`, work.id, work.code, work.title, work.release, work.rating, work.sales); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			INSERT INTO party_catalog_item (party_id, provider_id, primary_code, title, release_date, catalog_status, dlsite_available)
			VALUES (1, ?, ?, ?, ?, 'imported', 1)
		`, providerID, work.code, work.title, work.release); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			INSERT INTO work_party (work_id, party_id, role, provider_id, source)
			VALUES (?, 1, 'circle', ?, 'dlsite_snapshot')
		`, work.id, providerID); err != nil {
			t.Fatal(err)
		}
	}
	// A catalog row without a work has no rating or sales count.
	if _, err := db.Exec(`
		INSERT INTO party_catalog_item (party_id, provider_id, primary_code, title, release_date, catalog_status, dlsite_available)
		VALUES (1, ?, ?, 'Delta', '2024-04-01', 'catalog_only', 1)
	`, providerID, catalogOnly); err != nil {
		t.Fatal(err)
	}

	server := &Server{db: db}
	for _, test := range []struct {
		name      string
		sort      string
		direction string
		want      []string
	}{
		{"default", "", "", []string{catalogOnly, first, third, second}},
		{"release ascending", "release", "asc", []string{second, third, first, catalogOnly}},
		{"code ascending", "code", "asc", []string{first, second, third, catalogOnly}},
		{"title ascending ignores case", "title", "asc", []string{second, first, catalogOnly, third}},
		{"rating descending keeps unrated last", "rating", "desc", []string{second, third, first, catalogOnly}},
		{"rating ascending keeps unrated last", "rating", "asc", []string{first, third, second, catalogOnly}},
		{"sales descending keeps unsold last", "sales", "desc", []string{first, third, second, catalogOnly}},
		{"unknown order falls back to release", "price", "sideways", []string{catalogOnly, first, third, second}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/api/circles/RG00000001?page=1&pageSize=24&sort="+test.sort+"&direction="+test.direction, nil)
			page, total, err := server.loadCircleWorksPage(ctx, 0, 1, parseCircleWorksQuery(request))
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(page))
			for _, work := range page {
				got = append(got, work.PrimaryCode)
			}
			if total != len(test.want) || len(got) != len(test.want) {
				t.Fatalf("page = %v (total %d), want %v", got, total, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("order = %v, want %v", got, test.want)
				}
			}
		})
	}

	// Paging walks the same order instead of the enrichment query's own.
	request := httptest.NewRequest("GET", "/api/circles/RG00000001?page=2&pageSize=2&sort=sales&direction=desc", nil)
	page, _, err := server.loadCircleWorksPage(ctx, 0, 1, parseCircleWorksQuery(request))
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].PrimaryCode != second || page[1].PrimaryCode != catalogOnly {
		t.Fatalf("second sales page = %+v, want the lowest seller then the catalog-only work", page)
	}
}
