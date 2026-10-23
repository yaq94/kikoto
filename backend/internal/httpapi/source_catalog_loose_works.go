package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/yexca/kikoto/backend/internal/openlist"
	"github.com/yexca/kikoto/backend/internal/workcode"
)

const (
	catalogWorkSplitSingle    = "single"
	catalogWorkSplitPrefix    = "prefix"
	catalogWorkSplitFile      = "file"
	catalogWorkSplitEpisode   = "episode"
	catalogWorkSplitDuplicate = "duplicate"
)

// catalogSplitPinnedSQL reports whether a directory already belongs to a
// tracked work, a series, or a relocation target. Such a card keeps its
// identity; only an unclaimed creator folder can be split into loose works.
// The index scan and the finite scan both decide with this one predicate.
const catalogSplitPinnedSQL = `SELECT EXISTS(
		SELECT 1 FROM source_catalog_entry e
		WHERE e.file_source_id=? AND e.root_path=? AND (
			e.work_id IS NOT NULL
			OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
			OR EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)))`

// A creator folder can contain many independent works as loose audio files.
// These patterns identify the numbering schemes those sources use. A code at
// the start of a file name is a work number, not a track number.
// A release code is defined once in workcode. The short creator-folder form
// (N006, B20) is separate: it is not a global work code, but a repeated prefix
// in one directory means several files belong to the same release.
var catalogLooseWorkPrefix = regexp.MustCompile(`(?i)^(?:` + workcode.PrefixAlternation + `)[0-9]{5,8}(?:[^0-9A-Za-z]|$)|^[A-Z]{1,4}[0-9]{1,6}(?:[^0-9A-Za-z]|$)`)

// "1of3" and "2 of 3" name a track inside one release. Only the spelled-out
// form is accepted: a slash would be a path separator, not part of a name.
var catalogLooseLeadingTrack = regexp.MustCompile(`^[0-9]{1,4}(?:[\s._\-]*of[\s._\-]*[0-9]{1,4})?(?:[\s._\-、]+|$)`)
var catalogLooseTrailingTrack = regexp.MustCompile(`[\s._\-（(【\[]+[0-9]{1,4}[\s._\-）)】\]]*$`)
var catalogLooseTrailingNumber = regexp.MustCompile(`[0-9]{1,4}[\s._\-）)】\]]*$`)
var catalogNumberedTrackPrefix = regexp.MustCompile(`^([0-9]{1,4})(?:[\s._\-]*of[\s._\-]*([0-9]{1,4}))?(?:[\s._\-、]|$)`)
var catalogPrefixedTrack = regexp.MustCompile(`^[\s._\-、]*([0-9]{1,3})(?:[\s._\-、]|$)`)

// A volume marker keeps its number in the title. Vol.1 and Vol.2 are separate
// releases, unlike Title 1 and Title 2, which are tracks of one release.
var catalogLooseVolumeMarker = regexp.MustCompile(`(?i)(?:^|[\s._\-（(【\[])(?:vol(?:ume)?)\.?\s*[0-9]{1,4}\s*$`)

// A calendar year at either end of a name is part of the title. Without this
// guard 2021 Live and 2022 Live both reduce to "live" and merge into one card,
// and a release dated only by its year loses its identity.
var catalogLooseLeadingYear = regexp.MustCompile(`^(?:19|20)[0-9]{2}(?:[\s._\-、:：]|$)`)
var catalogLooseTrailingYear = regexp.MustCompile(`(?:^|[\s._\-（(【\[])(?:19|20)[0-9]{2}\s*[）)】\]]*$`)

// Disc, CD and Part number the pieces of one release. They are neither work
// codes nor track numbers: Disc1 Opening and Disc2 Ending are one card, while
// Vol.1 and Vol.2 stay separate releases.
var catalogLooseLeadingDisc = regexp.MustCompile(`(?i)^(?:disc|disk|cd|part|chapter|track|scene)\s*[0-9]{1,3}(?:[\s._\-、:：]+|$)`)
var catalogStructuredTrackPrefix = regexp.MustCompile(`(?i)^(disc|disk|cd|part|chapter|track|scene)\s*([0-9]{1,3})(?:[\s._\-、:：]+|$)`)

// A trailing part word names one half of a release. 表妹上 and 表妹下 are two
// files of one work; the same is true for 前編/後編 and card markers. The
// marker is only removed when both halves are present in one directory, so a
// lone title ending in 上 or 下 keeps its own identity.
var catalogLoosePartSuffix = regexp.MustCompile(`(?i)^(.*?)(?:[\s._\-（(【\[]*(前編|後編|前编|后编|前篇|后篇|上巻|下巻|上卷|下卷|中篇|中編|中编|中部|上|中|下|前|后|後)|[\s._\-（(【\[]+(?:part|disc|disk|cd|chapter|track|scene)\s*([0-9]{1,3}))\s*[）)】\]]*$`)

var catalogPartFirstWords = map[string]bool{
	"上": true, "前": true, "前編": true, "前编": true, "前篇": true, "上巻": true, "上卷": true,
}
var catalogPartSecondWords = map[string]bool{
	"下": true, "后": true, "後": true, "後編": true, "后编": true, "后篇": true, "下巻": true, "下卷": true,
}
var catalogPartMiddleWords = map[string]bool{
	"中": true, "中篇": true, "中編": true, "中编": true, "中部": true,
}

// A release can name its parts instead of numbering them: Prologue / Main
// Story / Epilogue or Opening / Ending. When every file in one directory is
// such a part, the directory is one work rather than one work per part.
var catalogNarrativePart = regexp.MustCompile(`(?i)^(?:prologue|epilogue|opening|ending|intro|outro|main story|main|bonus track|bonus|extra|omake|preview|sample|teaser|序章|終章|终章|前編|後編|前篇|后篇|上|中|下)$`)
var catalogNarrativeSuffix = regexp.MustCompile(`(?i)^(.+?)[\s._\-（(【\[]*(?:prologue|epilogue|opening|ending|intro|outro|main story|bonus track|bonus|extra|omake|preview|sample|teaser|序章|終章|终章|前編|後編|前篇|后篇|上巻|下巻|上卷|下卷|上|中|下)[）)】\]]*$`)

// Words that number a piece of a release rather than identify a work. They
// must not become a work prefix: Disc1 and Disc2 are two halves of one work,
// not two works.
var catalogStructuralPrefixWords = map[string]bool{
	"disc": true, "disk": true, "cd": true, "part": true,
	"vol": true, "volume": true, "chapter": true, "track": true, "scene": true,
}

// catalogWorkSplitMode decides how one directory's direct audio is published.
// The decision is taken once, when the directory listing first completes.
func catalogWorkSplitMode(files []openlist.File) string {
	names := make([]string, 0, len(files))
	for _, file := range files {
		if file.Kind == "audio" && !strings.Contains(file.Path, "/") {
			names = append(names, file.Path)
		}
	}
	return catalogWorkSplitModeNames(names)
}

func catalogWorkSplitModeNames(names []string) string {
	if len(names) == 0 {
		return catalogWorkSplitSingle
	}
	if _, ok := catalogLooseEpisodeFiles("", names); ok {
		return catalogWorkSplitEpisode
	}
	// 001.mp3..225.mp3 is one audiobook with many episodes, not 225 works. A
	// set of date-only names is also all digits but is not a sequence: 20210104
	// and 20210110 are separate recordings, so require an ordered run rather
	// than testing the stem shape alone.
	if catalogNumberedTrackRun(names) || catalogStructuredTrackRun(names) ||
		catalogTrailingNumberRun(names) || catalogNamedPartRun(names) ||
		catalogNumberedRunWithExtras(names) {
		return catalogWorkSplitSingle
	}
	prefixes := map[string]int{}
	for _, name := range names {
		if prefix := catalogWorkPrefix(name); prefix != "" {
			prefixes[prefix]++
		}
	}
	// A single repeated prefix is enough: RJ00000001 上 / RJ00000001 下 is one
	// release in two files and must not become two cards just because no other
	// code appears in the directory. A lone file with a code stays on the
	// ordinary base-title rule.
	for _, count := range prefixes {
		if count >= 2 {
			return catalogWorkSplitPrefix
		}
	}
	// A creator folder can hold only a few unrelated recordings, so a minimum
	// file count would fold them into one card. Split when several base titles
	// remain; the numbered-run check above already kept 01.mp3/02.mp3/03.mp3
	// and 01 Opening/02 Ending together.
	distinct := map[string]bool{}
	for _, name := range names {
		// Keep a trailing number as part of the identity unless the whole set
		// forms a contiguous run (handled above). Room 101 and Room 102 are
		// different works; stripping both to "room" would merge them, while a
		// genuine Example Session 1..4 has already been recognized as one work.
		distinct[catalogWorkIdentityTitle(name)] = true
	}
	if len(distinct) > 1 {
		return catalogWorkSplitFile
	}
	return catalogWorkSplitSingle
}

// catalogTrailingNumberRun reports titles that share one base and differ only
// by a contiguous trailing number starting at 0 or 1: "Example Session 1..4".
// A high or gapped counter ("Room 101", "Case 7") stays part of the title.
func catalogTrailingNumberRun(names []string) bool {
	if len(names) < 2 {
		return false
	}
	prefix := ""
	positions := make([]int, 0, len(names))
	for _, name := range names {
		stem := strings.TrimSpace(catalogFileStem(name))
		// Vol.1 and Vol.2 are separate releases even though the numbers form a
		// run. The volume marker belongs to the title.
		if catalogLooseVolumeMarker.MatchString(strings.ToLower(stem)) {
			return false
		}
		loc := catalogLooseTrailingNumber.FindStringIndex(stem)
		if loc == nil || loc[1] != len(stem) {
			return false
		}
		number := strings.TrimRight(stem[loc[0]:], " ._-）)】]")
		// A zero-padded number is a code, not a track counter: T001/T002 and
		// B006/B007 are separate releases.
		if len(number) > 1 && number[0] == '0' {
			return false
		}
		position, err := strconv.Atoi(strings.TrimSpace(number))
		if err != nil || position > 9999 {
			return false
		}
		base := strings.TrimSpace(strings.TrimRight(stem[:loc[0]], " ._-（(【["))
		if !catalogHasLetter(base) {
			return false
		}
		if prefix == "" {
			prefix = strings.ToLower(base)
		} else if !strings.EqualFold(prefix, base) {
			return false
		}
		positions = append(positions, position)
	}
	sort.Ints(positions)
	if positions[0] > 1 {
		return false
	}
	for i, position := range positions {
		if position != positions[0]+i {
			return false
		}
	}
	return true
}

