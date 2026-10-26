package personal

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/yexca/kikoto/backend/internal/workcode"
)

const MaxTransferBytes = 10 << 20
const maxTransferWorks = 20000
const maxTransferTags = 5000
const maxTransferLists = 1000
const maxTransferItems = 50000

type Backup struct {
	Format     string           `json:"format"`
	Version    int              `json:"version"`
	ExportedAt string           `json:"exportedAt,omitempty"`
	Works      []BackupWork     `json:"works"`
	Playlists  []BackupPlaylist `json:"playlists"`
	Tags       []BackupTag      `json:"tags"`
	// Issues are export-side observations about the source library, such as an
	// episode whose persisted parent cannot be expressed. They never block the
	// rest of the backup and are ignored on import.
	Issues []ImportIssue `json:"issues,omitempty"`
}

type BackupWork struct {
	PrimaryCode     string            `json:"primaryCode"`
	Identity        *BackupIdentity   `json:"identity,omitempty"`
	ListeningStatus string            `json:"listeningStatus"`
	Rating          *int              `json:"rating"`
	Note            string            `json:"note"`
	Tags            []string          `json:"tags"`
	Progress        *BackupProgress   `json:"progress,omitempty"`
	Statistics      *BackupStatistics `json:"statistics,omitempty"`
}

// Ref names an exact retained object. References are opaque portable aliases,
// never source addresses, translated titles or permission to change relations.
type BackupIdentity struct {
	Ref        string          `json:"ref"`
	Kind       string          `json:"kind"`
	Title      string          `json:"title"`
	References []string        `json:"references,omitempty"`
	Parent     *BackupIdentity `json:"parent,omitempty"`
}

func (w BackupWork) key() string {
	if w.Identity != nil {
		return w.Identity.Ref
	}
	return w.PrimaryCode
}

func (w BackupWork) label() string {
	if w.Identity != nil {
		return w.Identity.Title
	}
	return w.PrimaryCode
}

type BackupProgress struct {
	MediaWorkCode   string          `json:"mediaWorkCode"`
	MediaIdentity   *BackupIdentity `json:"mediaIdentity,omitempty"`
	MediaTitle      string          `json:"mediaTitle"`
	TrackNumber     *int            `json:"trackNumber"`
	DiscNumber      *int            `json:"discNumber"`
	PositionSeconds float64         `json:"positionSeconds"`
	DurationSeconds *float64        `json:"durationSeconds"`
	Completed       bool            `json:"completed"`
	LastPlayedAt    string          `json:"lastPlayedAt"`
}

type BackupStatistics struct {
	ListenedSeconds float64 `json:"listenedSeconds"`
	ListenCount     int64   `json:"listenCount"`
	LastPlayedAt    string  `json:"lastPlayedAt"`
}

