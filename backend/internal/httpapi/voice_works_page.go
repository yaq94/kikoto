package httpapi

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// voiceWorksQuery selects one page of a voice actor's works.
type voiceWorksQuery struct {
	Page     int
	PageSize int
	Query    string
	// Availability is all, available, local, remote, or missing.
	Availability string
	// Sort is release, code, title, rating, or sales; empty sorts by release.
	Sort string
	// Direction is asc or desc; empty sorts descending.
	Direction string
}

type voiceWorksPage struct {
	PersonID int64           `json:"personId"`
	Works    []voiceWorkView `json:"works"`
	Page     int             `json:"page"`
	PageSize int             `json:"pageSize"`
	Total    int             `json:"total"`
}

// voiceWorkView is one work of a voice actor: a known work carrying every
// remote observation of it, or a remote catalog work no local credit names.
// Exactly one of Known and Remote is set, and the view serializes as that
// work's own shape.
type voiceWorkView struct {
	Known  *voiceKnownWork
	Remote *voiceRemoteWork
	// RemoteObservations belongs to a remote catalog work; a known work keeps
	// its own.
	RemoteObservations []voiceRemoteObservation
}

func (view voiceWorkView) MarshalJSON() ([]byte, error) {
	if view.Known != nil {
		return json.Marshal(view.Known)
	}
	return json.Marshal(struct {
		*voiceRemoteWork
		RemoteObservations []voiceRemoteObservation `json:"remoteObservations"`
	}{view.Remote, view.RemoteObservations})
}

func parseVoiceWorksQuery(r *http.Request) voiceWorksQuery {
	pageSize := queryInt(r, "pageSize", 24)
	if pageSize > 100 {
		pageSize = 24
	}
	availability := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("availability")))
	switch availability {
	case "available", "local", "remote", "missing":
	default:
		availability = "all"
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 200 {
		query = query[:200]
	}
	sortKey, direction := normalizeCircleWorksSort(r.URL.Query().Get("sort"), r.URL.Query().Get("direction"))
	return voiceWorksQuery{
		Page:         queryInt(r, "page", 1),
		PageSize:     pageSize,
		Query:        query,
		Availability: availability,
		Sort:         sortKey,
		Direction:    direction,
	}
}

// loadVoiceWorksPage merges the voice actor's known works with the persisted
// remote catalog, then filters, orders, and pages the result. A page past the
// end resolves to the last page.
func (s *Server) loadVoiceWorksPage(ctx context.Context, userID, personID int64, worksQuery voiceWorksQuery) (voiceWorksPage, error) {
	known, err := s.loadVoiceKnownWorks(ctx, userID, personID)
	if err != nil {
		return voiceWorksPage{}, err
	}
	sets, err := s.loadVoiceCatalogMatches(ctx, personID)
	if err != nil {
		return voiceWorksPage{}, err
	}
	works := filterVoiceWorkViews(mergeVoiceWorkViews(known, sets), worksQuery.Query, worksQuery.Availability)
	sortVoiceWorkViews(works, worksQuery.Sort, worksQuery.Direction)
	page, start, end := creatorPageBounds(worksQuery.Page, worksQuery.PageSize, len(works))
	return voiceWorksPage{
		PersonID: personID, Works: works[start:end], Page: page,
		PageSize: minInt(maxInt(worksQuery.PageSize, 1), 100), Total: len(works),
	}, nil
}