// catalogNamedPartRun reports a directory whose files are all named parts of
// one release and share the same base. Whole names such as Prologue and Main
// Story have an empty base; 表妹上 and 表妹下 share "表妹".
func catalogNamedPartRun(names []string) bool {
	if len(names) < 2 {
		return false
	}
	base := ""
	for index, name := range names {
		// A repeated release code already has its own grouping rule; folding it
		// here would hide that evidence and change the card identity.
		if catalogWorkPrefix(name) != "" {
			return false
		}
		stem := strings.ToLower(strings.TrimSpace(catalogFileStem(name)))
		current := ""
		if catalogNarrativePart.MatchString(stem) {
			current = ""
		} else if match := catalogNarrativeSuffix.FindStringSubmatch(stem); match != nil {
			current = strings.TrimSpace(match[1])
			if !catalogHasLetter(current) {
				return false
			}
		} else {
			return false
		}
		if index == 0 {
			base = current
		} else if current != base {
			return false
		}
	}
	return true
}

// catalogNumberedRunWithExtras keeps a numbered release together when one or
// two files are named Bonus, Extra or a narrative part. Without this a single
// Bonus track turns 01..03 into four separate works.
func catalogNumberedRunWithExtras(names []string) bool {
	if len(names) < 2 {
		return false
	}
	numbered := make([]string, 0, len(names))
	extras := 0
	for _, name := range names {
		stem := strings.ToLower(strings.TrimSpace(catalogFileStem(name)))
		if catalogNumberedTrackPrefix.MatchString(stem) {
			numbered = append(numbered, name)
			continue
		}
		if catalogNarrativePart.MatchString(stem) || catalogNarrativeSuffix.MatchString(stem) ||
			catalogAuxiliaryDirectory(stem) || catalogFormatDirectory(stem) {
			extras++
			continue
		}
		return false
	}
	if extras == 0 || len(numbered) == 0 {
		return false
	}
	if len(numbered) >= 2 {
		return catalogNumberedTrackRun(numbered)
	}
	match := catalogNumberedTrackPrefix.FindStringSubmatch(strings.TrimSpace(catalogFileStem(numbered[0])))
	if match == nil {
		return false
	}
	position, err := strconv.Atoi(match[1])
	return err == nil && position <= 1
}

// catalogEpisodeNumberLimit separates an episode or track number from a
// number that belongs to the title. "Alpha Rain 1..3" are three episodes of
// one work; "Room 101" and "Room 102" are different works.
const catalogEpisodeNumberLimit = 30

// catalogWorkIdentityTitle is the base title used to decide whether a
// directory contains several works. A small trailing number is a track or
// episode marker and is removed, so one work stays together; a larger number
// is part of its title, so unrelated works do not collapse onto one card.
func catalogWorkIdentityTitle(name string) string {
	original := strings.ToLower(strings.TrimSpace(catalogFileStem(name)))
	if catalogLooseVolumeMarker.MatchString(original) {
		return strings.Join(strings.Fields(original), " ")
	}
	// A stem that is nothing but a local code is the whole identity. Stripping
	// its digits would collapse T001 and T002 onto "t".
	if catalogLooseWorkPrefix.FindString(original) == original {
		return original
	}
	stem := original
	if !catalogLooseLeadingYear.MatchString(stem) {
		stem = catalogLooseLeadingTrack.ReplaceAllString(stem, "")
	}
	stem = strings.TrimSpace(catalogStripLeadingDisc(stem))
	trimmed := strings.TrimSpace(catalogStripTrailingTrack(stem))
	if trimmed == stem {
		trimmed = strings.TrimSpace(catalogStripTrailingNumber(stem))
	}
	if trimmed != stem {
		digits := strings.TrimRight(stem[len(trimmed):], " ._-）)】]")
		// A zero-padded number is a release or work code ("T001", "N006"),
		// not an episode counter. Stripping it would merge T001 and T002.
		codeSuffix := strings.HasPrefix(digits, "0") && len(digits) > 1
		if number, err := strconv.Atoi(strings.TrimSpace(digits)); err == nil && number <= catalogEpisodeNumberLimit && !codeSuffix {
			stem = trimmed
		}
	}
	trimmed = strings.TrimRight(stem, " ._-（(【[")
	if !catalogHasLetter(trimmed) {
		return strings.Join(strings.Fields(original), " ")
	}
	return strings.Join(strings.Fields(trimmed), " ")
}

// catalogNumberedTrackRun reports a directory whose every file starts with an
// ordered track number. The sequence must be the only discriminator: the
// remaining titles may differ. Missing or duplicate positions keep the caller
// on the ordinary base-title rule so a broken listing is not silently merged.
func catalogNumberedTrackRun(names []string) bool {
	if len(names) < 2 {
		return false
	}
	positions := make([]int, 0, len(names))
	total := -1
	bare := 0
	for _, name := range names {
		stem := strings.TrimSpace(catalogFileStem(name))
		match := catalogNumberedTrackPrefix.FindStringSubmatch(stem)
		if match == nil {
			return false
		}
		// A bare number is a track only when the whole listing is numbered.
		// "T001 Crossing" / "T002 Departure" carry the number after the code,
		// and a single "T001" beside a title must not fold the two together.
		if strings.TrimSpace(stem[len(match[0]):]) == "" {
			bare++
		}
		position, err := strconv.Atoi(match[1])
		if err != nil || position < 0 || position > 999 {
			return false
		}
		// A stated total must be the same on every file; "1 of 2" next to
		// "2 of 3" is two different releases, not one track listing.
		if match[2] != "" {
			declared, err := strconv.Atoi(match[2])
			if err != nil || declared < position || declared > 999 {
				return false
			}
			if total >= 0 && total != declared {
				return false
			}
			total = declared
		} else if total >= 0 {
			return false
		}
		positions = append(positions, position)
	}
	if bare != len(names) && bare != 0 {
		// Mixed bare numbers and titled files in one folder are separate
		// recordings, not one track list.
		return false
	}
	sort.Ints(positions)
	if positions[0] > 1 {
		// A year or a high counter is not a track listing. A genuine run
		// starts at the first track (00 is an accepted prelude); a missing
		// start means the number is part of the title.
		return false
	}
	for i, position := range positions {
		if position != positions[0]+i {
			return false
		}
	}
	return true
}

// catalogStructuredTrackRun reports a directory whose files are all numbered
// parts of one release (Disc1/Disc2, CD1/CD2, Part1/Part2). The parts may have
// different titles, so the run itself is the evidence that they belong
// together. A gap or a mix of marker words keeps them on the ordinary rule.
// A work prefix may precede the marker (B20 Disc1).
func catalogStructuredTrackRun(names []string) bool {
	if len(names) < 2 {
		return false
	}
	prefix := ""
	word := ""
	positions := make([]int, 0, len(names))
	for _, name := range names {
		stem := strings.TrimSpace(catalogFileStem(name))
		workPrefix := catalogWorkPrefix(name)
		rest := stem
		if workPrefix != "" && len(stem) >= len(workPrefix) && strings.EqualFold(stem[:len(workPrefix)], workPrefix) {
			rest = strings.TrimLeft(stem[len(workPrefix):], " ._-、")
		}
		match := catalogStructuredTrackPrefix.FindStringSubmatch(rest)
		if match == nil {
			return false
		}
		if prefix == "" {
			prefix = workPrefix
		} else if !strings.EqualFold(prefix, workPrefix) {
			return false
		}
		current := strings.ToLower(match[1])
		if word == "" {
			word = current
		} else if word != current {
			return false
		}
		position, err := strconv.Atoi(match[2])
		if err != nil || position > 999 {
			return false
		}
		positions = append(positions, position)
	}
	sort.Ints(positions)
	if positions[0] > 1 {
		return false
	}
	for i, position := range positions {
		if position != positions[0]+i {
			return false
		}
	}
	return true
}

func catalogFileStem(name string) string {
	return strings.TrimSuffix(path.Base(name), path.Ext(name))
}

// catalogBaseTitle removes a leading or trailing track number so 01 Title,
// Title 1 and Title 2 count as the same work. The full title is still kept for
// display. A title that is only a number has no base and keeps its own name.
func catalogBaseTitle(name string) string {
	original := strings.ToLower(strings.TrimSpace(catalogFileStem(name)))
	if catalogLooseVolumeMarker.MatchString(original) {
		// The number belongs to the title, so the whole stem is the identity.
		return strings.Join(strings.Fields(original), " ")
	}
	stem := original
	if !catalogLooseLeadingYear.MatchString(stem) {
		stem = catalogLooseLeadingTrack.ReplaceAllString(stem, "")
	}
	stem = catalogStripLeadingDisc(stem)
	trimmed := strings.TrimSpace(catalogStripTrailingTrack(stem))
	if trimmed == stem {
		// Titles such as Example Episode1 and Example Episode2 are episodes of
		// one work even without a separator before the number.
		trimmed = strings.TrimSpace(catalogStripTrailingNumber(stem))
	}
	trimmed = strings.TrimRight(trimmed, " ._-（(【[")
	// A number is not a title. Without this guard a date or a bare counter is
	// reduced to a fragment (20210104 -> 2021, 2020-12-16 -> 12), so unrelated
	// recordings in one creator folder collapse onto the same card. Only strip
	// a track number when a real title remains; otherwise keep the full stem.
	if !catalogHasLetter(trimmed) {
		return strings.Join(strings.Fields(original), " ")
	}
	return strings.Join(strings.Fields(trimmed), " ")
}

