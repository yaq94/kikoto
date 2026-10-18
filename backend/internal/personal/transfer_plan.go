package personal

import (
	"context"
	"database/sql"
	"sort"
)

func buildImportPlan(ctx context.Context, tx *sql.Tx, user int64, b Backup) (importPlan, error) {
	p := importPlan{preview: ImportPreview{Works: len(b.Works), Playlists: len(b.Playlists), Tags: len(b.Tags), MissingCodes: []string{}}, works: map[string]int64{}, conflicts: map[int64]bool{}, media: map[string]int64{}, blocked: map[string]bool{}, identities: map[string]int64{}, reviews: map[int64]TransferReview{}}
	codes := map[string]bool{}
	for _, w := range b.Works {
		if w.Identity == nil {
			codes[w.PrimaryCode] = true
			continue
		}
		m, err := resolveTransferIdentity(ctx, tx, w.Identity, w.PrimaryCode)
		if err != nil {
			return p, err
		}
		p.works[w.key()] = m.work
		if m.problem != "" {
			p.blocked[w.key()] = true
			if err := p.preview.addIssue(ImportIssue{Ref: w.key(), Title: w.label(), Reason: m.problem}); err != nil {
				return p, err
			}
			continue
		}
		if m.work == 0 {
			p.preview.NewWorks++
		}
		for _, ref := range append([]string{w.Identity.Ref}, w.Identity.References...) {
			if ref[:2] == "w:" {
				continue
			}
			var found bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM personal_transfer_ref WHERE ref=?)`, ref).Scan(&found); err != nil {
				return p, err
			}
			if !found {
				if err := p.preview.addIssue(ImportIssue{Ref: ref, Title: w.label(), Reason: "source_reference_retained"}); err != nil {
					return p, err
				}
			}
		}
		if m.work == 0 && w.Identity.Kind == "episode" {
			if err := p.preview.addIssue(ImportIssue{Ref: w.key(), Title: w.label(), Reason: "relationships_not_imported"}); err != nil {
				return p, err
			}
		}
	}
	for _, l := range b.Playlists {
		for _, i := range l.Items {
			if i.WorkRef == "" {
				codes[i.PrimaryCode] = true
			}
		}
	}
	for code := range codes {
		id, err := resolveCode(ctx, tx, code)
		if err != nil {
			return p, err
		}
		p.works[code] = id
		if id == 0 {
			p.preview.MissingCodes = append(p.preview.MissingCodes, code)
		}
	}
	sort.Strings(p.preview.MissingCodes)
	seen := map[int64]bool{}
	conflictingGroups := map[int64]bool{}
	inputConflicts := map[int64]bool{}
	for _, w := range b.Works {
		id := p.works[w.key()]
		if id == 0 || p.blocked[w.key()] {
			continue
		}
		if seen[id] {
			return p, ErrConflict
		}
		seen[id] = true
		identity, err := sharedIdentity(ctx, tx, id)
		if err != nil {
			return p, err
		}
		p.identities[w.key()] = identity
		p.preview.MatchedWorks++
		var exists bool
		// A shared identity means several work ids expose one personal record.
		// Any member's saved data is already this user's record, so an import
		// with keep must not overwrite it through another member id.
		// One list keeps every personal table in scope: a table added to the
		// export must also be able to raise a conflict here.
		err = tx.QueryRowContext(ctx, `WITH personal_work AS (
				SELECT work_id FROM user_work_state WHERE user_id=?
				UNION SELECT work_id FROM user_work_tag WHERE user_id=?
				UNION SELECT work_id FROM user_work_playback_cursor WHERE user_id=?
				UNION SELECT work_id FROM user_listening_session WHERE user_id=?
				UNION SELECT work_id FROM user_listening_import WHERE user_id=?
			)
			SELECT EXISTS(SELECT 1 FROM personal_work p WHERE p.work_id=?
				OR EXISTS(SELECT 1 FROM shared_identity_member m WHERE m.work_id=p.work_id AND m.identity_work_id=?))`,
			user, user, user, user, user, id, identity).Scan(&exists)
		if err != nil {
			return p, err
		}
		p.conflicts[id] = exists
		if exists {
			conflictingGroups[identity] = true
			var current TransferReview
			if err = tx.QueryRowContext(ctx, `SELECT s.rating,COALESCE(s.note,'') FROM work w LEFT JOIN user_work_state s ON s.work_id=w.id AND s.user_id=? WHERE w.id=?`, user, identity).Scan(&current.Rating, &current.Note); err != nil {
				return p, err
			}
			if err := p.preview.addIssue(ImportIssue{Ref: w.key(), Title: w.label(), Reason: "existing_record", Incoming: &TransferReview{Rating: w.Rating, Note: w.Note}, Existing: &current}); err != nil {
				return p, err
			}
		}
		if review, ok := p.reviews[identity]; ok {
			if review.Rating != nil && w.Rating != nil && *review.Rating != *w.Rating || review.Note != "" && w.Note != "" && review.Note != w.Note {
				inputConflicts[identity] = true
				conflictingGroups[identity] = true
			}
			if review.Rating == nil {
				review.Rating = w.Rating
			}
			if review.Note == "" {
				review.Note = w.Note
			}
			p.reviews[identity] = review
		} else {
			p.reviews[identity] = TransferReview{Rating: w.Rating, Note: w.Note}
		}
		if w.Progress != nil {
			p.media[w.key()], err = matchProgress(ctx, tx, id, w.Progress)
			if err != nil {
				return p, err
			}
			if p.media[w.key()] == 0 {
				p.preview.UnmatchedProgress++
			}
		}
	}
	for _, w := range b.Works {
		if inputConflicts[p.identities[w.key()]] {
			p.blocked[w.key()] = true
			if err := p.preview.addIssue(ImportIssue{Ref: w.key(), Title: w.label(), Reason: "incoming_review_conflict", Incoming: &TransferReview{Rating: w.Rating, Note: w.Note}}); err != nil {
				return p, err
			}
		}
		if p.works[w.key()] == 0 && w.Identity != nil && w.Progress != nil {
			p.preview.UnmatchedProgress++
		}
	}
	p.preview.Conflicts = len(conflictingGroups)
	return p, nil
}
