package httpapi

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// All sequence heuristics have explicit numeric limits. A fixed bitset detects
// duplicate/gapped positions without retaining a name or position per file.
type catalogPositionRun struct {
	seen            [157]uint64
	count, min, max int
	invalid         bool
}

func (r *catalogPositionRun) add(position, limit int) {
	if position < 0 || position > limit || r.invalid {
		r.invalid = true
		return
	}
	word, bit := position/64, uint(position%64)
	if r.seen[word]&(uint64(1)<<bit) != 0 {
		r.invalid = true
		return
	}
	r.seen[word] |= uint64(1) << bit
	if r.count == 0 || position < r.min {
		r.min = position
	}
	if position > r.max {
		r.max = position
	}
	r.count++
}
func (r *catalogPositionRun) complete() bool {
	return !r.invalid && r.count >= 2 && r.min <= 1 && r.max-r.min+1 == r.count
}

type catalogNumberedAccumulator struct {
	run         catalogPositionRun
	total, bare int
}

func (a *catalogNumberedAccumulator) add(name string) {
	if a.run.invalid {
		return
	}
	stem := strings.TrimSpace(catalogFileStem(name))
	m := catalogNumberedTrackPrefix.FindStringSubmatch(stem)
	if m == nil {
		a.run.invalid = true
		return
	}
	position, err := strconv.Atoi(m[1])
	if err != nil {
		a.run.invalid = true
		return
	}
	if strings.TrimSpace(stem[len(m[0]):]) == "" {
		a.bare++
	}
	if m[2] != "" {
		total, e := strconv.Atoi(m[2])
		if e != nil || total < position || total > 999 || (a.total >= 0 && a.total != total) {
			a.run.invalid = true
			return
		}
		a.total = total
	} else if a.total >= 0 {
		a.run.invalid = true
		return
	}
	a.run.add(position, 999)
}
func (a *catalogNumberedAccumulator) complete() bool {
	return a.run.complete() && (a.bare == 0 || a.bare == a.run.count)
}

func catalogIndexedSplitMode(ctx context.Context, tx *sql.Tx, id int64) (string, error) {
	var total, episodes int
	if err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(n.episode_series<>'' AND COALESCE(json_extract(b.payload,'$.count'),0)>=2),0) FROM source_catalog_grouping_name n LEFT JOIN source_catalog_grouping_bucket b ON b.grouping_id=n.grouping_id AND b.kind='episode' AND b.key=n.episode_series WHERE n.grouping_id=?`, id).Scan(&total, &episodes); err != nil {
		return "", err
	}
	if total == 0 {
		return catalogWorkSplitSingle, nil
	}
	if total >= 2 && episodes == total {
		return catalogWorkSplitEpisode, nil
	}
	numbered := catalogNumberedAccumulator{total: -1}
	withExtras := catalogNumberedAccumulator{total: -1}
	var structured, trailing catalogPositionRun
	var structuredPrefix, structuredWord, trailingBase, partBase, identity string
	partsValid, distinct := true, false
	extras := 0
	rows, err := tx.QueryContext(ctx, `SELECT name,work_prefix,identity_title FROM source_catalog_grouping_name WHERE grouping_id=? ORDER BY name`, id)
	if err != nil {
		return "", err
	}
	index := 0
	for rows.Next() {
		var name, prefix, currentIdentity string
		if err = rows.Scan(&name, &prefix, &currentIdentity); err != nil {
			break
		}
		if index == 0 {
			identity = currentIdentity
		} else if identity != currentIdentity {
			distinct = true
		}
		numbered.add(name)
		stem := strings.TrimSpace(catalogFileStem(name))
		lower := strings.ToLower(stem)
		if catalogNumberedTrackPrefix.MatchString(lower) {
			withExtras.add(name)
		} else if catalogNarrativePart.MatchString(lower) || catalogNarrativeSuffix.MatchString(lower) || catalogAuxiliaryDirectory(lower) || catalogFormatDirectory(lower) {
			extras++
		} else {
			withExtras.run.invalid = true
		}
		if !structured.invalid {
			rest := stem
			if prefix != "" && len(stem) >= len(prefix) && strings.EqualFold(stem[:len(prefix)], prefix) {
				rest = strings.TrimLeft(stem[len(prefix):], " ._-、")
			}
			m := catalogStructuredTrackPrefix.FindStringSubmatch(rest)
			if m == nil {
				structured.invalid = true
			} else {
				if structuredPrefix == "" {
					structuredPrefix = prefix
				} else if !strings.EqualFold(structuredPrefix, prefix) {
					structured.invalid = true
				}
				word := strings.ToLower(m[1])
				if structuredWord == "" {
					structuredWord = word
				} else if structuredWord != word {
					structured.invalid = true
				}
				position, e := strconv.Atoi(m[2])
				if e != nil {
					structured.invalid = true
				} else {
					structured.add(position, 999)
				}
			}
		}
		if !trailing.invalid {
			loc := catalogLooseTrailingNumber.FindStringIndex(stem)
			if catalogLooseVolumeMarker.MatchString(lower) || loc == nil || loc[1] != len(stem) {
				trailing.invalid = true
			} else {
				number := strings.TrimRight(stem[loc[0]:], " ._-）)】]")
				base := strings.TrimSpace(strings.TrimRight(stem[:loc[0]], " ._-（(【["))
				if (len(number) > 1 && number[0] == '0') || !catalogHasLetter(base) {
					trailing.invalid = true
				} else {
					if trailingBase == "" {
						trailingBase = strings.ToLower(base)
					} else if !strings.EqualFold(trailingBase, base) {
						trailing.invalid = true
					}
					position, e := strconv.Atoi(strings.TrimSpace(number))
					if e != nil {
						trailing.invalid = true
					} else {
						trailing.add(position, 9999)
					}
				}
			}
		}
		if partsValid {
			current := ""
			if prefix != "" {
				partsValid = false
			} else if catalogNarrativePart.MatchString(lower) {
			} else if m := catalogNarrativeSuffix.FindStringSubmatch(lower); m != nil {
				current = strings.TrimSpace(m[1])
				partsValid = catalogHasLetter(current)
			} else {
				partsValid = false
			}
			if index == 0 {
				partBase = current
			} else if partBase != current {
				partsValid = false
			}
		}
		index++
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return "", err
	}
	observeCatalogGrouping(ctx, "sequence", index)
	extraRun := extras > 0 && !withExtras.run.invalid && (withExtras.complete() || (withExtras.run.count == 1 && withExtras.run.min <= 1))
	if numbered.complete() || structured.complete() || trailing.complete() || (partsValid && total >= 2) || extraRun {
		return catalogWorkSplitSingle, nil
	}
	var repeated int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM source_catalog_grouping_bucket WHERE grouping_id=? AND kind='prefix' AND json_extract(payload,'$.count')>=2)`, id).Scan(&repeated); err != nil {
		return "", err
	}
	if repeated != 0 {
		return catalogWorkSplitPrefix, nil
	}
	if distinct {
		return catalogWorkSplitFile, nil
	}
	return catalogWorkSplitSingle, nil
}