// mergeVoiceWorkViews folds each remote catalog work into the known work with
// the same code and keeps the rest as remote-only works, one per code. A source
// whose last pass failed still contributes the rows it persisted earlier.
func mergeVoiceWorkViews(known []voiceKnownWork, sets []voiceRemoteSourceSet) []voiceWorkView {
	remoteWorks := []voiceRemoteWork{}
	for _, set := range sets {
		remoteWorks = append(remoteWorks, set.Works...)
	}
	remoteByCode := map[string][]voiceRemoteWork{}
	for _, work := range remoteWorks {
		if key := voiceWorkCodeKey(work.PrimaryCode); key != "" {
			remoteByCode[key] = append(remoteByCode[key], work)
		}
	}
	consumed := map[string]bool{}
	views := make([]voiceWorkView, 0, len(known)+len(remoteByCode))
	for index := range known {
		work := known[index]
		key := voiceWorkCodeKey(work.PrimaryCode)
		matches := remoteByCode[key]
		preferredCodes := []string{work.RemoteCode}
		for _, observation := range work.RemoteObservations {
			if voiceSourceAvailabilityStatus(observation.Status) == "available" {
				preferredCodes = append(preferredCodes, observation.RemoteCode)
			}
		}
		preferredCodes = append(preferredCodes, work.PrimaryCode)
		observations := orderVoiceRemoteObservations(
			unionVoiceRemoteObservations(work.RemoteObservations, catalogRemoteObservations(matches)), preferredCodes,
		)
		if len(matches) > 0 {
			consumed[key] = true
		}
		if len(observations) > 0 {
			work.RemoteObservations = observations
			work.SourceTags = sourceTagsWithObservations(work.SourceTags, observations)
		}
		views = append(views, voiceWorkView{Known: &work})
	}
	for index := range remoteWorks {
		work := remoteWorks[index]
		key := voiceWorkCodeKey(work.PrimaryCode)
		if consumed[key] {
			continue
		}
		consumed[key] = true
		matches := remoteByCode[key]
		if len(matches) == 0 {
			matches = []voiceRemoteWork{work}
		}
		views = append(views, voiceWorkView{
			Remote: &work,
			RemoteObservations: orderVoiceRemoteObservations(
				catalogRemoteObservations(matches), []string{work.RemoteCode, work.PrimaryCode},
			),
		})
	}
	return views
}

// catalogRemoteObservations reads one observation per source and remote code
// from the catalog rows of a work.
func catalogRemoteObservations(works []voiceRemoteWork) []voiceRemoteObservation {
	observations := []voiceRemoteObservation{}
	seen := map[string]bool{}
	for _, work := range works {
		remoteCode := strings.TrimSpace(firstNonEmpty(work.RemoteCode, work.PrimaryCode))
		if work.SourceID <= 0 || remoteCode == "" {
			continue
		}
		observation := voiceRemoteObservation{
			SourceID: work.SourceID, SourceCode: work.SourceCode, SourceName: work.SourceName, RemoteCode: remoteCode,
		}
		key := voiceRemoteObservationKey(observation)
		if seen[key] {
			continue
		}
		seen[key] = true
		status := work.Availability
		if status == "" && work.RemotePlayable {
			status = "available"
		}
		observation.Status = voiceSourceAvailabilityStatus(status)
		observations = append(observations, observation)
	}
	return observations
}

// unionVoiceRemoteObservations keeps one observation per source and remote
// code. A later observation replaces an earlier one unless it reports a weaker
// status.
func unionVoiceRemoteObservations(groups ...[]voiceRemoteObservation) []voiceRemoteObservation {
	result := []voiceRemoteObservation{}
	indexes := map[string]int{}
	for _, group := range groups {
		for _, observation := range group {
			observation.RemoteCode = strings.TrimSpace(observation.RemoteCode)
			if observation.SourceID <= 0 || observation.RemoteCode == "" {
				continue
			}
			observation.Status = voiceSourceAvailabilityStatus(observation.Status)
			key := voiceRemoteObservationKey(observation)
			index, ok := indexes[key]
			if !ok {
				indexes[key] = len(result)
				result = append(result, observation)
				continue
			}
			if voiceSourceStatusPriority(observation.Status) >= voiceSourceStatusPriority(result[index].Status) {
				result[index] = observation
			}
		}
	}
	return result
}