// catalogStripLeadingDisc removes a leading disc or part marker when a real
// title follows. A bare marker keeps its stem because there is no identity to
// share with another file.
func catalogStripLeadingDisc(stem string) string {
	match := catalogLooseLeadingDisc.FindString(stem)
	if match == "" {
		return stem
	}
	rest := strings.TrimSpace(stem[len(match):])
	if !catalogHasLetter(rest) {
		return stem
	}
	return rest
}

// catalogStripTrailingTrack removes a trailing track number unless it is a
// calendar year, which is part of the title.
func catalogStripTrailingTrack(stem string) string {
	if catalogLooseTrailingYear.MatchString(stem) {
		return stem
	}
	return catalogLooseTrailingTrack.ReplaceAllString(stem, "")
}

func catalogStripTrailingNumber(stem string) string {
	if catalogLooseTrailingYear.MatchString(stem) {
		return stem
	}
	return catalogLooseTrailingNumber.ReplaceAllString(stem, "")
}

func catalogHasLetter(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// catalogPartTitle splits a base title into the shared work title and the half
// of the release named by its suffix. It reports rank 1 for the opening half
// and rank 2 for the closing half; a numeric part keeps its own rank (zero for
// a third or later part). The caller only merges when one directory contains
// both an opening and a closing half.
func catalogPartTitle(name string) (string, int, bool) {
	base := catalogBaseTitle(name)
	match := catalogLoosePartSuffix.FindStringSubmatch(base)
	if match == nil {
		return "", 0, false
	}
	neutral := strings.TrimSpace(strings.TrimRight(match[1], " ._-（(【["))
	if neutral == "" || !catalogHasLetter(neutral) {
		return "", 0, false
	}
	// A one-character stem followed by 上/下/前/后 is usually an ordinary word
	// (之前 and 之后, 早上), not two halves of a release. Require a real title
	// before folding an unseparated single-character marker.
	if match[3] == "" && len([]rune(neutral)) < 2 {
		return "", 0, false
	}
	if match[3] != "" {
		position, err := strconv.Atoi(match[3])
		if err != nil {
			return "", 0, false
		}
		switch position {
		case 1:
			return neutral, 1, true
		case 2:
			return neutral, 2, true
		default:
			return neutral, 0, true
		}
	}
	word := strings.ToLower(match[2])
	switch {
	case catalogPartFirstWords[word]:
		return neutral, 1, true
	case catalogPartSecondWords[word]:
		return neutral, 2, true
	case catalogPartMiddleWords[word]:
		// The middle part has no rank of its own; it joins the pair when the
		// opening and closing halves are present.
		return neutral, 0, true
	}
	return "", 0, false
}

// catalogPartFold names the files whose trailing part marker is shared with the
// other half of the same release. A directory that only contains one file
// ending in 上 keeps it; a pair of 上 and 下 collapses to the shared title.
func catalogPartFold(names []string) map[string]string {
	type partTitle struct {
		neutral string
		rank    int
	}
	parts := map[string]partTitle{}
	ranks := map[string]map[int]bool{}
	for _, name := range names {
		neutral, rank, ok := catalogPartTitle(name)
		if !ok {
			continue
		}
		parts[name] = partTitle{neutral: neutral, rank: rank}
		if ranks[neutral] == nil {
			ranks[neutral] = map[int]bool{}
		}
		ranks[neutral][rank] = true
	}
	folded := map[string]string{}
	for name, part := range parts {
		if observed := ranks[part.neutral]; observed[1] && observed[2] {
			folded[name] = part.neutral
		}
	}
	return folded
}

// catalogStructuredRuns groups files that number the pieces of one release.
// The marker may follow a work prefix (RJ... Disc1 Opening) or stand alone
// (Disc1 Opening). Only a complete run with unique positions is evidence; a
// duplicate Disc1 in one folder is two releases, so it keeps the ordinary
// title rule instead of silently merging them.
func catalogStructuredRuns(names []string) map[string]string {
	type entry struct {
		name     string
		prefix   string
		word     string
		position int
	}
	entries := []entry{}
	for _, name := range names {
		stem := strings.TrimSpace(catalogFileStem(name))
		prefix := catalogWorkPrefix(name)
		rest := stem
		if prefix != "" && len(stem) >= len(prefix) && strings.EqualFold(stem[:len(prefix)], prefix) {
			rest = strings.TrimLeft(stem[len(prefix):], " ._-、")
		}
		match := catalogStructuredTrackPrefix.FindStringSubmatch(rest)
		if match == nil {
			continue
		}
		position, err := strconv.Atoi(match[2])
		if err != nil || position > 999 {
			continue
		}
		entries = append(entries, entry{name: name, prefix: prefix, word: strings.ToLower(match[1]), position: position})
	}
	type runKey struct{ prefix, word string }
	grouped := map[runKey][]entry{}
	for _, item := range entries {
		key := runKey{item.prefix, item.word}
		grouped[key] = append(grouped[key], item)
	}
	result := map[string]string{}
	for key, group := range grouped {
		if len(group) < 2 {
			continue
		}
		positions := make([]int, 0, len(group))
		seen := map[int]bool{}
		for _, item := range group {
			if seen[item.position] {
				positions = nil
				break
			}
			seen[item.position] = true
			positions = append(positions, item.position)
		}
		sort.Ints(positions)
		if len(positions) == 0 || positions[0] > 1 {
			continue
		}
		complete := true
		for i, position := range positions {
			if position != positions[0]+i {
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		cardKey := "structured:" + key.word
		if key.prefix != "" {
			cardKey = key.prefix
		}
		for _, item := range group {
			result[item.name] = cardKey
		}
	}
	return result
}

func catalogWorkPrefix(name string) string {
	stem := catalogFileStem(name)
	match := catalogLooseWorkPrefix.FindString(stem)
	if match == "" {
		return ""
	}
	// The pattern consumes one delimiter so a following digit cannot extend
	// the number. The prefix itself ends at the last digit.
	end := len(match)
	for end > 0 && (match[end-1] < '0' || match[end-1] > '9') {
		end--
	}
	if end == 0 {
		return ""
	}
	prefix := strings.ToUpper(match[:end])
	word := strings.TrimRightFunc(prefix, func(r rune) bool { return unicode.IsDigit(r) })
	if catalogStructuralPrefixWords[strings.ToLower(strings.TrimSpace(word))] {
		return ""
	}
	return prefix
}

// catalogLooseGrouping is the work partition of one directory listing. It is
// built once from the direct audio names so refresh and publish see the same
// groups.
type catalogLooseGrouping struct {
	mode          string
	splitPrefixes map[string]bool
	partFold      map[string]string
	structured    map[string]string
	episodes      map[string]catalogLooseEpisode
}

// newCatalogLooseGrouping decides whether a repeated work prefix names one
// work or several. A prefix such as N006 usually identifies one release, but
// creator folders also reuse B011 for unrelated recordings. Within one prefix,
// files stay together when they share a base title (B20 ...01/02) or when the
// prefix is followed by a track run starting at 1 (N006-1/2/3). Different
// titles without that evidence stay separate.
func newCatalogLooseGrouping(names []string, mode string) catalogLooseGrouping {
	grouping := catalogLooseGrouping{
		mode:          mode,
		splitPrefixes: map[string]bool{},
		partFold:      catalogPartFold(names),
		structured:    catalogStructuredRuns(names),
		episodes:      catalogLooseEpisodeSubset("", names),
	}
	if mode == catalogWorkSplitEpisode {
		if episodes, ok := catalogLooseEpisodeFiles("", names); ok {
			grouping.episodes = episodes
		}
	}
	if mode == catalogWorkSplitPrefix {
		byPrefix := map[string][]string{}
		for _, name := range names {
			if prefix := catalogWorkPrefix(name); prefix != "" {
				byPrefix[prefix] = append(byPrefix[prefix], name)
			}
		}
		for prefix, group := range byPrefix {
			if !catalogPrefixFilesStayTogether(prefix, group, grouping.partFold) {
				grouping.splitPrefixes[prefix] = true
			}
		}
	}
	return grouping
}

func catalogPrefixFilesStayTogether(prefix string, names []string, folded map[string]string) bool {
	bases := map[string]bool{}
	for _, name := range names {
		if neutral, ok := folded[name]; ok {
			bases[neutral] = true
			continue
		}
		bases[catalogBaseTitle(name)] = true
	}
	if len(bases) == 1 {
		return true
	}
	positions := make([]int, 0, len(names))
	for _, name := range names {
		stem := strings.TrimSpace(catalogFileStem(name))
		if !strings.HasPrefix(strings.ToUpper(stem), prefix) {
			return false
		}
		remainder := stem[len(prefix):]
		match := catalogPrefixedTrack.FindStringSubmatch(remainder)
		if match == nil {
			return false
		}
		position, err := strconv.Atoi(match[1])
		if err != nil || position < 0 {
			return false
		}
		positions = append(positions, position)
	}
	sort.Ints(positions)
	if positions[0] > 1 {
		return false
	}
	for i, position := range positions {
		if position != positions[0]+i {
			return false
		}
	}
	return true
}

func (g catalogLooseGrouping) keyFor(name string) string {
	if episode, ok := g.episodes[name]; ok {
		return "episode:" + episode.Key
	}
	// Numbered discs, CDs and parts of one release share a card. The marker is
	// checked before the work prefix because RJ... Disc1 and RJ... Disc2 are
	// one release, not two works with the same prefix.
	if key, ok := g.structured[name]; ok {
		return key
	}
	// A paired part marker is not part of the work identity: both halves keep
	// one key. The folded value already contains the work prefix and title.
	if neutral, ok := g.partFold[name]; ok {
		if g.mode == catalogWorkSplitPrefix {
			if prefix := catalogWorkPrefix(name); prefix != "" && strings.EqualFold(strings.TrimSpace(neutral), prefix) {
				return prefix
			}
		}
		return neutral
	}
	if g.mode == catalogWorkSplitPrefix {
		prefix := catalogWorkPrefix(name)
		if prefix == "" {
			// A creator folder can mix coded releases with unrelated uncoded
			// recordings. Each uncoded recording is its own work: keeping them
			// on the parent card merged several works and their evaluations
			// into one. The file identity keeps a single work's tracks together
			// because a contiguous numbered run is already one split mode above.
			return catalogWorkIdentityTitle(name)
		}
		if g.splitPrefixes[prefix] {
			// Keep the prefix in the identity so B011 and B012 with the same
			// title cannot collapse onto one card. The remainder is read from
			// the file stem, not the lowercased base title, so the prefix is
			// removed once and the card key stays readable.
			stem := strings.TrimSpace(catalogFileStem(name))
			remainder := ""
			if len(stem) >= len(prefix) && strings.EqualFold(stem[:len(prefix)], prefix) {
				remainder = strings.TrimSpace(strings.TrimLeft(stem[len(prefix):], " ._-、"))
			}
			if base := catalogBaseTitle(name); base != "" && remainder == "" {
				remainder = base
			}
			if remainder != "" {
				return prefix + " " + remainder
			}
		}
		return prefix
	}
	if g.mode == catalogWorkSplitFile {
		// In a creator folder the file title decides the work. A trailing
		// number belongs to that title unless the whole set is a contiguous
		// run, which the mode check has already treated as one work.
		if base := catalogWorkIdentityTitle(name); base != "" {
			return base
		}
	}
	if base := catalogBaseTitle(name); base != "" && g.mode != catalogWorkSplitFile {
		return base
	}
	if g.mode == catalogWorkSplitFile {
		if base := catalogWorkIdentityTitle(name); base != "" {
			return base
		}
	}
	return strings.ToLower(strings.TrimSpace(catalogFileStem(name)))
}

// catalogGroupTitles returns the display title of each group from the
// lexicographically first file in that group.
func catalogGroupTitles(grouping catalogLooseGrouping, names []string) map[string]string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	titles := map[string]string{}
	for _, name := range sorted {
		if episode, ok := grouping.episodes[name]; ok {
			key := "episode:" + episode.Key
			if _, exists := titles[key]; !exists {
				titles[key] = episode.Title
			}
			continue
		}
		key := grouping.keyFor(name)
		if key == "" {
			continue
		}
		if _, ok := titles[key]; ok {
			continue
		}
		titles[key] = catalogFileStem(name)
	}
	return titles
}

type catalogSplitTarget struct {
	Root       string
	SourceRoot string
	Title      string
	Episode    catalogSeriesEvidence
	HasEpisode bool
}

// catalogSplitPlan is the one partition shared by the resumable index, a
// finite scan and a single-card refresh. Direct audio decides the groups;
// nested audio joins a group only when it is a format copy or track part of
// that group. Everything else stays on the parent card so a refresh or a
// generation change cannot silently drop it.
type catalogSplitPlan struct {
	root     string
	grouping catalogLooseGrouping
	titles   map[string]string
	direct   map[string]catalogSplitTarget
	byKey    map[string]catalogSplitTarget
}

func newCatalogSplitPlan(root string, names []string, mode string) catalogSplitPlan {
	grouping := newCatalogLooseGrouping(names, mode)
	if episodes := catalogLooseEpisodeSubset(root, names); len(episodes) > 0 {
		grouping.episodes = episodes
	}
	plan := catalogSplitPlan{
		root:     root,
		grouping: grouping,
		titles:   catalogGroupTitles(grouping, names),
		direct:   map[string]catalogSplitTarget{},
		byKey:    map[string]catalogSplitTarget{},
	}
	if mode != catalogWorkSplitPrefix && mode != catalogWorkSplitFile && mode != catalogWorkSplitEpisode {
		return plan
	}
	for _, name := range names {
		key := plan.grouping.keyFor(name)
		if key == "" {
			continue
		}
		target := catalogSplitTarget{Root: root + "::" + key, SourceRoot: root, Title: plan.titles[key]}
		if episode, ok := plan.grouping.episodes[name]; ok {
			target.Episode = episode.Evidence
			target.HasEpisode = true
			target.Title = episode.Title
		}
		plan.direct[name] = target
		if _, exists := plan.byKey[key]; !exists {
			plan.byKey[key] = target
		}
	}
	return plan
}

// targetFor maps a listing-relative audio path to its card. A nested file is
// only claimed when its directory is a format copy or track container and its
// group key names one of this directory's cards. A nested file without that
// evidence keeps the parent card, which is then not retired.
func (p catalogSplitPlan) targetFor(relPath string) (catalogSplitTarget, bool) {
	key := p.grouping.keyFor(relPath)
	if key == "" {
		return catalogSplitTarget{}, false
	}
	if target, ok := p.direct[relPath]; ok {
		return target, true
	}
	target, ok := p.byKey[key]
	if !ok {
		return catalogSplitTarget{}, false
	}
	if !strings.Contains(relPath, "/") {
		return catalogSplitTarget{}, false
	}
	if !catalogWorkPartDirectory(path.Base(path.Dir(relPath))) {
		return catalogSplitTarget{}, false
	}
	return target, true
}

// catalogSplitSnapshot partitions a finite scan. Only direct audio is split;
// the parent keeps direct audio without a work prefix and nested audio from
// part directories, so a disc or format copy is not lost.
func catalogSplitSnapshot(root string, files []openlist.File, mode string) (map[string][]openlist.File, []openlist.File) {
	if mode == catalogWorkSplitSingle {
		return nil, files
	}
	plan := newCatalogSplitPlan(root, catalogDirectAudioNames(files), mode)
	groups := map[string][]openlist.File{}
	parent := []openlist.File{}
	for _, file := range files {
		if file.Kind != "audio" {
			// Readmes, covers and subtitles describe the whole directory, not
			// one split work. They stay on the parent card so a split does not
			// silently drop the release's credit file or artwork.
			parent = append(parent, file)
			continue
		}
		target, ok := plan.targetFor(file.Path)
		if !ok {
			parent = append(parent, file)
			continue
		}
		groups[target.Root] = append(groups[target.Root], file)
	}
	return groups, parent
}

// catalogSplitCardFiles keeps only the files that belong to one published
// split card. A refresh of that card must not republish the whole creator
// directory into it. Nested audio (discs, format copies) stays on the parent
// card, so split cards only ever own direct files.
func catalogSplitCardFiles(rootPath, sourceRoot string, files []openlist.File) []openlist.File {
	if sourceRoot == "" || !strings.HasPrefix(rootPath, sourceRoot+"::") {
		if sourceRoot != "" && !strings.Contains(rootPath, "::") && path.Dir(rootPath) == strings.TrimRight(sourceRoot, "/") {
			episodeName := path.Base(rootPath)
			if _, ok := catalogEpisodeEvidence(rootPath); ok {
				result := []openlist.File{}
				for _, file := range files {
					parts := strings.SplitN(file.Path, "/", 2)
					if len(parts) != 2 {
						continue
					}
					if parts[0] != episodeName {
						if copyName, isCopy := catalogEpisodeFormatCopy(parts[0]); !isCopy || copyName != episodeName {
							continue
						}
					}
					result = append(result, file)
				}
				if len(result) > 0 {
					return result
				}
			}
		}
		return files
	}
	names := catalogDirectAudioNames(files)
	// Recompute the same partition an index would publish from this listing.
	// The card suffix is only an identity; a title that looks like a prefix
	// ("n006") must not choose the grouping by itself.
	plan := newCatalogSplitPlan(sourceRoot, names, catalogWorkSplitModeNames(names))
	result := []openlist.File{}
	for _, file := range files {
		if file.Kind != "audio" {
			continue
		}
		if target, ok := plan.targetFor(file.Path); ok && target.Root == rootPath {
			result = append(result, file)
		}
	}
	return result
}

// catalogEntryCode derives the work code from a split identity such as
// /creator::RJ00000001 as well as from a plain directory.
func catalogEntryCode(root string) string {
	if index := strings.LastIndex(root, "::"); index >= 0 {
		return catalogDirectoryCode(root[index+2:])
	}
	return catalogDirectoryCode(root)
}

// decideCatalogWorkSplitTx records the one-time decision for a directory that
// became a card. A card already linked to a work, a series or a relocated
// child is never split.
func decideCatalogWorkSplitTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, directoryPath string, revision int) error {
	if directoryPath == "/" {
		// The source root is the operator's scan boundary, not a creator
		// folder. Its loose files keep the root card.
		return nil
	}
	var pinned int
	if err := tx.QueryRowContext(ctx, catalogSplitPinnedSQL,
		sourceID, directoryPath).Scan(&pinned); err != nil {
		return err
	}
	mode := catalogWorkSplitSingle
	if pinned == 0 {
		// A directory named with its own work code is an authored release.
		// Its direct files are tracks or versions, never separate works.
		if catalogDirectoryCode(directoryPath) != "" {
			mode = catalogWorkSplitSingle
		} else {
			// A package copy of the parent's audio is one version, not one
			// work per file. Keep it on a single card so the same recordings
			// do not reappear as hundreds of split cards.
			duplicate, dupErr := catalogDuplicateDirectoryTx(ctx, tx, sourceID, generation, directoryPath)
			if dupErr != nil {
				return dupErr
			}
			if duplicate {
				_, execErr := tx.ExecContext(ctx, `UPDATE source_catalog_directory SET work_split=? WHERE file_source_id=? AND path=? AND generation=?`, catalogWorkSplitSingle, sourceID, directoryPath, generation)
				return execErr
			}
			rows, err := tx.QueryContext(ctx, `SELECT name FROM source_catalog_observation WHERE file_source_id=? AND directory_path=? AND revision=? AND kind='audio' ORDER BY name`, sourceID, directoryPath, revision)
			if err != nil {
				return err
			}
			names := []string{}
			for rows.Next() {
				var name string
				if err = rows.Scan(&name); err != nil {
					break
				}
				names = append(names, name)
			}
			readErr := rows.Err()
			_ = rows.Close()
			if err != nil {
				return err
			}
			if readErr != nil {
				return readErr
			}
			mode = catalogWorkSplitModeNames(names)
		}
	}
	// A split decision belongs to the current listing. completeCatalogDirectoryTx
	// runs once per completed revision, so recompute from that revision's names
	// instead of freezing the first choice forever.
	// A decision computed from one generation must never overwrite the mode
	// of a newer generation that already completed the same directory.
	_, err := tx.ExecContext(ctx, `UPDATE source_catalog_directory SET work_split=? WHERE file_source_id=? AND path=? AND generation=?`, mode, sourceID, directoryPath, generation)
	return err
}

// retireCatalogSplitParentTx hides the old creator-folder card once split
// cards exist. Relocation keeps its id and any personal reference resolvable;
// the old file rows are removed because the split cards own those files now.
func retireCatalogSplitParentTx(ctx context.Context, tx *sql.Tx, sourceID int64, parentRoot string, targetID int64) error {
	var id int64
	var workID sql.NullInt64
	var series, relocated int
	err := tx.QueryRowContext(ctx, `SELECT e.id,e.work_id,
		EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id),
		EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)
		FROM source_catalog_entry e
		WHERE e.file_source_id=? AND e.root_path=? AND e.relocated_to_id IS NULL`,
		sourceID, parentRoot).Scan(&id, &workID, &series, &relocated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// A directory card is a container, so any alias of it names the directory,
	// not one of the works inside. Re-pointing such an alias to an arbitrary
	// split card would move a personal record to an unrelated work; the parent
	// stays as a hidden alias until an explicit decision names a work.
	if workID.Valid || series != 0 || relocated != 0 || id == targetID {
		return nil
	}
	_, err = retireCatalogCardTx(ctx, tx, id, targetID)
	return err
}

// retireCatalogCardTx retires one card after re-pointing any card that was
// relocated to it. The alias rows keep their id, so every personal record that
// references the alias stays resolvable; only the shared target changes. A
// format card that already owns a reviewed work first moves that work (or its
// personal state) to the current release card.
func retireCatalogCardTx(ctx context.Context, tx *sql.Tx, id, targetID int64) (retired bool, err error) {
	// Retirement performs several dependent writes: work/personal-state
	// migration, series folding and the final relocation. A series conflict is
	// an expected review outcome, not an error, so the whole retirement must be
	// undone before the caller commits. A savepoint keeps those partial writes
	// out of the caller's transaction without requiring every check to be
	// duplicated before the first mutation.
	const savepoint = "retire_catalog_card"
	if _, err := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); err != nil {
		return false, err
	}
	released := false
	defer func() {
		if released {
			return
		}
		rollbackCtx := context.WithoutCancel(ctx)
		if _, rollbackErr := tx.ExecContext(rollbackCtx, "ROLLBACK TO SAVEPOINT "+savepoint); rollbackErr != nil {
			retired = false
			err = errors.Join(err, fmt.Errorf("rollback catalog card retirement: %w", rollbackErr))
			return
		}
		if _, releaseErr := tx.ExecContext(rollbackCtx, "RELEASE SAVEPOINT "+savepoint); releaseErr != nil {
			retired = false
			err = errors.Join(err, fmt.Errorf("release catalog card savepoint: %w", releaseErr))
		}
	}()
	moved, err := transferRetiredCatalogWorkTx(ctx, tx, id, targetID)
	if err != nil {
		return false, err
	}
	if !moved {
		return false, nil
	}
	if targetID != 0 && targetID != id {
		if err := mergeCatalogSeriesMembershipTx(ctx, tx, id, targetID); err != nil {
			if errors.Is(err, errCatalogSeriesRetireConflict) {
				// Two different series claim one successor. Keeping the stale
				// card addressable is safer than silently moving an episode.
				return false, nil
			}
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry SET relocated_to_id=? WHERE relocated_to_id=? AND id<>?`, targetID, id, targetID); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE source_catalog_entry SET file_source_id=NULL,relocated_to_id=?,updated_at=CURRENT_TIMESTAMP WHERE id=?`, nullIfZero(targetID), id); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM source_catalog_file WHERE catalog_entry_id=?`, id); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint); err != nil {
		return false, err
	}
	released = true
	return true, nil
}

// transferRetiredCatalogWorkTx preserves reviews when an old format card is
// folded into its release. A work referenced by another active catalog row is a
// separate identity and is left for review. When both cards already have
// different works, all users' states move only if no user has conflicting
// rating or note values; a conflict keeps both cards visible for owner choice.
func transferRetiredCatalogWorkTx(ctx context.Context, tx *sql.Tx, staleID, targetID int64) (bool, error) {
	var staleWork sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT work_id FROM source_catalog_entry WHERE id=?", staleID).Scan(&staleWork); err != nil {
		return false, err
	}
	if !staleWork.Valid {
		return true, nil
	}
	if targetID == 0 || targetID == staleID {
		return false, nil
	}
	var targetWork sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT work_id FROM source_catalog_entry WHERE id=?", targetID).Scan(&targetWork); err != nil {
		return false, err
	}
	if targetWork.Valid && targetWork.Int64 == staleWork.Int64 {
		_, err := tx.ExecContext(ctx, "UPDATE source_catalog_entry SET work_id=NULL WHERE id=?", staleID)
		return err == nil, err
	}
	var staleCode, targetCode string
	if err := tx.QueryRowContext(ctx, `SELECT stale.primary_code,COALESCE(target.primary_code,'')
		FROM work AS stale LEFT JOIN work AS target ON target.id=?
		WHERE stale.id=?`, targetWork, staleWork.Int64).Scan(&staleCode, &targetCode); err != nil {
		return false, err
	}
	staleCode, targetCode = normalizeWorkCode(staleCode), normalizeWorkCode(targetCode)
	if staleCode != "" && targetCode != "" && staleCode != targetCode {
		return false, nil
	}
	var shared int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_entry
		WHERE work_id=? AND id<>? AND id<>? AND relocated_to_id IS NULL)`, staleWork.Int64, staleID, targetID).Scan(&shared); err != nil {
		return false, err
	}
	if shared != 0 {
		return false, nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM shared_identity_member
		WHERE work_id=? OR identity_work_id=?)`, staleWork.Int64, staleWork.Int64).Scan(&shared); err != nil {
		return false, err
	}
	if shared != 0 {
		return false, nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_series WHERE work_id=?)`, staleWork.Int64).Scan(&shared); err != nil {
		return false, err
	}
	if shared != 0 {
		return false, nil
	}
	if !targetWork.Valid {
		if _, err := tx.ExecContext(ctx, "UPDATE source_catalog_entry SET work_id=? WHERE id=?", staleWork.Int64, targetID); err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE source_catalog_entry SET work_id=NULL WHERE id=?", staleID); err != nil {
			return false, err
		}
		return true, nil
	}

	var conflict int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM user_work_state AS old
		INNER JOIN user_work_state AS current ON current.user_id=old.user_id AND current.work_id=?
		WHERE old.work_id=? AND (
			(old.rating IS NOT NULL AND current.rating IS NOT NULL AND old.rating<>current.rating)
			OR (old.note<>'' AND current.note<>'' AND old.note<>current.note)))`,
		targetWork.Int64, staleWork.Int64).Scan(&conflict); err != nil {
		return false, err
	}
	if conflict != 0 {
		return false, nil
	}
	personalStateConflict, err := retiredCatalogPersonalStateConflictTx(ctx, tx, staleWork.Int64, targetWork.Int64)
	if err != nil {
		return false, err
	}
	if personalStateConflict {
		return false, nil
	}
	// Merge every user's state into the release work. Empty fields never
	// overwrite a stored value; favorites are unioned and the latest playback
	// timestamp survives.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_work_state(user_id,work_id,listening_status,favorite,rating,note,last_played_at,created_at,updated_at)
		SELECT old.user_id,?1,
			CASE WHEN current.user_id IS NULL OR current.listening_status='none' THEN old.listening_status ELSE current.listening_status END,
			CASE WHEN current.user_id IS NULL THEN old.favorite ELSE MAX(current.favorite,old.favorite) END,
			CASE WHEN current.rating IS NOT NULL THEN current.rating ELSE old.rating END,
			CASE WHEN current.note IS NOT NULL AND current.note<>'' THEN current.note ELSE old.note END,
			CASE
				WHEN current.last_played_at IS NULL THEN old.last_played_at
				WHEN old.last_played_at IS NULL THEN current.last_played_at
				WHEN old.last_played_at>current.last_played_at THEN old.last_played_at
				ELSE current.last_played_at
			END,
			CASE WHEN current.user_id IS NULL THEN old.created_at ELSE current.created_at END,
			CURRENT_TIMESTAMP
		FROM user_work_state AS old
		LEFT JOIN user_work_state AS current ON current.user_id=old.user_id AND current.work_id=?2
		WHERE old.work_id=?3
		ON CONFLICT(user_id,work_id) DO UPDATE SET
			listening_status=excluded.listening_status,
			favorite=excluded.favorite,
			rating=excluded.rating,
			note=excluded.note,
			last_played_at=excluded.last_played_at,
			updated_at=CURRENT_TIMESTAMP`,
		targetWork.Int64, targetWork.Int64, staleWork.Int64); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM user_work_state WHERE work_id=?", staleWork.Int64); err != nil {
		return false, err
	}
	if err := moveRetiredCatalogPersonalStateTx(ctx, tx, staleWork.Int64, targetWork.Int64); err != nil {
		return false, err
	}
	// A backup exported before retirement still names the old work. Keep that
	// alias resolvable but demote it so the target work keeps its single primary
	// reference. The catalog alias remains available as a secondary reference.
	if _, err := tx.ExecContext(ctx, `UPDATE personal_transfer_ref SET work_id=?1,is_primary=0 WHERE work_id=?2`, targetWork.Int64, staleWork.Int64); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE source_catalog_entry SET work_id=NULL WHERE id=?", staleID); err != nil {
		return false, err
	}
	return true, nil
}

