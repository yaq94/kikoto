package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

var errWorkMatchingBusy = errors.New("matching is busy")

type workMatchEvidence struct {
	Subject  associationSubject  `json:"subject"`
	Title    string              `json:"title"`
	Code     string              `json:"code"`
	Level    string              `json:"level"`
	Identity int64               `json:"identity"`
	Family   int64               `json:"family"`
	Voices   []int64             `json:"voices"`
	Circles  []int64             `json:"circles"`
	Release  string              `json:"release"`
	Language string              `json:"language"`
	Edition  string              `json:"edition"`
	WorkType string              `json:"workType"`
	Series   string              `json:"series"`
	Position int                 `json:"position"`
	Content  string              `json:"content"`
	Revision int64               `json:"revision"`
	Peers    []workMatchEvidence `json:"peers,omitempty"`
}

type workMatchCandidate struct {
	Subject    associationSubject `json:"subject"`
	Title      string             `json:"title"`
	Rule       string             `json:"rule"`
	Reasons    []string           `json:"reasons"`
	Confidence string             `json:"confidence"`
}

type workMatchResult struct {
	Revision       int64                `json:"-"`
	Status         string               `json:"status"`
	Rule           string               `json:"rule"`
	Candidates     []workMatchCandidate `json:"candidates"`
	CandidateCount int                  `json:"candidateCount"`
	OperationID    int64                `json:"operationId,omitempty"`
	Fingerprint    string               `json:"-"`
	Target         associationSubject   `json:"-"`
}

func matchingSubject(ctx context.Context, tx *sql.Tx, subject associationSubject) (associationSubject, error) {
	if subject.ID <= 0 {
		return subject, errAssociationInvalid
	}
	var work *int64
	switch subject.Kind {
	case "work":
		return subject, nil
	case "catalog":
		if err := tx.QueryRowContext(ctx, `SELECT work_id FROM source_catalog_entry WHERE id=? AND relocated_to_id IS NULL`, subject.ID).Scan(&work); err != nil {
			return subject, err
		}
	case "series":
		if err := tx.QueryRowContext(ctx, `SELECT work_id FROM catalog_series WHERE id=?`, subject.ID).Scan(&work); err != nil {
			return subject, err
		}
	default:
		return subject, errAssociationInvalid
	}
	if work != nil {
		return associationSubject{Kind: "work", ID: *work}, nil
	}
	return subject, nil
}

func loadWorkMatchEvidence(ctx context.Context, tx *sql.Tx, subject associationSubject) (workMatchEvidence, error) {
	return loadWorkMatchMember(ctx, tx, subject, true)
}

