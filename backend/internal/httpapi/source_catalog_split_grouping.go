package httpapi

import (
	"sort"
	"strconv"
	"strings"
)

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