type BackupTag struct {
	Scope string `json:"scope"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type BackupPlaylist struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Icon        string               `json:"icon,omitempty"`
	SortOrder   int64                `json:"sortOrder"`
	Items       []BackupPlaylistItem `json:"items"`
}

type BackupPlaylistItem struct {
	PrimaryCode string `json:"primaryCode"`
	WorkRef     string `json:"workRef,omitempty"`
	Note        string `json:"note"`
	SortOrder   int64  `json:"sortOrder"`
}

func (i BackupPlaylistItem) key() string {
	if i.WorkRef != "" {
		return i.WorkRef
	}
	return i.PrimaryCode
}

type ImportRequest struct {
	Format   string          `json:"format"`
	Data     json.RawMessage `json:"data"`
	Conflict string          `json:"conflict"`
}

func DecodeJSON(data []byte, value any, strict bool) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(value); err != nil {
		return ErrInvalid
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrInvalid
	}
	return nil
}

func ParseImport(request ImportRequest) (Backup, error) {
	if len(request.Data) > MaxTransferBytes {
		return Backup{}, ErrLimit
	}
	if request.Conflict != "" && request.Conflict != "keep" && request.Conflict != "overwrite" {
		return Backup{}, ErrInvalid
	}
	var backup Backup
	switch request.Format {
	case "kikoto":
		if err := DecodeJSON(request.Data, &backup, true); err != nil {
			return backup, err
		}
		if backup.Format != "kikoto-user-data" || (backup.Version != 1 && backup.Version != 2) {
			return backup, ErrInvalid
		}
	case "kikoeru":
		var err error
		backup, err = parseKikoeru(request.Data)
		if err != nil {
			return backup, err
		}
	default:
		return backup, ErrInvalid
	}
	return backup, validateBackup(&backup)
}

func normalizeTimestamp(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil && parsed.Year() >= 1970 && parsed.Before(time.Now().Add(24*time.Hour)) {
			return parsed.UTC().Format("2006-01-02 15:04:05"), nil
		}
	}
	return "", ErrInvalid
}

func normalizeCode(value string) (string, error) {
	value = workcode.Normalize(value)
	if value == "" {
		return "", ErrInvalid
	}
	return value, nil
}

func validStatus(status string) bool {
	switch status {
	case "none", "want_to_listen", "listening", "finished", "relisten", "paused":
		return true
	}
	return false
}

// SQLite's built-in LOWER, used by the existing tag store, folds ASCII only.
func tagNameKey(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, name)
}

func validateBackup(b *Backup) error {
	if len(b.Works) > maxTransferWorks || len(b.Playlists) > maxTransferLists || len(b.Tags) > maxTransferTags {
		return ErrLimit
	}
	seen := map[string]bool{}
	references := map[string]bool{}
	numbered := map[string]bool{}
	items := 0
	assignments := 0
	for i := range b.Works {
		w := &b.Works[i]
		assignments += len(w.Tags)
		if assignments > maxTransferItems {
			return ErrLimit
		}
		var err error
		if b.Version >= 2 {
			if !validBackupIdentity(w.Identity) {
				return ErrInvalid
			}
			// A malformed parent blocks only its entry in the import plan, so
			// other records still import; the parent's ref, kind and title are
			// validated there before any relation is written. Parent references
			// are deliberately not part of this document-wide uniqueness set: a
			// parent series and its top-level BackupWork legitimately share the
			// same series alias.
			for _, ref := range append([]string{w.Identity.Ref}, w.Identity.References...) {
				if references[ref] {
					return ErrInvalid
				}
				references[ref] = true
			}
		} else if w.Identity != nil {
			return ErrInvalid
		}
		if w.PrimaryCode != "" || b.Version == 1 {
			w.PrimaryCode, err = normalizeCode(w.PrimaryCode)
		}
		if err != nil || seen[w.key()] {
			return ErrInvalid
		}
		seen[w.key()] = true
		if w.PrimaryCode != "" {
			if numbered[w.PrimaryCode] {
				return ErrInvalid
			}
			numbered[w.PrimaryCode] = true
		}
		if !validStatus(w.ListeningStatus) || len(w.Tags) > maxTransferTags || (w.Rating != nil && (*w.Rating < 0 || *w.Rating > 5)) {
			return ErrInvalid
		}
		tagSeen := map[string]bool{}
		for j, name := range w.Tags {
			name = strings.TrimSpace(name)
			if !validTagName(name) || tagSeen[tagNameKey(name)] {
				return ErrInvalid
			}
			tagSeen[tagNameKey(name)], w.Tags[j] = true, name
		}
		if p := w.Progress; p != nil {
			if b.Version >= 2 {
				if !validBackupIdentity(p.MediaIdentity) {
					return ErrInvalid
				}
			} else if p.MediaIdentity != nil {
				return ErrInvalid
			}
			if p.MediaWorkCode != "" || b.Version == 1 {
				p.MediaWorkCode, err = normalizeCode(p.MediaWorkCode)
			}
			if err != nil || !validNumber(p.PositionSeconds) || p.PositionSeconds > 31536000 || (p.DurationSeconds != nil && (!validNumber(*p.DurationSeconds) || *p.DurationSeconds > 31536000 || p.PositionSeconds > *p.DurationSeconds)) {
				return ErrInvalid
			}
			p.LastPlayedAt, err = normalizeTimestamp(p.LastPlayedAt)
			if err != nil {
				return err
			}
		}
		if stats := w.Statistics; stats != nil {
			if !validNumber(stats.ListenedSeconds) || stats.ListenedSeconds > 1e12 || stats.ListenCount < 0 || stats.ListenCount > 1e9 {
				return ErrInvalid
			}
			stats.LastPlayedAt, err = normalizeTimestamp(stats.LastPlayedAt)
			if err != nil {
				return err
			}
		}
	}
	workRefs := seen
	seen = map[string]bool{}
	for i := range b.Tags {
		tag := &b.Tags[i]
		tag.Name = strings.TrimSpace(tag.Name)
		key := tag.Scope + ":" + tagNameKey(tag.Name)
		if _, ok := tagScopes[tag.Scope]; !ok || !validTagName(tag.Name) || len(tag.Color) > 64 || seen[key] {
			return ErrInvalid
		}
		seen[key] = true
	}
	seen = map[string]bool{}
	for i := range b.Playlists {
		list := &b.Playlists[i]
		list.Name = strings.TrimSpace(list.Name)
		// Text already shares the bounded file budget. Existing list names and
		// personal notes have no smaller field limit, so do not truncate backups.
		if list.Name == "" || seen[list.Name] {
			return ErrInvalid
		}
		// Favorite-list names use the existing exact-name uniqueness contract.
		seen[list.Name] = true
		icon, ok := NormalizeFavoriteListIcon(list.Icon)
		if !ok {
			return ErrInvalid
		}
		list.Icon = icon
		items += len(list.Items)
		if items > maxTransferItems {
			return ErrLimit
		}
		codes := map[string]bool{}
		for j := range list.Items {
			item := &list.Items[j]
			var err error
			if b.Version >= 2 {
				if !workRefs[item.WorkRef] {
					return ErrInvalid
				}
			} else if item.WorkRef != "" {
				return ErrInvalid
			}
			if item.PrimaryCode != "" || b.Version == 1 {
				item.PrimaryCode, err = normalizeCode(item.PrimaryCode)
			}
			if err != nil || codes[item.key()] {
				return ErrInvalid
			}
			codes[item.key()] = true
		}
	}
	return nil
}

func validTransferRef(ref string) bool {
	if len(ref) != 34 || (ref[:2] != "w:" && ref[:2] != "c:" && ref[:2] != "s:") {
		return false
	}
	_, err := hex.DecodeString(ref[2:])
	return err == nil && strings.ToLower(ref) == ref
}

func validBackupIdentity(i *BackupIdentity) bool {
	if i == nil || !validTransferRef(i.Ref) || (i.Kind != "work" && i.Kind != "episode" && i.Kind != "series") || strings.TrimSpace(i.Title) == "" || len(i.References) > 100 {
		return false
	}
	seen := map[string]bool{i.Ref: true}
	for _, ref := range i.References {
		if !validTransferRef(ref) || seen[ref] {
			return false
		}
		seen[ref] = true
	}
	return true
}

func validBackupParent(parent *BackupIdentity) bool {
	if parent == nil || !validTransferRef(parent.Ref) || !strings.HasPrefix(parent.Ref, "s:") || parent.Kind != "series" || strings.TrimSpace(parent.Title) == "" || parent.Parent != nil {
		return false
	}
	if len(parent.References) > 100 {
		return false
	}
	seen := map[string]bool{parent.Ref: true}
	for _, ref := range parent.References {
		if !validTransferRef(ref) || seen[ref] {
			return false
		}
		seen[ref] = true
	}
	return true
}