func loadWorkMatchMember(ctx context.Context, tx *sql.Tx, subject associationSubject, peers bool) (workMatchEvidence, error) {
	o, err := loadAssociationObject(ctx, tx, subject)
	e := workMatchEvidence{Subject: subject, Voices: []int64{}, Circles: []int64{}}
	if err != nil {
		return e, err
	}
	e.Title, e.Code, e.Level, e.Identity, e.Revision = o.Title, o.Code, o.Level, o.IdentityWorkID, o.Revision
	if len(e.Title) > 8192 {
		return e, errAssociationLimit
	}
	if o.WorkID != nil {
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(w.release_date,''),w.work_type,COALESCE(e.logical_work_id,0),COALESCE(e.metadata_language,''),COALESCE(e.edition_label,'') FROM work w LEFT JOIN work_edition e ON e.work_id=w.id WHERE w.id=?`, *o.WorkID).Scan(&e.Release, &e.WorkType, &e.Family, &e.Language, &e.Edition)
		if err != nil {
			return e, err
		}
		if len(e.Release)+len(e.WorkType)+len(e.Language)+len(e.Edition) > 4096 {
			return e, errAssociationLimit
		}
		for _, item := range []struct {
			query string
			dest  *[]int64
		}{
			{`SELECT DISTINCT person_id FROM work_credit WHERE work_id=? AND role='voice_actor' ORDER BY person_id LIMIT 101`, &e.Voices},
			{`SELECT DISTINCT party_id FROM work_party WHERE work_id=? AND role='circle' ORDER BY party_id LIMIT 101`, &e.Circles},
		} {
			rows, queryErr := tx.QueryContext(ctx, item.query, *o.WorkID)
			if queryErr != nil {
				return e, queryErr
			}
			for rows.Next() {
				var id int64
				if err = rows.Scan(&id); err != nil {
					break
				}
				*item.dest = append(*item.dest, id)
			}
			if err == nil {
				err = rows.Err()
			}
			_ = rows.Close()
			if err != nil {
				return e, err
			}
			if len(*item.dest) > 100 {
				return e, errAssociationLimit
			}
		}
	}
	if subject.Kind == "catalog" {
		if err = tx.QueryRowContext(ctx, `SELECT content_fingerprint FROM source_catalog_entry WHERE id=?`, subject.ID).Scan(&e.Content); err != nil {
			return e, err
		}
		if decoded, decodeErr := hex.DecodeString(e.Content); decodeErr != nil || len(decoded) != 32 {
			e.Content = ""
		}
	}
	if peers && o.WorkID != nil {
		for _, member := range o.Members {
			if member == *o.WorkID {
				continue
			}
			peer, readErr := loadWorkMatchMember(ctx, tx, associationSubject{Kind: "work", ID: member}, false)
			if readErr != nil {
				return e, readErr
			}
			e.Peers = append(e.Peers, peer)
		}
	}
	if e.Level == "episode" {
		err = tx.QueryRowContext(ctx, `SELECT s.title,m.position FROM catalog_series_member m JOIN catalog_series s ON s.id=m.series_id JOIN source_catalog_entry e ON e.id=m.catalog_entry_id WHERE (?='catalog' AND e.id=?) OR (?='work' AND e.work_id=?) ORDER BY e.id LIMIT 1`, subject.Kind, subject.ID, subject.Kind, subject.ID).Scan(&e.Series, &e.Position)
		if err != nil {
			return e, err
		}
	}
	return e, nil
}

func matchWorkEvidence(a, b workMatchEvidence) workMatchCandidate {
	c := workMatchCandidate{Subject: b.Subject, Title: b.Title, Rule: "insufficient_evidence", Reasons: []string{}, Confidence: "review"}
	if len([]rune(c.Title)) > 512 {
		c.Title = string([]rune(c.Title)[:512]) + "…"
	}
	for _, peer := range a.Peers {
		other := b
		other.Peers = nil
		if matchWorkEvidence(peer, other).Rule == "conflicting_evidence" {
			c.Rule = "conflicting_evidence"
			c.Reasons = []string{"group_evidence"}
			return c
		}
	}
	for _, peer := range b.Peers {
		other := a
		other.Peers = nil
		if matchWorkEvidence(other, peer).Rule == "conflicting_evidence" {
			c.Rule = "conflicting_evidence"
			c.Reasons = []string{"group_evidence"}
			return c
		}
	}
	conflict := func(reason string) { c.Reasons = append(c.Reasons, reason) }
	if a.Level != b.Level {
		conflict("evaluation_level")
	}
	if a.Level == "episode" && (a.Position <= 0 || b.Position <= 0 || a.Position != b.Position || a.Series != b.Series) {
		conflict("episode_identity")
	}
	// A declared edition family is reliable identity evidence, not a timeline mapping.
	related := a.Family > 0 && a.Family == b.Family
	if a.Code != "" && b.Code != "" && a.Code != b.Code && !related {
		conflict("different_codes")
	}
	if !related {
		for _, pair := range []struct{ name, left, right string }{{"release_date", a.Release, b.Release}, {"language", a.Language, b.Language}, {"edition", a.Edition, b.Edition}, {"work_type", a.WorkType, b.WorkType}} {
			if pair.left != "" && pair.right != "" && pair.left != pair.right {
				conflict(pair.name)
			}
		}
		if len(a.Voices) > 0 && len(b.Voices) > 0 && !slices.Equal(a.Voices, b.Voices) {
			conflict("voice_actors")
		}
		if len(a.Circles) > 0 && len(b.Circles) > 0 && !slices.Equal(a.Circles, b.Circles) {
			conflict("circles")
		}
	}
	if len(c.Reasons) > 0 {
		c.Rule = "conflicting_evidence"
		return c
	}
	if related {
		c.Rule = "declared_relationship"
		c.Confidence = "reliable"
		c.Reasons = []string{"declared_relationship"}
		return c
	}
	if a.Code != "" && a.Code == b.Code {
		c.Rule = "product_code"
		c.Confidence = "reliable"
		c.Reasons = []string{"product_code"}
		return c
	}
	if strings.TrimSpace(a.Title) == "" || !strings.EqualFold(strings.TrimSpace(a.Title), strings.TrimSpace(b.Title)) {
		return c
	}
	c.Reasons = append(c.Reasons, "original_title")
	if a.Content != "" && a.Content == b.Content {
		c.Rule = "title_content_hash"
		c.Confidence = "high"
		c.Reasons = append(c.Reasons, "complete_audio_hashes")
		return c
	}
	if len(a.Voices) == 0 || !slices.Equal(a.Voices, b.Voices) {
		return c
	}
	c.Reasons = append(c.Reasons, "voice_actors")
	if len(a.Circles) > 0 && slices.Equal(a.Circles, b.Circles) {
		c.Rule = "title_voice_circle"
		c.Confidence = "high"
		c.Reasons = append(c.Reasons, "circles")
	} else if a.Release != "" && a.Release == b.Release {
		c.Rule = "title_voice_release"
		c.Confidence = "high"
		c.Reasons = append(c.Reasons, "release_date")
	}
	return c
}

func evaluateWorkMatchTx(ctx context.Context, tx *sql.Tx, subject associationSubject) (workMatchResult, error) {
	result := workMatchResult{Status: "no_match", Rule: "insufficient_evidence", Candidates: []workMatchCandidate{}}
	a, err := loadWorkMatchEvidence(ctx, tx, subject)
	if err != nil {
		return result, err
	}
	if subject.Kind == "catalog" && normalizeWorkCode(a.Code) != "" {
		var knownID int64
		err = tx.QueryRowContext(ctx, `SELECT w.id FROM work w WHERE w.primary_code=?
			UNION ALL SELECT COALESCE(l.canonical_work_id,alias.source_work_id) FROM work_code_alias alias
				LEFT JOIN logical_work l ON l.id=alias.logical_work_id
				WHERE alias.primary_code=? AND COALESCE(l.canonical_work_id,alias.source_work_id) IS NOT NULL LIMIT 1`, a.Code, a.Code).Scan(&knownID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result, err
		}
		if knownID != 0 {
			result.Status, result.Rule = "existing", "product_code"
			result.Target = associationSubject{Kind: "work", ID: knownID}
			return result, nil
		}
	}
	options, err := readWorkMatchingOptions(ctx, tx)
	if err != nil {
		return result, err
	}
	// An unfinished scan only withholds automatic association for the subjects
	// it could still publish. A failed or partial source must not freeze every
	// unrelated work and catalog entry forever.
	var incomplete int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_scan scan JOIN workflow_run run ON run.id=scan.workflow_run_id
		WHERE run.status<>'succeeded' AND (
			?='catalog' AND scan.file_source_id=(SELECT file_source_id FROM source_catalog_entry WHERE id=?)
			OR ?='work' AND (
				EXISTS(SELECT 1 FROM source_catalog_entry e WHERE e.work_id=? AND e.file_source_id=scan.file_source_id)
				OR EXISTS(SELECT 1 FROM work_source_presence p WHERE p.work_id=? AND p.file_source_id=scan.file_source_id AND p.presence_type='source')
			)
			OR ?='series' AND EXISTS(SELECT 1 FROM catalog_series_member m JOIN source_catalog_entry e ON e.id=m.catalog_entry_id WHERE m.series_id=? AND e.file_source_id=scan.file_source_id)
		))`, subject.Kind, subject.ID, subject.Kind, subject.ID, subject.ID, subject.Kind, subject.ID).Scan(&incomplete); err != nil {
		return result, err
	}
	if incomplete != 0 {
		result.Status, result.Rule = "paused", "source_index_incomplete"
		return result, nil
	}
	// The query covers every source and registered work. Overflow pauses the whole
	// decision; LIMIT can never manufacture uniqueness from a partial candidate set.
	rows, err := tx.QueryContext(ctx, `SELECT kind,id FROM work_matching_subject s WHERE NOT (kind=? AND id=?) AND (
	 lower(trim(title))=lower(trim(?)) OR (?<>'' AND primary_code=?) OR
	 (? > 0 AND work_id IN (SELECT work_id FROM work_edition WHERE logical_work_id=?)))
	 ORDER BY kind,id LIMIT ?`, subject.Kind, subject.ID, a.Title, a.Code, a.Code, a.Family, a.Family, options.CandidateLimit+1)
	if err != nil {
		return result, err
	}
	var subjects []associationSubject
	for rows.Next() {
		var candidate associationSubject
		if err = rows.Scan(&candidate.Kind, &candidate.ID); err != nil {
			break
		}
		subjects = append(subjects, candidate)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return result, err
	}
	result.CandidateCount = len(subjects)
	if len(subjects) > options.CandidateLimit {
		result.Status = "paused"
		result.Rule = "candidate_limit"
		return result, nil
	}
	rawEvidence, err := json.Marshal(a)
	if err != nil {
		return result, err
	}
	evidenceBytes := len(rawEvidence)
	aPrint, err := associationFingerprint(a)
	if err != nil {
		return result, err
	}
	evidence := []string{aPrint}
	seen := map[int64]bool{}
	best, count := 0, 0
	for _, candidate := range subjects {
		b, e := loadWorkMatchEvidence(ctx, tx, candidate)
		if e != nil {
			return result, e
		}
		rawEvidence, err = json.Marshal(b)
		if err != nil {
			return result, err
		}
		evidenceBytes += len(rawEvidence)
		if evidenceBytes > 2<<20 {
			result.Status, result.Rule = "paused", "metadata_limit"
			return result, nil
		}
		bPrint, printErr := associationFingerprint(b)
		if printErr != nil {
			return result, printErr
		}
		evidence = append(evidence, bPrint)
		if a.Identity > 0 && a.Identity == b.Identity {
			continue
		}
		if b.Identity > 0 && seen[b.Identity] {
			continue
		}
		if b.Identity > 0 {
			seen[b.Identity] = true
		}
		c := matchWorkEvidence(a, b)
		if a.Identity > 0 && b.Identity > 0 {
			var split int
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM shared_association WHERE status='undone' AND
			((EXISTS(SELECT 1 FROM json_each(result_json,'$.left.members') WHERE value IN (SELECT work_id FROM shared_work_identity WHERE identity_work_id=?)) AND EXISTS(SELECT 1 FROM json_each(result_json,'$.right.members') WHERE value IN (SELECT work_id FROM shared_work_identity WHERE identity_work_id=?))) OR
			(EXISTS(SELECT 1 FROM json_each(result_json,'$.right.members') WHERE value IN (SELECT work_id FROM shared_work_identity WHERE identity_work_id=?)) AND EXISTS(SELECT 1 FROM json_each(result_json,'$.left.members') WHERE value IN (SELECT work_id FROM shared_work_identity WHERE identity_work_id=?)))))`, a.Identity, b.Identity, a.Identity, b.Identity).Scan(&split)
			if err != nil {
				return result, err
			}
			if split != 0 {
				c.Rule, c.Confidence = "previously_split", "review"
				c.Reasons = []string{"previously_split"}
				result.Rule = c.Rule
			}
		}
		if c.Rule == "conflicting_evidence" && best == 0 {
			result.Rule = c.Rule
		}
		if len(result.Candidates) < 32 {
			result.Candidates = append(result.Candidates, c)
		}
		rank := 0
		if c.Confidence == "high" {
			rank = 1
		}
		if c.Confidence == "reliable" {
			rank = 2
		}
		if rank > best {
			best, count = rank, 0
		}
		if rank > 0 && rank == best {
			count++
			result.Target = candidate
			result.Rule = c.Rule
		}
	}
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM work_matching_control WHERE id=1`).Scan(&revision); err != nil {
		return result, err
	}
	result.Fingerprint, err = associationFingerprint(struct {
		RuleVersion string
		Revision    int64
		Evidence    []string
	}{"1", revision, evidence})
	if err != nil {
		return result, err
	}
	if len(result.Candidates) > 0 {
		result.Status = "review"
	}
	if count > 1 {
		result.Rule = "ambiguous_evidence"
	} else if count == 1 {
		result.Status = "eligible"
		if result.Rule == "product_code" {
			result.Status = "existing"
		}
	}
	return result, nil
}