func nullIfZero(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

// replacementCatalogCardTx reports the current card that inherited a stale
// card's identity. A split card is named after the lexicographically first file
// in its group, so that file is the card's identity evidence. If the current
// generation publishes exactly one card for the same relative path, that card
// took over the identity; if the path is gone or several cards claim it, the
// evidence does not name a replacement and the caller leaves the stale card for
// review. A container card has no identity file and is handled by
// retireCatalogSplitParentTx instead.
func replacementCatalogCardTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, parentRoot string, staleID int64) (int64, error) {
	var title string
	if err := tx.QueryRowContext(ctx, `SELECT title FROM source_catalog_entry WHERE id=?`, staleID).Scan(&title); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT relative_path FROM source_catalog_file
		WHERE catalog_entry_id=? AND kind='audio' ORDER BY relative_path`, staleID)
	if err != nil {
		return 0, err
	}
	identityPath := ""
	var readErr error
	for rows.Next() {
		var relativePath string
		if err = rows.Scan(&relativePath); err != nil {
			break
		}
		if identityPath == "" && strings.EqualFold(catalogFileStem(relativePath), title) {
			identityPath = relativePath
		}
	}
	readErr = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	if readErr != nil {
		return 0, readErr
	}
	if identityPath == "" {
		// The title is not one of this card's file stems, so the card was a
		// directory container and has no work identity to transfer.
		return 0, nil
	}
	rows, err = tx.QueryContext(ctx, `SELECT e.id FROM source_catalog_entry e
		JOIN source_catalog_file f ON f.catalog_entry_id=e.id
		WHERE e.file_source_id=? AND e.last_seen_generation=? AND e.id<>?
			AND (e.source_root=? OR e.root_path=?)
			AND f.relative_path=? AND f.kind='audio'
		ORDER BY e.id LIMIT 2`, sourceID, generation, staleID, parentRoot, parentRoot, identityPath)
	if err != nil {
		return 0, err
	}
	candidates := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		candidates = append(candidates, id)
	}
	readErr = rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	if readErr != nil {
		return 0, readErr
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	// The identity file is gone or several current cards claim it. Keep the old
	// card instead of guessing.
	return 0, nil
}

// catalogFormatCardRetireTargetTx returns the current release card that owns
// every audio path of a stale format card. Zero means the card must stay: its
// subtree was not fully observed, it has no unique owner, its files are not
// covered, or a series/alias identity needs review.
func catalogFormatCardRetireTargetTx(ctx context.Context, tx *sql.Tx, sourceID, generation, cardID int64, root string) (int64, error) {
	// The format directory must have been observed completely in this
	// generation before its old card can be retired. A failed or restricted
	// subtree proves nothing: its audio was not republished, so deleting the
	// old rows would hide playable files until the source recovers. A split
	// card root carries a "::key" suffix; the directory row uses the path
	// before it.
	directory := root
	if index := strings.Index(directory, "::"); index >= 0 {
		directory = directory[:index]
	}
	incomplete, err := catalogRootIncomplete(ctx, tx, sourceID, generation, directory)
	if err != nil {
		return 0, err
	}
	if incomplete {
		return 0, nil
	}
	// The owner is the catalog root assigned by the current directory pass,
	// not necessarily path.Dir. A format folder can sit below an unadmitted
	// wrapper, so its parent path may have no card while the release card is
	// a grandparent. Using the recorded root also prevents attaching the old
	// card to an unrelated work that happens to share the parent path.
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT catalog_root FROM source_catalog_directory
		WHERE file_source_id=? AND generation=? AND path=?`, sourceID, generation, directory).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if owner == "" || owner == directory || owner == root {
		return 0, nil
	}
	var parentID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM source_catalog_entry
		WHERE file_source_id=? AND root_path=? AND relocated_to_id IS NULL AND last_seen_generation=?`,
		sourceID, owner, generation).Scan(&parentID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	covered, err := catalogCardFilesCoveredTx(ctx, tx, cardID, parentID, directory, owner)
	if err != nil {
		return 0, err
	}
	if !covered {
		// The current card does not publish every audio path the old card
		// owned. Retiring it would hide playable files, so keep it for review.
		return 0, nil
	}
	var blocked int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_entry e WHERE e.id=? AND (
			EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
		OR EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)))`, cardID).Scan(&blocked); err != nil {
		return 0, err
	}
	if blocked != 0 {
		return 0, nil
	}
	return parentID, nil
}

