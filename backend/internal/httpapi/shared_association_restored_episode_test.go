package httpapi

import (
	"fmt"
	"testing"

	"github.com/yexca/kikoto/backend/internal/personal"
)

func restoredEpisodeBackup(parent bool, rightKind string) personal.Backup {
	rating := 2
	identity := &personal.BackupIdentity{Ref: parentTestRef("w", '1'), Kind: "episode", Title: "Example Work"}
	if parent {
		identity.Parent = &personal.BackupIdentity{Ref: parentTestRef("s", '2'), Kind: "series", Title: "Example Series"}
	}
	right := &personal.BackupIdentity{Ref: parentTestRef("w", '3'), Kind: rightKind, Title: "Example Work"}
	if rightKind == "episode" {
		right.Parent = identity.Parent
	}
	return personal.Backup{Format: "kikoto-user-data", Version: 2, Works: []personal.BackupWork{
		{Identity: identity, ListeningStatus: "none", Rating: &rating, Note: "Example episode note"},
		{Identity: right, ListeningStatus: "none"},
	}}
}

func TestSharedAssociationRestoredEpisodeRejectsCrossLevel(t *testing.T) {
	for _, parent := range []bool{true, false} {
		for _, automatic := range []bool{false, true} {
			t.Run(fmt.Sprintf("parent_%t/automatic_%t", parent, automatic), func(t *testing.T) {
				root := t.TempDir()
				db, host, admin, _, closeApp := openTransferApplication(t, root, true)
				body := mustJSON(map[string]any{"format": "kikoto", "conflict": "keep", "data": restoredEpisodeBackup(parent, "work")})
				workReferenceRequest(t, host, admin, "POST", "/api/user-data/import", body, 200, nil)
				var episode, standalone int64
				if err := db.QueryRow(`SELECT work_id FROM personal_transfer_ref WHERE ref=?`, parentTestRef("w", '1')).Scan(&episode); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT work_id FROM personal_transfer_ref WHERE ref=?`, parentTestRef("w", '3')).Scan(&standalone); err != nil {
					t.Fatal(err)
				}
				// Equal public metadata is deliberately sufficient for the ordinary
				// work matching rule, but cannot overrule evaluation level.
				metadataReviewExec(t, db, `UPDATE work SET release_date='2020-01-02';
				INSERT INTO person(id,display_name) VALUES(1,'Example Voice');
				INSERT INTO work_credit(work_id,person_id,role) SELECT id,1,'voice_actor' FROM work;`)
				for boot := 0; boot < 2; boot++ {
					if boot == 1 {
						closeApp()
						_, host, admin, _, closeApp = openTransferApplication(t, root, false)
					}
					if automatic {
						result := translationRequest(t, host, admin, "POST", "/api/work-matching", mustJSON(map[string]any{"kind": "work", "id": episode}), 200)
						if result["status"] != "review" || result["rule"] != "conflicting_evidence" {
							t.Fatalf("cross-level automatic result: %+v", result)
						}
					} else {
						payload := mustJSON(map[string]any{"left": map[string]any{"kind": "work", "id": episode}, "right": map[string]any{"kind": "work", "id": standalone}})
						workReferenceRequest(t, host, admin, "POST", "/api/shared-associations/previews", payload, 409, nil)
					}
					current := translationRequest(t, host, admin, "GET", fmt.Sprintf("/api/works/%d?includeMedia=false", standalone), "", 200)
					assertReview(t, current, nil, "")
					current = translationRequest(t, host, admin, "GET", fmt.Sprintf("/api/works/%d?includeMedia=false", episode), "", 200)
					assertReview(t, current, float64(2), "Example episode note")
				}
			})
		}
	}
}

func TestSharedAssociationRestoredEpisodesLinkAndSplitAfterRestart(t *testing.T) {
	root := t.TempDir()
	db, host, admin, _, closeApp := openTransferApplication(t, root, true)
	body := mustJSON(map[string]any{"format": "kikoto", "conflict": "keep", "data": restoredEpisodeBackup(true, "episode")})
	workReferenceRequest(t, host, admin, "POST", "/api/user-data/import", body, 200, nil)
	var left, right int64
	if err := db.QueryRow(`SELECT work_id FROM personal_transfer_ref WHERE ref=?`, parentTestRef("w", '1')).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT work_id FROM personal_transfer_ref WHERE ref=?`, parentTestRef("w", '3')).Scan(&right); err != nil {
		t.Fatal(err)
	}
	payload := mustJSON(map[string]any{"left": map[string]any{"kind": "work", "id": left}, "right": map[string]any{"kind": "work", "id": right}})
	preview := translationRequest(t, host, admin, "POST", "/api/shared-associations/previews", payload, 201)
	for _, side := range []string{"left", "right"} {
		if preview[side].(map[string]any)["level"] != "episode" {
			t.Fatalf("restored %s is not an episode: %+v", side, preview)
		}
	}
	url := fmt.Sprintf("/api/shared-associations/%.0f", preview["id"])
	started := translationRequest(t, host, admin, "POST", url+"/start", mustJSON(map[string]any{"version": preview["version"]}), 200)
	if started["status"] != "completed" {
		t.Fatalf("same-level association = %+v", started)
	}
	current := translationRequest(t, host, admin, "GET", fmt.Sprintf("/api/works/%d?includeMedia=false", right), "", 200)
	assertReview(t, current, float64(2), "Example episode note")
	split := translationRequest(t, host, admin, "POST", url+"/split-preview", `{}`, 201)
	closeApp()
	_, host, admin, _, _ = openTransferApplication(t, root, false)
	splitURL := fmt.Sprintf("/api/shared-splits/%.0f", split["id"])
	completed := translationRequest(t, host, admin, "POST", splitURL+"/start", mustJSON(map[string]any{"version": split["version"]}), 200)
	if completed["status"] != "completed" {
		t.Fatalf("restored episode split = %+v", completed)
	}
	current = translationRequest(t, host, admin, "GET", fmt.Sprintf("/api/works/%d?includeMedia=false", left), "", 200)
	assertReview(t, current, float64(2), "Example episode note")
	current = translationRequest(t, host, admin, "GET", fmt.Sprintf("/api/works/%d?includeMedia=false", right), "", 200)
	assertReview(t, current, nil, "")
	var backup personal.Backup
	workReferenceRequest(t, host, admin, "GET", "/api/user-data/export", "", 200, &backup)
	for _, work := range backup.Works {
		if work.Note == "Example episode note" {
			if work.Identity == nil || work.Identity.Kind != "episode" || work.Identity.Parent == nil || work.Identity.Parent.Ref != parentTestRef("s", '2') {
				t.Fatalf("split changed portable parent identity: %+v", work.Identity)
			}
			return
		}
	}
	t.Fatal("split lost the exported episode review")
}