func (s *Server) matchWork(ctx context.Context, subject associationSubject, actor *int64) (workMatchResult, error) {
	if !s.workMatchingMu.TryLock() {
		return workMatchResult{}, errWorkMatchingBusy
	}
	defer s.workMatchingMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return workMatchResult{}, err
	}
	defer release()
	if _, err = tx.ExecContext(ctx, `UPDATE shared_association SET id=id WHERE id=-1`); err != nil {
		return workMatchResult{}, err
	}
	subject, err = matchingSubject(ctx, tx.Tx, subject)
	if err != nil {
		return workMatchResult{}, err
	}
	options, err := readWorkMatchingOptions(ctx, tx)
	if err != nil {
		return workMatchResult{}, err
	}
	result := workMatchResult{Status: "paused", Rule: s.catalogIndexDiskPause(catalogIndexOptions{MinFreeBytes: options.MinFreeBytes, MaxResponseBytes: 64 << 10}), Candidates: []workMatchCandidate{}}
	if result.Rule == "" {
		result, err = evaluateWorkMatchTx(ctx, tx.Tx, subject)
	}
	if errors.Is(err, errAssociationLimit) {
		result = workMatchResult{Status: "paused", Rule: "metadata_limit", Candidates: []workMatchCandidate{}}
		err = nil
	}
	if err != nil {
		return result, err
	}
	if result.Status == "existing" && result.Target.Kind == "work" && result.Target.ID > 0 && subject.Kind == "catalog" {
		// A published product code is authoritative evidence. Persist the link
		// so the catalog card joins the registered work in lists and searches;
		// reading through the code alone left a duplicate card behind.
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_entry SET work_id=? WHERE id=? AND work_id IS NULL`, result.Target.ID, subject.ID); err != nil {
			return result, err
		}
	}
	if result.Status == "eligible" {
		var pending int64
		err = tx.QueryRowContext(ctx, `SELECT o.operation_id FROM work_matching_operation o JOIN shared_association a ON a.id=o.operation_id WHERE o.subject_kind=? AND o.subject_id=? AND o.evidence_fingerprint=? AND a.status='pending' ORDER BY o.operation_id DESC LIMIT 1`, subject.Kind, subject.ID, result.Fingerprint).Scan(&pending)
		if err == nil && !freshPendingWorkMatch(ctx, tx.Tx, pending) {
			if _, err = tx.ExecContext(ctx, `UPDATE shared_association SET status='stale' WHERE id=?`, pending); err != nil {
				return result, err
			}
			err = sql.ErrNoRows
		}
		if err == nil {
			result.Status = "pending"
			result.OperationID = pending
		} else if !errors.Is(err, sql.ErrNoRows) {
			return result, err
		} else {
			var existingID int64
			e := tx.QueryRowContext(ctx, `SELECT a.id FROM shared_association a JOIN work_matching_operation m ON m.operation_id=a.id WHERE a.status='pending' AND
			((json_extract(a.snapshot_json,'$.left.subject.kind')=? AND json_extract(a.snapshot_json,'$.left.subject.id')=? AND json_extract(a.snapshot_json,'$.right.subject.kind')=? AND json_extract(a.snapshot_json,'$.right.subject.id')=?) OR
			(json_extract(a.snapshot_json,'$.right.subject.kind')=? AND json_extract(a.snapshot_json,'$.right.subject.id')=? AND json_extract(a.snapshot_json,'$.left.subject.kind')=? AND json_extract(a.snapshot_json,'$.left.subject.id')=?)) ORDER BY a.id DESC LIMIT 1`, subject.Kind, subject.ID, result.Target.Kind, result.Target.ID, subject.Kind, subject.ID, result.Target.Kind, result.Target.ID).Scan(&existingID)
			if e == nil && freshPendingWorkMatch(ctx, tx.Tx, existingID) {
				result.Status, result.OperationID = "pending", existingID
			} else if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return result, e
			} else if e == nil {
				if _, e = tx.ExecContext(ctx, `UPDATE shared_association SET status='stale' WHERE id=?`, existingID); e != nil {
					return result, e
				}
			}
			if result.Status == "eligible" {
				var recent int
				if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM shared_association a JOIN work_matching_operation m ON m.operation_id=a.id WHERE a.created_at>datetime('now','-1 day')`).Scan(&recent); e != nil {
					return result, e
				}
				if recent >= 1000 {
					result.Status, result.Rule = "paused", "operation_limit"
				}
			}
			if result.Status == "eligible" {
				snapshot, fingerprint, e := loadAssociationSnapshot(ctx, tx.Tx, subject, result.Target)
				if e != nil {
					return result, e
				}
				var nonce [24]byte
				if _, e = rand.Read(nonce[:]); e != nil {
					return result, e
				}
				a := storedAssociation{Status: "preview", Version: hex.EncodeToString(nonce[:]), Fingerprint: fingerprint, Snapshot: snapshot}
				raw, e := json.Marshal(snapshot)
				if e != nil {
					return result, e
				}
				if e = tx.QueryRowContext(ctx, `INSERT INTO shared_association(actor_id,version,fingerprint,snapshot_json) VALUES(?,?,?,?) RETURNING id`, actor, a.Version, fingerprint, string(raw)).Scan(&a.ID); e != nil {
					return result, e
				}
				if _, e = tx.ExecContext(ctx, `INSERT INTO work_matching_operation(operation_id,subject_kind,subject_id,evidence_fingerprint,rule) VALUES(?,?,?,?,?)`, a.ID, subject.Kind, subject.ID, result.Fingerprint, result.Rule); e != nil {
					return result, e
				}
				if e = startSharedAssociationTx(ctx, tx.Tx, &a); e != nil {
					return result, e
				}
				result.Status, result.OperationID = a.Status, a.ID
			}
		}
	}
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM work_matching_control WHERE id=1`).Scan(&result.Revision); err != nil {
		return result, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO work_matching_result(kind,entity_id,result_json,revision) VALUES(?,?,?,?) ON CONFLICT(kind,entity_id) DO UPDATE SET result_json=excluded.result_json,revision=excluded.revision,updated_at=CURRENT_TIMESTAMP`, subject.Kind, subject.ID, string(raw), result.Revision); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func freshPendingWorkMatch(ctx context.Context, tx *sql.Tx, id int64) bool {
	a, err := loadStoredAssociation(ctx, tx, id)
	if err != nil {
		return false
	}
	_, fingerprint, err := loadAssociationSnapshot(ctx, tx, a.Snapshot.Left.Subject, a.Snapshot.Right.Subject)
	return err == nil && fingerprint == a.Fingerprint && validateWorkMatchingTx(ctx, tx, id) == nil
}