// catalogCardFilesCoveredTx reports whether every audio path of a stale format
// card is also published by the current owner card. Both sides are compared as
// absolute source paths: a stale card that carries its own directory stores the
// path relative to that directory, while the owner may be a grandparent such as
// /Creator with /Creator/Release/MP3 poured into one card.
func catalogCardFilesCoveredTx(ctx context.Context, tx *sql.Tx, staleID, ownerID int64, staleRoot, ownerRoot string) (bool, error) {
	staleBase := `COALESCE(NULLIF(old.source_root,''),?1)`
	currentBase := `COALESCE(NULLIF(cur.source_root,''),?2)`
	stalePath := `(CASE WHEN ` + staleBase + `='/' THEN '/' || old.relative_path ELSE ` + staleBase + ` || '/' || old.relative_path END)`
	currentPath := `(CASE WHEN ` + currentBase + `='/' THEN '/' || cur.relative_path ELSE ` + currentBase + ` || '/' || cur.relative_path END)`
	var missing int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM source_catalog_file old
		WHERE old.catalog_entry_id=?3 AND old.kind='audio'
			AND NOT EXISTS(SELECT 1 FROM source_catalog_file cur
				WHERE cur.catalog_entry_id=?4 AND cur.kind='audio'
					AND `+currentPath+`=`+stalePath+`))`,
		staleRoot, ownerRoot, staleID, ownerID).Scan(&missing)
	return missing == 0, err
}

// retireOrphanSplitRootsBatch retires split cards whose root is no longer
// split in the current generation. A full index only builds a plan for roots
// that still split, so without this pass the cards of a former creator split
// would keep their files and stay visible beside the single card that replaced
// them. A card with a work, a series membership or an incoming alias is
// retained for explicit review, and a root that is not fully observed is
// skipped. Each call handles at most 100 roots so the write lock stays short
// and the workflow can continue on the next invocation.
func (s *Server) retireOrphanSplitRootsBatch(ctx context.Context, job workflowJobRecord, sourceID int64) (int, error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return 0, err
	}
	// The pass is resumable: a root that is not fully observed is skipped but
	// the cursor still moves past it, so a run with a permanent upstream failure
	// terminates and the next generation retries the root.
	var cursor int64
	if err = tx.QueryRowContext(ctx, `SELECT orphan_cleanup_cursor FROM source_catalog_scan WHERE file_source_id=?`, sourceID).Scan(&cursor); err != nil {
		return 0, err
	}
	// The root is the unit of the pass, not of the decision. A card that carries
	// a work, a series membership or an alias keeps its identity and is skipped
	// per card below; skipping the whole root would also keep the unrelated
	// duplicates beside it. Roots are ordered by their first stale id, which the
	// cursor advances past even when every card in that root is skipped.
	rows, err := tx.QueryContext(ctx, `SELECT e.source_root,MIN(e.id) AS first_id FROM source_catalog_entry e
		WHERE e.file_source_id=? AND e.source_root<>'' AND e.relocated_to_id IS NULL
			AND e.last_seen_generation<>?
		GROUP BY e.source_root HAVING first_id>?
		ORDER BY first_id LIMIT ?`,
		sourceID, job.RunID, cursor, catalogRetireRoots)
	if err != nil {
		return 0, err
	}
	roots := []string{}
	nextCursor := cursor
	for rows.Next() {
		var root string
		var firstID int64
		if err = rows.Scan(&root, &firstID); err != nil {
			break
		}
		roots = append(roots, root)
		if firstID > nextCursor {
			nextCursor = firstID
		}
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	if readErr != nil {
		return 0, readErr
	}
	if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET orphan_cleanup_cursor=? WHERE file_source_id=?`, nextCursor, sourceID); err != nil {
		return 0, err
	}
	generation := job.RunID
	for _, root := range roots {
		var status string
		if err = tx.QueryRowContext(ctx, `SELECT status FROM source_catalog_directory
			WHERE file_source_id=? AND generation=? AND path=?`, sourceID, generation, root).Scan(&status); errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if status != "complete" {
			continue
		}
		incomplete, err := catalogRootIncomplete(ctx, tx.Tx, sourceID, generation, root)
		if err != nil {
			return 0, err
		}
		if incomplete {
			continue
		}
		keep := map[int64]bool{}
		rows, err := tx.QueryContext(ctx, `SELECT id FROM source_catalog_entry
			WHERE file_source_id=? AND source_root=? AND last_seen_generation=?`, sourceID, root, generation)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			keep[id] = true
		}
		readErr := rows.Err()
		_ = rows.Close()
		if err != nil {
			return 0, err
		}
		if readErr != nil {
			return 0, readErr
		}
		if err = retireStaleSplitCardsTx(ctx, tx.Tx, sourceID, generation, root, keep); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(roots), nil
}

