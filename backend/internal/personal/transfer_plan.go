package personal

import (
	"context"
	"database/sql"
	"errors"
	"sort"
)

func buildImportPlan(ctx context.Context, tx *sql.Tx, user int64, b Backup) (importPlan, error) {
	p := importPlan{
		preview:          ImportPreview{Works: len(b.Works), Playlists: len(b.Playlists), Tags: len(b.Tags), MissingCodes: []string{}},
		works:            map[string]int64{},
		matches:          map[string]identityMatch{},
		reviewConflicts:  map[int64]bool{},
		progressConflict: map[int64]bool{},
		statusConflict:   map[int64]bool{},
		media:            map[string]int64{},
		blocked:          map[string]bool{},
		identities:       map[string]int64{},
		reviews:          map[int64]TransferReview{},
		parents:          map[string]transferParent{},
		seriesRefs:       map[string]string{},
		seriesTitles:     map[string]string{},
		parentClaims:     map[string]string{},
		newParents:       map[string]bool{},
	}
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
		if m.problem == "" {
			m.problem, err = transferIdentityKindProblem(ctx, tx, w, m)
			if err != nil {
				return p, err
			}
		}
		p.matches[w.key()] = m
		p.works[w.key()] = m.work
	}
	// Reserve the exact references declared by a top-level series. An episode's
	// parent may name one of these while both rows are still being planned.
	for i := range b.Works {
		w := &b.Works[i]
		if w.Identity == nil || w.Identity.Kind != "series" {
			continue
		}
		key := w.key()
		p.seriesTitles[key] = w.Identity.Title
		if m := p.matches[key]; m.work != 0 && m.series == 0 {
			var seriesID int64
			if err := tx.QueryRowContext(ctx, `SELECT id FROM catalog_series WHERE work_id=?`, m.work).Scan(&seriesID); err == nil {
				m.series = seriesID
				p.matches[key] = m
			} else if !errors.Is(err, sql.ErrNoRows) {
				return p, err
			}
		}
		for _, ref := range append([]string{w.Identity.Ref}, w.Identity.References...) {
			p.seriesRefs[ref] = key
		}
	}
	for i := range b.Works {
		w := &b.Works[i]
		if w.Identity == nil {
			continue
		}
		key := w.key()
		m := p.matches[key]
		if m.problem != "" {
			if err := p.block(key, w.label(), m.problem); err != nil {
				return p, err
			}
			continue
		}
		if w.Identity.Parent != nil && w.Identity.Kind != "episode" {
			if err := p.block(key, w.label(), "parent_conflict"); err != nil {
				return p, err
			}
			continue
		}
		if w.Identity.Kind == "episode" {
			parent, problem, err := planEpisodeParent(ctx, tx, &p, w, m)
			if err != nil {
				return p, err
			}
			if problem != "" {
				if err := p.block(key, w.label(), problem); err != nil {
					return p, err
				}
				continue
			}
			p.parents[key] = parent
			if w.Identity.Parent != nil && parent.workID == 0 && parent.key == "" {
				p.requiresRegistration = true
				parentKey := "new:" + w.Identity.Parent.Ref
				if !p.newParents[parentKey] {
					p.newParents[parentKey] = true
					p.preview.NewWorks++
				}
			}
			if b.Version == 2 && w.Identity.Parent == nil {
				if err := p.preview.addIssue(ImportIssue{Ref: key, Title: w.label(), Reason: "relationships_not_imported"}); err != nil {
					return p, err
				}
			}
		}
		if m.work == 0 {
			p.preview.NewWorks++
			p.requiresRegistration = true
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
		var current TransferReview
		err = tx.QueryRowContext(ctx, `SELECT s.rating,COALESCE(s.note,'') FROM work w LEFT JOIN user_work_state s ON s.work_id=w.id AND s.user_id=? WHERE w.id=?`, user, identity).Scan(&current.Rating, &current.Note)
		if err != nil {
			return p, err
		}
		reviewConflict := w.Rating != nil && current.Rating != nil && *w.Rating != *current.Rating ||
			w.Note != "" && current.Note != "" && w.Note != current.Note
		var status string
		err = tx.QueryRowContext(ctx, `SELECT listening_status FROM user_work_state WHERE user_id=? AND work_id=?`, user, id).Scan(&status)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return p, err
		}
		incomingStatus := w.ListeningStatus != "" && w.ListeningStatus != "none"
		statusConflict := incomingStatus && status != "" && status != "none" && status != w.ListeningStatus
		var progressExists bool
		if w.Progress != nil {
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_work_playback_cursor WHERE user_id=? AND work_id=?)
					OR EXISTS(SELECT 1 FROM personal_transfer_progress WHERE user_id=? AND work_id=?)`,
				user, id, user, id).Scan(&progressExists); err != nil {
				return p, err
			}
		}
		p.reviewConflicts[identity] = p.reviewConflicts[identity] || reviewConflict
		p.statusConflict[id] = statusConflict
		p.progressConflict[id] = progressExists
		if reviewConflict {
			conflictingGroups[identity] = true
			if err := p.preview.addIssue(ImportIssue{Ref: w.key(), Title: w.label(), Reason: "existing_record", Incoming: &TransferReview{Rating: w.Rating, Note: w.Note}, Existing: &current}); err != nil {
				return p, err
			}
		}
		if statusConflict {
			conflictingGroups[identity] = true
			if err := p.preview.addIssue(ImportIssue{Ref: w.key(), Title: w.label(), Reason: "existing_status"}); err != nil {
				return p, err
			}
		}
		if progressExists {
			conflictingGroups[identity] = true
			if err := p.preview.addIssue(ImportIssue{Ref: w.key(), Title: w.label(), Reason: "existing_progress"}); err != nil {
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

func (p *importPlan) block(key, title, reason string) error {
	p.blocked[key] = true
	return p.preview.addIssue(ImportIssue{Ref: key, Title: title, Reason: reason})
}

func intString(value int64) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}

// planEpisodeParent resolves the episode's single series parent. An episode
// that names a parent must resolve it unambiguously; a missing or invalid
// parent blocks only that entry instead of importing it as an independent
// work. An episode without a parent field keeps the legacy v2 behavior.
func planEpisodeParent(ctx context.Context, tx *sql.Tx, p *importPlan, w *BackupWork, m identityMatch) (transferParent, string, error) {
	if w.Identity.Parent == nil {
		return transferParent{}, "", nil
	}
	parent, problem, err := resolvePlannedParent(ctx, tx, p, w.Identity.Parent)
	if err != nil || problem != "" {
		return parent, problem, err
	}
	if parent.key != "" {
		top := p.matches[parent.key]
		if top.problem != "" {
			return parent, "parent_conflict", nil
		}
		if top.work != 0 {
			if top.work == m.work {
				return parent, "parent_conflict", nil
			}
			if parent.workID != 0 && parent.workID != top.work {
				return parent, "parent_conflict", nil
			}
			parent.workID = top.work
		}
		if top.series != 0 {
			if parent.seriesID != 0 && parent.seriesID != top.series {
				return parent, "parent_conflict", nil
			}
			parent.seriesID = top.series
		}
		parent.title = p.seriesTitles[parent.key]
	}
	if m.work != 0 && parent.workID == m.work {
		return parent, "parent_conflict", nil
	}
	state, err := loadEpisodeParentState(ctx, tx, m.work, m.catalog)
	if err != nil {
		return parent, "", err
	}
	if state.problem != "" {
		return parent, state.problem, nil
	}
	if state.parentWork != 0 {
		if parent.workID != 0 && parent.workID != state.parentWork {
			return parent, "parent_conflict", nil
		}
		// An unknown parent reference would create a second series work. A
		// stored different parent is shared state; never replace it with a new
		// identity from this backup.
		if parent.workID == 0 && parent.seriesID == 0 && parent.key == "" {
			return parent, "parent_conflict", nil
		}
		if parent.key != "" && p.matches[parent.key].work != state.parentWork {
			return parent, "parent_conflict", nil
		}
	}
	for _, seriesID := range state.series {
		if parent.seriesID != 0 && parent.seriesID != seriesID {
			return parent, "parent_conflict", nil
		}
	}
	if m.work != 0 && parent.workID != 0 {
		problem, err := transferParentCycleProblem(ctx, tx, m.work, parent.workID)
		if err != nil || problem != "" {
			return parent, problem, err
		}
	}
	if parent.workID == 0 && parent.seriesID != 0 && m.work != 0 {
		// The series work may not exist yet. If it does, its own parent chain
		// still decides whether this relation is nested or cyclic.
		var seriesWork sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT work_id FROM catalog_series WHERE id=?`, parent.seriesID).Scan(&seriesWork); err != nil {
			return parent, "", err
		}
		if seriesWork.Valid {
			parent.workID = seriesWork.Int64
			problem, err := transferParentCycleProblem(ctx, tx, m.work, parent.workID)
			if err != nil || problem != "" {
				return parent, problem, err
			}
			var parentOfParent sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT parent_work_id FROM work WHERE id=?`, parent.workID).Scan(&parentOfParent); err != nil {
				return parent, "", err
			}
			if parentOfParent.Valid {
				return parent, "parent_conflict", nil
			}
		}
	}
	if parent.nested {
		return parent, "parent_conflict", nil
	}
	return parent, "", nil
}

// resolvePlannedParent matches retained series references and top-level series
// works in the same document. Titles, paths and the episode's own membership
// are never used to guess a parent.
func resolvePlannedParent(ctx context.Context, tx *sql.Tx, p *importPlan, parent *BackupIdentity) (transferParent, string, error) {
	if !validBackupParent(parent) {
		return transferParent{}, "parent_conflict", nil
	}
	if title, ok := p.parentClaims[parent.Ref]; ok && title != parent.Title {
		return transferParent{}, "parent_conflict", nil
	}
	p.parentClaims[parent.Ref] = parent.Title
	plan, problem, err := resolveParentPlan(ctx, tx, parent)
	if err != nil || problem != "" {
		return plan, problem, err
	}
	keys := []string{}
	if key := p.seriesRefs[parent.Ref]; key != "" {
		keys = append(keys, key)
	}
	for _, ref := range parent.References {
		if key := p.seriesRefs[ref]; key != "" {
			keys = append(keys, key)
		}
	}
	for _, key := range keys {
		if p.blocked[key] || p.seriesTitles[key] != parent.Title {
			return plan, "parent_conflict", nil
		}
		if plan.key != "" && plan.key != key {
			return plan, "parent_conflict", nil
		}
		plan.key = key
		if top := p.matches[key]; top.series != 0 {
			if plan.seriesID != 0 && plan.seriesID != top.series {
				return plan, "parent_conflict", nil
			}
			plan.seriesID = top.series
		}
	}
	if plan.key != "" {
		plan.title = p.seriesTitles[plan.key]
	}
	return plan, "", nil
}