func (s *Server) runWorkMatching(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.requirePermission(w, r, "library:write")
	if !ok {
		return
	}
	var subject associationSubject
	if !decodeAssociationBody(w, r, &subject) {
		return
	}
	result, err := s.matchWork(r.Context(), subject, &actor.ID)
	if err != nil {
		if errors.Is(err, errWorkMatchingBusy) {
			writeAssociationError(w, errAssociationLimit)
			return
		}
		writeAssociationError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

// Owner decisions are valid only for the same unique, supported candidate set.
// Manual associations have no matching proof and keep their existing contract.
func validateWorkMatchingTx(ctx context.Context, tx *sql.Tx, operation int64) error {
	var subject associationSubject
	var fingerprint string
	err := tx.QueryRowContext(ctx, `SELECT subject_kind,subject_id,evidence_fingerprint FROM work_matching_operation WHERE operation_id=?`, operation).Scan(&subject.Kind, &subject.ID, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	result, err := evaluateWorkMatchTx(ctx, tx, subject)
	if err != nil || result.Status != "eligible" || result.Fingerprint != fingerprint {
		return errAssociationStale
	}
	return nil
}

func (s *Server) getWorkMatching(w http.ResponseWriter, r *http.Request) {
	if s.cfg.IsDemo() {
		writeJSON(w, 200, workMatchResult{Status: "no_match", Candidates: []workMatchCandidate{}})
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	// Reading pending matches never writes, so a read transaction does not take
	// SQLite's single write lock.
	tx, err := s.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeError(w, err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	subject, err := matchingSubject(r.Context(), tx, associationSubject{Kind: r.URL.Query().Get("kind"), ID: id})
	if err != nil {
		writeAssociationError(w, err)
		return
	}
	var raw string
	var savedRevision, currentRevision int64
	err = tx.QueryRowContext(r.Context(), `SELECT result_json,r.revision,c.revision FROM work_matching_result r CROSS JOIN work_matching_control c WHERE kind=? AND entity_id=?`, subject.Kind, subject.ID).Scan(&raw, &savedRevision, &currentRevision)
	result := workMatchResult{Status: "queued", Rule: "awaiting_matching", Candidates: []workMatchCandidate{}}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &result)
		if savedRevision != currentRevision {
			result = workMatchResult{Status: "queued", Rule: "awaiting_matching", Candidates: []workMatchCandidate{}}
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
