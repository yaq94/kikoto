package httpapi

import (
	"context"
	"database/sql"
	"path"
	"strings"
)

// catalogDuplicateDirectoryTx reports a child folder that repeats the parent's
// own audio, such as a "279 works 14G" package placed beside the loose files it
// copies. Publishing it would create a second card for every work inside it,
// while the parent already carries the same recordings.
//
// Provider content hashes identify the same bytes even when a package renamed
// its files. Without a complete hash set, small folders require exact names and
// sizes; a tolerance is used only for larger package copies. Duplicates stay
// unpublished rather than being merged into the parent, because the parent
// already holds the original files.
const (
	catalogDuplicateMinimumFiles = 2
	catalogDuplicateSmallSet     = 20
)

type catalogDuplicateDecision struct {
	Duplicate bool
	Root      string
	Matched   []catalogAudioFile
	Unique    []catalogAudioFile
}

func catalogDuplicateDirectoryTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, directoryPath string) (bool, error) {
	decision, err := catalogDuplicateDirectoryDecisionTx(ctx, tx, sourceID, generation, directoryPath)
	if err != nil {
		return false, err
	}
	return decision.Duplicate && len(decision.Unique) == 0, nil
}

// catalogDuplicateDirectoryDecisionTx separates a harmless format copy from
// one that carries extra recordings. The caller consumes only matched files:
// a unique bonus stays pending and publishes into the canonical parent card.
func catalogDuplicateDirectoryDecisionTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, directoryPath string) (catalogDuplicateDecision, error) {
	parent := path.Dir(directoryPath)
	if directoryPath == "/" || parent == "." || parent == directoryPath {
		return catalogDuplicateDecision{}, nil
	}
	child, err := catalogAudioFilesTx(ctx, tx, sourceID, generation, directoryPath, true)
	if err != nil || len(child) == 0 {
		return catalogDuplicateDecision{}, err
	}
	parentFiles, err := catalogAudioFilesTx(ctx, tx, sourceID, generation, parent, false)
	if err != nil {
		return catalogDuplicateDecision{}, err
	}
	if len(parentFiles) > 0 {
		matched, duplicate := catalogDuplicateMatches(child, parentFiles, catalogPackageDirectory(path.Base(directoryPath)))
		if !duplicate {
			return catalogDuplicateDecision{}, nil
		}
		decision := catalogDuplicateDecision{Duplicate: true, Root: parent}
		for index, file := range child {
			if matched[index] {
				decision.Matched = append(decision.Matched, file)
			} else {
				decision.Unique = append(decision.Unique, file)
			}
		}
		return decision, nil
	}

	// A package can split the parent's recordings across nested Work folders.
	// The immediate container then has no direct audio of its own. Treat a
	// small exact subset of the nearest audio-bearing ancestor as a copy, but
	// only when that container really has several child folders.
	root, ancestorFiles, ok, err := catalogDuplicateAncestorTx(ctx, tx, sourceID, generation, directoryPath, parent)
	if err != nil || !ok {
		return catalogDuplicateDecision{}, err
	}
	if !catalogDuplicateAncestorEvidence(child, ancestorFiles, directoryPath, parent) {
		return catalogDuplicateDecision{}, nil
	}
	if !catalogAudioIsExactSubset(child, ancestorFiles) {
		return catalogDuplicateDecision{}, nil
	}
	decision := catalogDuplicateDecision{Duplicate: true, Root: root, Matched: append([]catalogAudioFile{}, child...)}
	return decision, nil
}

func catalogDuplicateAncestorTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, directoryPath, parent string) (string, []catalogAudioFile, bool, error) {
	var folders int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE o.file_source_id=? AND o.directory_path=? AND o.kind='folder'
			AND o.revision=d.revision AND d.generation=?`, sourceID, parent, generation).Scan(&folders)
	if err != nil || folders < 2 {
		return "", nil, false, err
	}
	for ancestor := path.Dir(parent); ancestor != "." && ancestor != ""; ancestor = path.Dir(ancestor) {
		files, queryErr := catalogAudioFilesTx(ctx, tx, sourceID, generation, ancestor, false)
		if queryErr != nil {
			return "", nil, false, queryErr
		}
		if len(files) >= catalogDuplicateMinimumFiles {
			return ancestor, files, true, nil
		}
		if ancestor == "/" {
			break
		}
	}
	return "", nil, false, nil
}

// catalogDuplicateAncestorEvidence keeps the nested-package shortcut narrow.
// Exact names and sizes can describe unrelated short releases; without hashes
// on both sides only a directory that names a format/track part of one work is
// enough evidence to hide the child as a copy.
func catalogDuplicateAncestorEvidence(child, ancestor []catalogAudioFile, directoryPath, containerPath string) bool {
	if catalogAudioFilesHaveHashes(child) && catalogAudioFilesHaveHashes(ancestor) {
		return true
	}
	if catalogWorkPartDirectory(path.Base(directoryPath)) {
		return true
	}
	// An explicit package container ("Package", "24 works 14G") is named as a
	// collection rather than another work. This keeps nested package copies
	// folded while a plain child folder still needs exact names or hashes.
	return catalogPackageDirectory(path.Base(containerPath))
}

// catalogDuplicateMatches returns a per-child match mask and the existing
// copy decision. The mask is what lets the caller preserve a unique track.
// allowRenamed permits size-only matching inside a directory whose own name
// says it is a package; that is the only no-hash case where renamed files are
// accepted as a copy.
func catalogDuplicateMatches(child, parent []catalogAudioFile, renamedPackage bool) ([]bool, bool) {
	// A selection of the parent's tracks is a real release, not a copy. Only a
	// child that repeats most of the parent's audio can be a format or package
	// copy; twenty shared tracks below a two-hundred-track collection are two
	// different works.
	if len(child)*5 < len(parent)*4 {
		return make([]bool, len(child)), false
	}
	parentNames := make(map[string][]int64, len(parent))
	for _, file := range parent {
		key := strings.ToLower(strings.TrimSpace(file.Name))
		parentNames[key] = append(parentNames[key], file.Size)
	}
	namedMatches, exactNameMatches := 0, 0
	for _, file := range child {
		if sizes, ok := parentNames[strings.ToLower(strings.TrimSpace(file.Name))]; ok {
			if matchingSize(sizes, file.Size) {
				namedMatches++
			}
			if containsExactSize(sizes, file.Size) {
				exactNameMatches++
			}
		}
	}
	hashes := catalogAudioFilesHaveHashes(child) && catalogAudioFilesHaveHashes(parent)
	matched := catalogAudioMatchMask(child, parent, hashes)
	matchedCount := 0
	for _, ok := range matched {
		if ok {
			matchedCount++
		}
	}
	// A single file has no arrangement evidence. Without provider content
	// hashes, equal names and sizes are not proof that two files are the same
	// recording, so keep both cards rather than hiding an independent work.
	if len(child) == 1 || len(parent) == 1 {
		return matched, len(child) == 1 && len(parent) == 1 && hashes && matchedCount == 1
	}
	// A package may contain every parent recording plus a small number of
	// bonus files. The parent is still the canonical card; only the bonus
	// stays pending. Keep the allowance narrow so an unrelated compilation
	// that happens to reuse a few tracks is not folded into the parent.
	if matchedCount == len(parent) && len(child) >= len(parent) &&
		(len(child) <= len(parent)+2 || len(child)*10 <= len(parent)*12) &&
		(hashes || exactNameMatches >= len(parent)) {
		return matched, true
	}
	small := len(child) < catalogDuplicateSmallSet || len(parent) < catalogDuplicateSmallSet
	if hashes {
		hashed := catalogAudioHashMatches(child, parent)
		if small {
			return matched, len(child) == len(parent) && hashed == len(child)
		}
		return matched, hashed*10 >= len(child)*9
	}
	if small {
		if renamedPackage && len(child) == len(parent) && matchedCount == len(child) {
			return matched, true
		}
		return matched, len(child) == len(parent) && exactNameMatches == len(child)
	}
	// Without hashes, the size-only match mask cannot prove a renamed copy. A
	// large package still needs a high proportion of matching names; equality of
	// file counts and sizes alone can describe two unrelated works.
	if matchedCount*10 < len(child)*9 {
		return matched, false
	}
	if namedMatches*10 >= len(child)*9 {
		return matched, len(child) == len(parent)
	}
	return matched, renamedPackage && len(child) == len(parent)
}

func catalogAudioMatchMask(child, parent []catalogAudioFile, hashes bool) []bool {
	used := make([]bool, len(parent))
	mask := make([]bool, len(child))
	parentByName := make(map[string][]int, len(parent))
	for index, file := range parent {
		key := strings.ToLower(strings.TrimSpace(file.Name))
		parentByName[key] = append(parentByName[key], index)
	}
	parentByHash := map[string][]int{}
	if hashes {
		for index, file := range parent {
			hash := strings.TrimSpace(file.Hash)
			parentByHash[hash] = append(parentByHash[hash], index)
		}
	}
	for index, file := range child {
		if hashes {
			hash := strings.TrimSpace(file.Hash)
			for _, candidate := range parentByHash[hash] {
				if !used[candidate] {
					used[candidate], mask[index] = true, true
					break
				}
			}
			continue
		}
		name := strings.ToLower(strings.TrimSpace(file.Name))
		for _, candidate := range parentByName[name] {
			if !used[candidate] && matchingSize([]int64{parent[candidate].Size}, file.Size) {
				used[candidate], mask[index] = true, true
				break
			}
		}
		if mask[index] {
			continue
		}
		for candidate, parentFile := range parent {
			if used[candidate] {
				continue
			}
			tolerance := max(int64(1024), file.Size/1000)
			if parentFile.Size >= file.Size-tolerance && parentFile.Size <= file.Size+tolerance {
				used[candidate], mask[index] = true, true
				break
			}
		}
	}
	return mask
}

func catalogAudioIsExactSubset(child, parent []catalogAudioFile) bool {
	if len(child) < catalogDuplicateMinimumFiles || len(child) > len(parent) {
		return false
	}
	if catalogAudioFilesHaveHashes(child) && catalogAudioFilesHaveHashes(parent) {
		return catalogAudioHashMatches(child, parent) == len(child)
	}
	used := make([]bool, len(parent))
	for _, file := range child {
		found := false
		for index, candidate := range parent {
			if used[index] || candidate.Size != file.Size || !strings.EqualFold(strings.TrimSpace(candidate.Name), strings.TrimSpace(file.Name)) {
				continue
			}
			used[index], found = true, true
			break
		}
		if !found {
			return false
		}
	}
	return true
}

type catalogAudioFile struct {
	Name      string
	Size      int64
	Hash      string
	Directory string
}

func matchingSize(sizes []int64, size int64) bool {
	tolerance := max(int64(1024), size/1000)
	for _, candidate := range sizes {
		if candidate >= size-tolerance && candidate <= size+tolerance {
			return true
		}
	}
	return false
}

func containsExactSize(sizes []int64, size int64) bool {
	for _, candidate := range sizes {
		if candidate == size {
			return true
		}
	}
	return false
}

func catalogAudioFilesHaveHashes(files []catalogAudioFile) bool {
	for _, file := range files {
		if strings.TrimSpace(file.Hash) == "" {
			return false
		}
	}
	return len(files) > 0
}

func catalogAudioHashMatches(child, parent []catalogAudioFile) int {
	available := make(map[string]int, len(parent))
	for _, file := range parent {
		available[strings.TrimSpace(file.Hash)]++
	}
	matched := 0
	for _, file := range child {
		hash := strings.TrimSpace(file.Hash)
		if available[hash] > 0 {
			available[hash]--
			matched++
		}
	}
	return matched
}

func catalogAudioFilesTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, directoryPath string, allowPending bool) ([]catalogAudioFile, error) {
	// Only a directory whose pages this generation finished can describe the
	// current copy. `listing_complete` means every page was read but child
	// directories have not been expanded yet; its direct audio is already
	// stable. A failed, pending or stale directory keeps its old listing, which
	// must not decide how a newly observed child is admitted.
	statuses := "('complete','listing_complete')"
	if allowPending {
		statuses = "('complete','listing_complete','pending')"
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.name,o.size_bytes,COALESCE(o.content_hash,'') FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE o.file_source_id=? AND o.directory_path=? AND o.kind='audio' AND o.revision=d.revision
			AND d.generation=? AND d.status IN `+statuses,
		sourceID, directoryPath, generation)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	files := []catalogAudioFile{}
	for rows.Next() {
		file := catalogAudioFile{Directory: directoryPath}
		if err = rows.Scan(&file.Name, &file.Size, &file.Hash); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, rows.Err()
}
