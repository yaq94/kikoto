package httpapi

import (
	"context"
	"path"
	"strings"
)

// The remote directory budget is 32, so immediate-child evidence is bounded
// independently of how many tracks each child contains.
func (s *Server) finiteEpisodeChildren(ctx context.Context, listingID int64, root string) (map[string]catalogSplitTarget, error) {
	var direct int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_listing_item WHERE listing_id=? AND kind='audio' AND instr(name,'/')=0)`, listingID).Scan(&direct); err != nil || direct != 0 {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT substr(name,1,instr(name,'/')-1) FROM source_catalog_listing_item WHERE listing_id=? AND kind='audio' ORDER BY 1`, listingID)
	if err != nil {
		return nil, err
	}
	type child struct {
		name, canonical string
		copy            bool
		evidence        catalogSeriesEvidence
	}
	children := []child{}
	counts := map[string]int{}
	positions := map[int]bool{}
	copies := map[string]bool{}
	valid := true
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		canonical, copy := catalogEpisodeFormatCopy(name)
		if !copy {
			canonical = name
		}
		evidence, ok := catalogEpisodeEvidence(path.Join(root, canonical))
		if !ok {
			valid = false
			break
		}
		children = append(children, child{name, canonical, copy, evidence})
		counts[canonical]++
		positions[evidence.Position] = true
		copies[canonical] = copies[canonical] || copy
		if len(children) > catalogFiniteDirectories {
			valid = false
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	if !valid || len(children) < 2 {
		return nil, nil
	}
	targets := map[string]catalogSplitTarget{}
	for _, c := range children {
		sourceRoot := ""
		if counts[c.canonical] > 1 || (len(positions) >= 2 && copies[c.canonical]) {
			sourceRoot = root
		}
		evidence := c.evidence
		evidence.Explicit = len(positions) >= 2
		targets[c.name] = catalogSplitTarget{Root: path.Join(root, c.canonical), SourceRoot: sourceRoot, Title: c.canonical, Episode: evidence, HasEpisode: true}
	}
	return targets, nil
}

func finiteChildFileTarget(children map[string]catalogSplitTarget, relative string) (catalogSplitTarget, string, bool) {
	child, tail, ok := strings.Cut(relative, "/")
	if !ok {
		return catalogSplitTarget{}, relative, false
	}
	target, ok := children[child]
	if !ok {
		return target, relative, false
	}
	if target.SourceRoot == "" {
		relative = tail
	}
	return target, relative, true
}