// markStaleSnapshotCardsTx marks cards a finite scan no longer publishes as
// missing. The scan rewrites every card of the root in one transaction, so a
// child card outside the written set describes content the source no longer
// lists. The card itself, its work link and its personal records are kept; a
// later scan that lists the path again restores it. This never runs for a
// failed scan: the caller returns before publishing in that case.
func markStaleSnapshotCardsTx(ctx context.Context, tx *sql.Tx, sourceID int64, root string, keep map[int64]bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,root_path FROM source_catalog_entry
		WHERE file_source_id=? AND relocated_to_id IS NULL
			AND (source_root=? OR root_path=? OR substr(root_path,1,length(?)+1)=? || '/')
		ORDER BY id`, sourceID, root, root, root, root)
	if err != nil {
		return err
	}
	type staleCard struct {
		id   int64
		root string
	}
	stale := []staleCard{}
	for rows.Next() {
		var card staleCard
		if err = rows.Scan(&card.id, &card.root); err != nil {
			break
		}
		if keep[card.id] || card.root == root {
			continue
		}
		stale = append(stale, card)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if readErr != nil {
		return readErr
	}
	for _, card := range stale {
		if err = setCatalogEntryStatusTx(ctx, tx, card.id, "missing"); err != nil {
			return err
		}
	}
	return nil
}

// retireStaleSplitCardsTx hides split cards for this root that the current
// generation did not publish. A directory that used to hold separate works can
// become one audiobook (or change its grouping); the old cards would otherwise
// stay visible beside the new one. A card with a work or a series member keeps
// its identity and is left for explicit review. An alias child is re-pointed to
// the card that inherited the files, so the old card can retire without
// stranding the child's personal records.
func retireStaleSplitCardsTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, parentRoot string, keep map[int64]bool) error {
	// The parent card of a former split has source_root='' while its split
	// cards carry the real directory. Include both shapes so a split -> single
	// -> split sequence cannot leave the intermediate parent visible.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM source_catalog_entry
		WHERE file_source_id=? AND (source_root=? OR root_path=?) AND relocated_to_id IS NULL AND last_seen_generation<>?`,
		sourceID, parentRoot, parentRoot, generation)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if keep[id] {
			continue
		}
		var blocked int
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_entry e WHERE e.id=? AND (
			e.work_id IS NOT NULL
			OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)))`, id).Scan(&blocked); err != nil {
			return err
		}
		if blocked != 0 {
			continue
		}
		var children int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM source_catalog_entry WHERE relocated_to_id=?`, id).Scan(&children); err != nil {
			return err
		}
		target, err := replacementCatalogCardTx(ctx, tx, sourceID, generation, parentRoot, id)
		if err != nil {
			return err
		}
		if children != 0 && target == 0 {
			// The old card still anchors aliases but no current card carries
			// its works. Retiring it would strand those aliases, so leave the
			// conflict visible for review instead of guessing a target.
			continue
		}
		if _, err = retireCatalogCardTx(ctx, tx, id, target); err != nil {
			return err
		}
	}
	return nil
}