// orderVoiceRemoteObservations moves the observations of the preferred remote
// codes first, in the order those codes are given.
func orderVoiceRemoteObservations(observations []voiceRemoteObservation, preferredCodes []string) []voiceRemoteObservation {
	preferred := make([]string, 0, len(preferredCodes))
	for _, code := range preferredCodes {
		if key := voiceWorkCodeKey(code); key != "" {
			preferred = append(preferred, key)
		}
	}
	rank := func(observation voiceRemoteObservation) int {
		key := voiceWorkCodeKey(observation.RemoteCode)
		for index, code := range preferred {
			if code == key {
				return index
			}
		}
		return len(preferred)
	}
	sort.SliceStable(observations, func(left, right int) bool {
		return rank(observations[left]) < rank(observations[right])
	})
	return observations
}

// sourceTagsWithObservations adds a source tag for every observed remote
// source and raises a tag whose observation reports a stronger status.
func sourceTagsWithObservations(tags []circleSourceStat, observations []voiceRemoteObservation) []circleSourceStat {
	result := make([]circleSourceStat, 0, len(tags)+len(observations))
	indexes := map[string]int{}
	place := func(tag circleSourceStat, replace func(existing circleSourceStat) bool) {
		key := tag.Key
		if tag.SourceID != nil && *tag.SourceID > 0 {
			key = "source:" + strconv.FormatInt(*tag.SourceID, 10)
		}
		index, ok := indexes[key]
		if !ok {
			indexes[key] = len(result)
			result = append(result, tag)
			return
		}
		if replace(result[index]) {
			result[index] = tag
		}
	}
	for _, tag := range tags {
		place(tag, func(circleSourceStat) bool { return true })
	}
	for _, observation := range observations {
		sourceID := observation.SourceID
		tag := circleSourceStat{
			Key:         "source:" + strconv.FormatInt(sourceID, 10),
			SourceID:    &sourceID,
			DisplayName: firstNonEmpty(observation.SourceName, observation.SourceCode, "Remote source"),
			Status:      voiceSourceAvailabilityStatus(observation.Status),
		}
		if tag.Status == "available" {
			tag.Count = 1
		}
		place(tag, func(existing circleSourceStat) bool {
			return voiceSourceStatusPriority(tag.Status) > voiceSourceStatusPriority(existing.Status)
		})
	}
	return result
}

func filterVoiceWorkViews(views []voiceWorkView, query, availability string) []voiceWorkView {
	needle := strings.ToLower(strings.TrimSpace(query))
	result := make([]voiceWorkView, 0, len(views))
	for _, view := range views {
		if needle != "" && !view.matchesQuery(needle) {
			continue
		}
		local, remote, cache := view.availability()
		switch availability {
		case "available":
			if !local && !remote && !cache {
				continue
			}
		case "local":
			if !local {
				continue
			}
		case "remote":
			if !remote {
				continue
			}
		case "missing":
			if local || remote || cache || !view.observedMissing() {
				continue
			}
		}
		result = append(result, view)
	}
	return result
}