// A directory card of an older generation can describe the same work that the
// current generation publishes at a deeper directory. The old card carried the
// format subdirectory's paths; the rescan publishes that subdirectory as the
// card. Both list the same audio, so only the current card may stay visible.
// The container is retired only when exactly one current card covers every one
// of its audio paths; a partial or ambiguous move stays for review.
func retireCoveredContainerCardsTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,root_path FROM source_catalog_entry
		WHERE file_source_id=? AND source_root='' AND relocated_to_id IS NULL AND last_seen_generation<>?`,
		sourceID, generation)
	if err != nil {
		return err
	}
	type containerCard struct {
		id   int64
		root string
	}
	cards := []containerCard{}
	for rows.Next() {
		var card containerCard
		if err = rows.Scan(&card.id, &card.root); err != nil {
			break
		}
		cards = append(cards, card)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, card := range cards {
		target, err := catalogCoveredContainerRetireTargetTx(ctx, tx, sourceID, generation, card.id, card.root)
		if err != nil {
			return err
		}
		if target == 0 {
			continue
		}
		if _, err = retireCatalogCardTx(ctx, tx, card.id, target); err != nil {
			return err
		}
	}
	return nil
}

// catalogCoveredContainerRetireTargetTx returns the one deeper current card
// that owns every audio path of a stale container. Zero keeps the container.
func catalogCoveredContainerRetireTargetTx(ctx context.Context, tx *sql.Tx, sourceID, generation, cardID int64, root string) (int64, error) {
	var blocked int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_entry e WHERE e.id=? AND (
		e.work_id IS NOT NULL
		OR EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
		OR EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)))`, cardID).Scan(&blocked); err != nil {
		return 0, err
	}
	if blocked != 0 {
		return 0, nil
	}
	incomplete, err := catalogRootIncomplete(ctx, tx, sourceID, generation, root)
	if err != nil {
		return 0, err
	}
	if incomplete {
		return 0, nil
	}
	return coveredContainerTargetTx(ctx, tx, sourceID, generation, root, cardID)
}

// catalogDuplicateCardRetireTargetTx retires a stale card whose directory is a
// duplicate copy of its current parent. The current generation suppresses the
// child card, so the parent is the only safe successor. Series membership and
// incoming aliases are identity boundaries and keep the stale card; a work is
// left to the normal conflict-aware retirement path.
func catalogDuplicateCardRetireTargetTx(ctx context.Context, tx *sql.Tx, sourceID, generation, cardID int64, root string) (int64, error) {
	parent := path.Dir(root)
	if root == "/" || parent == "/" || parent == "." || parent == root {
		return 0, nil
	}
	var seriesOrAlias int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=?
		UNION ALL
		SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=? )`, cardID, cardID).Scan(&seriesOrAlias); err != nil {
		return 0, err
	}
	if seriesOrAlias != 0 {
		return 0, nil
	}
	decision, err := catalogDuplicateDirectoryDecisionTx(ctx, tx, sourceID, generation, root)
	if err != nil || !decision.Duplicate {
		return 0, err
	}
	var targetID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM source_catalog_entry
		WHERE file_source_id=? AND root_path=? AND relocated_to_id IS NULL
			AND last_seen_generation=?`, sourceID, parent, generation).Scan(&targetID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(decision.Unique) > 0 {
		covered, err := catalogDuplicateUniqueCoveredByTargetTx(ctx, tx, targetID, parent, generation, decision.Unique)
		if err != nil || !covered {
			return 0, err
		}
	}
	return targetID, nil
}

// catalogDuplicateUniqueCoveredByTargetTx reports whether every file that was
// not part of the duplicate set is already published on the canonical parent
// card. The bonus file is often the only reason a copy still has a card.
func catalogDuplicateUniqueCoveredByTargetTx(ctx context.Context, tx *sql.Tx, targetID int64, parent string, generation int64, files []catalogAudioFile) (bool, error) {
	prefix := strings.TrimSuffix(parent, "/") + "/"
	for _, file := range files {
		relative := strings.TrimPrefix(path.Join(file.Directory, file.Name), prefix)
		var exists int
		var err error
		if hash := strings.TrimSpace(file.Hash); hash != "" {
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(
				SELECT 1 FROM source_catalog_file
				WHERE catalog_entry_id=? AND relative_path=? AND kind='audio'
						AND content_hash=? AND observed_generation=?)`, targetID, relative, hash, generation).Scan(&exists)
		} else {
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(
				SELECT 1 FROM source_catalog_file
				WHERE catalog_entry_id=? AND relative_path=? AND kind='audio'
						AND size_bytes=? AND observed_generation=?)`, targetID, relative, file.Size, generation).Scan(&exists)
		}
		if err != nil {
			return false, err
		}
		if exists == 0 {
			return false, nil
		}
	}
	return true, nil
}

// catalogRetireCardsPerBatch bounds both the rows examined in one transaction
// and the retirements one call may commit.
const catalogRetireCardsPerBatch = 100

type catalogRetirePass int

const (
	catalogRetireCovered catalogRetirePass = iota
	catalogRetireFormat
	catalogRetireDuplicate
)

// retireCatalogCardsBatch moves stale-card retirement out of the final publish
// transaction. It examines at most 100 rows per transaction and commits each
// page independently. The cursor is local to this call; a later call restarts
// from the lowest id, and already-retired rows simply disappear. Every step is
// idempotent, so a restart repeats checks without duplicating a retirement.
//
// Pass order matches the former in-publish order: covered containers first,
// then format cards. A full covered pass returns without starting the format
// pass, so a large covered backlog does not let format cards overtake it.
//
// A page whose cards cannot retire (incomplete subtree, partial coverage or an
// ambiguous owner) still advances the local cursor, so the pass reaches later
// rows instead of stalling on a blocked prefix. The budget counts committed
// retirements, not examined rows; one call may therefore scan several pages.
func (s *Server) retireCatalogCardsBatch(ctx context.Context, job workflowJobRecord, sourceID int64) (retired int, done bool, err error) {
	budget := catalogRetireCardsPerBatch
	for _, pass := range []catalogRetirePass{catalogRetireCovered, catalogRetireFormat, catalogRetireDuplicate} {
		if budget == 0 {
			return retired, false, nil
		}
		n, exhausted, passErr := s.retireCatalogCardPass(ctx, job, sourceID, pass, budget)
		retired += n
		budget -= n
		if passErr != nil {
			return retired, false, passErr
		}
		if !exhausted {
			return retired, false, nil
		}
	}
	return retired, true, nil
}

func (s *Server) retireCatalogCardPass(ctx context.Context, job workflowJobRecord, sourceID int64, pass catalogRetirePass, budget int) (retired int, exhausted bool, err error) {
	var cursor int64
	for {
		remaining := budget - retired
		n, lastID, more, pageErr := s.retireCatalogCardPage(ctx, job, sourceID, pass, cursor, remaining)
		retired += n
		if pageErr != nil {
			return retired, false, pageErr
		}
		if retired >= budget {
			return retired, false, nil
		}
		if !more {
			return retired, true, nil
		}
		if lastID <= cursor {
			return retired, false, errors.New("catalog retire cursor did not advance")
		}
		cursor = lastID
	}
}

func (s *Server) retireCatalogCardPage(ctx context.Context, job workflowJobRecord, sourceID int64, pass catalogRetirePass, cursor int64, retireLimit int) (retired int, lastID int64, more bool, err error) {
	tx, release, err := beginTxWithDatabaseBusyRetry(ctx, s.db)
	if err != nil {
		return 0, cursor, false, err
	}
	defer release()
	if err = verifyCatalogLease(ctx, tx.Tx, job); err != nil {
		return 0, cursor, false, err
	}
	query := `SELECT e.id,e.root_path FROM source_catalog_entry e
		WHERE e.file_source_id=? AND e.relocated_to_id IS NULL AND e.last_seen_generation<>? AND e.id>?
			AND e.source_root='' AND e.work_id IS NULL
			AND NOT EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
			AND NOT EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)
		ORDER BY e.id LIMIT ?`
	if pass == catalogRetireFormat {
		query = `SELECT e.id,e.root_path FROM source_catalog_entry e
			WHERE e.file_source_id=? AND e.relocated_to_id IS NULL AND e.last_seen_generation<>? AND e.id>?
				AND NOT EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
				AND NOT EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)
			ORDER BY e.id LIMIT ?`
	} else if pass == catalogRetireDuplicate {
		query = `SELECT e.id,e.root_path FROM source_catalog_entry e
			WHERE e.file_source_id=? AND e.relocated_to_id IS NULL AND e.last_seen_generation<>? AND e.id>?
				AND e.source_root=''
				AND NOT EXISTS(SELECT 1 FROM catalog_series_member m WHERE m.catalog_entry_id=e.id)
				AND NOT EXISTS(SELECT 1 FROM source_catalog_entry r WHERE r.relocated_to_id=e.id)
			ORDER BY e.id LIMIT ?`
	}
	rows, err := tx.QueryContext(ctx, query, sourceID, job.RunID, cursor, catalogRetireCardsPerBatch)
	if err != nil {
		return 0, cursor, false, err
	}
	type retireCard struct {
		id   int64
		root string
	}
	cards := []retireCard{}
	for rows.Next() {
		var card retireCard
		if err = rows.Scan(&card.id, &card.root); err != nil {
			break
		}
		cards = append(cards, card)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return 0, cursor, false, err
	}
	if readErr != nil {
		return 0, cursor, false, readErr
	}
	if len(cards) > 0 {
		lastID = cards[len(cards)-1].id
	} else {
		lastID = cursor
	}
	more = len(cards) == catalogRetireCardsPerBatch
	for _, card := range cards {
		if retireLimit > 0 && retired >= retireLimit {
			break
		}
		var target int64
		if pass == catalogRetireCovered {
			target, err = catalogCoveredContainerRetireTargetTx(ctx, tx.Tx, sourceID, job.RunID, card.id, card.root)
		} else if pass == catalogRetireDuplicate {
			target, err = catalogDuplicateCardRetireTargetTx(ctx, tx.Tx, sourceID, job.RunID, card.id, card.root)
		} else {
			// An episode's format copy ("Episode 1 (MP3)") is a part of that
			// episode too; only its root name carries an episode marker.
			base := path.Base(card.root)
			if !catalogWorkPartDirectory(base) {
				if _, formatCopy := catalogEpisodeFormatCopy(base); !formatCopy {
					continue
				}
			}
			target, err = catalogFormatCardRetireTargetTx(ctx, tx.Tx, sourceID, job.RunID, card.id, card.root)
		}
		if err != nil {
			return retired, lastID, false, err
		}
		if target == 0 {
			continue
		}
		retiredCard, retireErr := retireCatalogCardTx(ctx, tx.Tx, card.id, target)
		if retireErr != nil {
			return retired, lastID, false, retireErr
		}
		if retiredCard {
			retired++
		}
	}
	if retired > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE source_catalog_scan SET indexed=(SELECT count(*) FROM source_catalog_entry WHERE file_source_id=? AND last_seen_generation=?) WHERE file_source_id=?`, sourceID, job.RunID, sourceID); err != nil {
			return retired, lastID, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return retired, lastID, false, err
	}
	return retired, lastID, more, nil
}

// coveredContainerTargetTx reports the one current card that owns every audio
// path of a stale container card. Paths are compared after removing the deeper
// card's directory prefix, because the container stored them relative to
// itself. Two candidates or a missing path leave the card alone.
func coveredContainerTargetTx(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string, staleID int64) (int64, error) {
	parent := []string{}
	rows, err := tx.QueryContext(ctx, `SELECT relative_path FROM source_catalog_file
		WHERE catalog_entry_id=? AND kind='audio'`, staleID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var relativePath string
		if err = rows.Scan(&relativePath); err != nil {
			break
		}
		parent = append(parent, relativePath)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, err
	}
	if len(parent) == 0 {
		return 0, nil
	}

	rows, err = tx.QueryContext(ctx, `SELECT id,root_path FROM source_catalog_entry
		WHERE file_source_id=? AND relocated_to_id IS NULL AND last_seen_generation=?
			AND substr(root_path,1,length(?)+1)=? || '/'`,
		sourceID, generation, root, root)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		id   int64
		root string
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.root); err != nil {
			break
		}
		candidates = append(candidates, c)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return 0, err
	}

	matches := []int64{}
	for _, c := range candidates {
		prefix := strings.TrimPrefix(c.root, root+"/") + "/"
		owned := map[string]bool{}
		fileRows, err := tx.QueryContext(ctx, `SELECT relative_path FROM source_catalog_file
			WHERE catalog_entry_id=? AND kind='audio'`, c.id)
		if err != nil {
			return 0, err
		}
		for fileRows.Next() {
			var relativePath string
			if err = fileRows.Scan(&relativePath); err != nil {
				break
			}
			owned[relativePath] = true
		}
		if err == nil {
			err = fileRows.Err()
		}
		_ = fileRows.Close()
		if err != nil {
			return 0, err
		}
		covered := 0
		for _, relativePath := range parent {
			if strings.HasPrefix(relativePath, prefix) && owned[strings.TrimPrefix(relativePath, prefix)] {
				covered++
			}
		}
		if covered == len(parent) {
			matches = append(matches, c.id)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return 0, nil
}

// catalogSnapshotSplitMode applies the same decision to a finite scan, with
// the same protection for a card that already owns a work or series.
func (s *Server) catalogSnapshotSplitMode(ctx context.Context, sourceID int64, root string, files []openlist.File) (string, error) {
	if root == "/" {
		return catalogWorkSplitSingle, nil
	}
	var pinned int
	if err := s.db.QueryRowContext(ctx, catalogSplitPinnedSQL,
		sourceID, root).Scan(&pinned); err != nil {
		return "", err
	}
	if pinned != 0 {
		return catalogWorkSplitSingle, nil
	}
	if catalogDirectoryCode(root) != "" {
		return catalogWorkSplitSingle, nil
	}
	return catalogWorkSplitMode(files), nil
}

// catalogSplitPlanForRoot loads the split plan for one indexed root. Direct
// audio names are read once per publish batch, so a batch boundary cannot
// change a group title. The plan is the same one a finite scan and a card
// refresh use; only its root-specific evidence differs.
func (s *Server) catalogSplitPlanForRoot(ctx context.Context, tx *sql.Tx, sourceID, generation int64, root string) (catalogSplitPlan, bool, error) {
	var pinned int
	if err := tx.QueryRowContext(ctx, catalogSplitPinnedSQL,
		sourceID, root).Scan(&pinned); err != nil {
		return catalogSplitPlan{}, false, err
	}
	if pinned != 0 {
		return catalogSplitPlan{}, false, nil
	}
	var mode string
	err := tx.QueryRowContext(ctx, `SELECT work_split FROM source_catalog_directory WHERE file_source_id=? AND generation=? AND path=?`, sourceID, generation, root).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogSplitPlan{}, false, nil
	}
	if err != nil {
		return catalogSplitPlan{}, false, err
	}
	if mode != catalogWorkSplitPrefix && mode != catalogWorkSplitFile && mode != catalogWorkSplitEpisode {
		return catalogSplitPlan{}, false, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT o.name FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE o.file_source_id=? AND o.directory_path=? AND o.revision=d.revision AND d.generation=? AND o.kind='audio'
		ORDER BY o.name`, sourceID, root, generation)
	if err != nil {
		return catalogSplitPlan{}, false, err
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		names = append(names, name)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return catalogSplitPlan{}, false, err
	}
	if readErr != nil {
		return catalogSplitPlan{}, false, readErr
	}
	plan := newCatalogSplitPlan(root, names, mode)
	// Only audio with a card of its own can be retired with the parent. A
	// nested file under an unrelated folder stays on the parent card, so the
	// parent must remain published.
	rows, err = tx.QueryContext(ctx, `SELECT o.directory_path,o.name,o.kind FROM source_catalog_observation o
		JOIN source_catalog_directory d ON d.file_source_id=o.file_source_id AND d.path=o.directory_path
		WHERE d.file_source_id=? AND d.generation=? AND d.catalog_root=? AND o.revision=d.revision AND o.kind<>'folder'
		ORDER BY o.directory_path,o.name`, sourceID, generation, root)
	if err != nil {
		return catalogSplitPlan{}, false, err
	}
	parentNeeded := 0
	for rows.Next() {
		var dir, name, kind string
		if err = rows.Scan(&dir, &name, &kind); err != nil {
			break
		}
		if kind != "audio" {
			// A readme, cover or subtitle describes the whole release. Keep the
			// parent card so its credit evidence and artwork survive a split.
			parentNeeded = 1
			break
		}
		rel := strings.TrimPrefix(path.Join(dir, name), strings.TrimRight(root, "/")+"/")
		if _, split := plan.targetFor(rel); split {
			continue
		}
		parentNeeded = 1
		break
	}
	readErr = rows.Err()
	_ = rows.Close()
	if err != nil {
		return catalogSplitPlan{}, false, err
	}
	if readErr != nil {
		return catalogSplitPlan{}, false, readErr
	}
	return plan, parentNeeded != 0, nil
}

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