// matchesQuery reports whether the lowercased needle occurs in the code,
// title, circle, a tag, or a personal tag of the work.
func (view voiceWorkView) matchesQuery(needle string) bool {
	values := []string{}
	if view.Known != nil {
		values = append(values, view.Known.PrimaryCode, view.Known.Title, view.Known.Circle)
		values = append(values, view.Known.Tags...)
		for _, tag := range view.Known.UserTags {
			values = append(values, tag.Name)
		}
	} else {
		values = append(values, view.Remote.PrimaryCode, view.Remote.Title, view.Remote.Circle)
		values = append(values, view.Remote.Tags...)
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

// availability reports whether the work has a local copy, an available remote
// source, and a cached copy.
func (view voiceWorkView) availability() (local, remote, cache bool) {
	if view.Known != nil {
		remote = view.Known.Remote
		for _, observation := range view.Known.RemoteObservations {
			remote = remote || observation.Status == "available"
		}
		for _, tag := range view.Known.SourceTags {
			remote = remote || (tag.Key != "cache" && tag.SourceID != nil && *tag.SourceID > 0 && tag.Status == "available")
		}
		return view.Known.Local, remote, view.Known.Cache
	}
	remote = view.Remote.HasRemote || view.Remote.RemotePlayable
	for _, observation := range view.RemoteObservations {
		remote = remote || observation.Status == "available"
	}
	return view.Remote.HasLocal, remote, view.Remote.HasCache
}

// observedMissing reports whether a remote source explicitly reported the work
// absent. A source that was never checked, or failed, does not count.
func (view voiceWorkView) observedMissing() bool {
	var tags []circleSourceStat
	if view.Known != nil {
		tags = view.Known.SourceTags
	} else {
		tags = sourceTagsWithObservations(nil, view.RemoteObservations)
	}
	for _, tag := range tags {
		if tag.Key == "cache" || tag.SourceID == nil || *tag.SourceID <= 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(tag.Status)) {
		case "missing", "not_found", "unavailable":
			return true
		}
	}
	return false
}

// sortVoiceWorkViews orders works like a circle catalog: by the chosen fact in
// either direction, with works that lack it last and the code breaking ties.
func sortVoiceWorkViews(views []voiceWorkView, sortKey, direction string) {
	sortKey, direction = normalizeCircleWorksSort(sortKey, direction)
	sign := -1
	if direction == "asc" {
		sign = 1
	}
	// A missing fact sorts last whichever way the order runs.
	compareFact := func(leftMissing, rightMissing bool, compare func() int) int {
		if leftMissing || rightMissing {
			return boolRank(leftMissing) - boolRank(rightMissing)
		}
		return sign * compare()
	}
	sort.SliceStable(views, func(leftIndex, rightIndex int) bool {
		left, right := views[leftIndex].sortFacts(), views[rightIndex].sortFacts()
		primary := 0
		switch sortKey {
		case "code":
		case "title":
			primary = compareFact(left.title == "", right.title == "", func() int {
				return strings.Compare(left.title, right.title)
			})
		case "rating":
			primary = compareFact(left.rating == nil, right.rating == nil, func() int {
				return cmp.Compare(*left.rating, *right.rating)
			})
		case "sales":
			primary = compareFact(left.sales == nil, right.sales == nil, func() int {
				return cmp.Compare(*left.sales, *right.sales)
			})
		default:
			primary = compareFact(left.releaseDate == "", right.releaseDate == "", func() int {
				return strings.Compare(left.releaseDate, right.releaseDate)
			})
		}
		if primary == 0 {
			primary = sign * strings.Compare(left.code, right.code)
		}
		return primary < 0
	})
}

type voiceWorkSortFacts struct {
	code, title, releaseDate string
	rating                   *float64
	sales                    *int64
}

func (view voiceWorkView) sortFacts() voiceWorkSortFacts {
	if view.Known != nil {
		releaseDate := ""
		if view.Known.ReleaseDate != nil {
			releaseDate = strings.TrimSpace(*view.Known.ReleaseDate)
		}
		return voiceWorkSortFacts{
			code: voiceWorkCodeKey(view.Known.PrimaryCode), title: strings.ToLower(strings.TrimSpace(view.Known.Title)),
			releaseDate: releaseDate, rating: view.Known.Rating, sales: view.Known.Sales,
		}
	}
	return voiceWorkSortFacts{
		code: voiceWorkCodeKey(view.Remote.PrimaryCode), title: strings.ToLower(strings.TrimSpace(view.Remote.Title)),
		releaseDate: strings.TrimSpace(view.Remote.ReleaseDate), rating: view.Remote.Rating, sales: view.Remote.Sales,
	}
}

func voiceWorkCodeKey(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func boolRank(value bool) int {
	if value {
		return 1
	}
	return 0
}
