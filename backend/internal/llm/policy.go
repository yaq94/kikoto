package llm

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

type KeywordRule struct {
	Term   string   `json:"term"`
	Fields []string `json:"fields"`
	Action string   `json:"action"`
}

type Policy struct {
	AlwaysScanAllAges bool          `json:"alwaysScanAllAges"`
	TagIDs            []int64       `json:"tagIds"`
	Keywords          []KeywordRule `json:"keywords"`
}

type Match struct {
	Kind  string `json:"kind"`
	Rule  string `json:"rule"`
	Field string `json:"field"`
}

type Assessment struct {
	Class   string  `json:"class"`
	Reason  string  `json:"reason"`
	Matches []Match `json:"matches"`
}

func DefaultPolicy() Policy { return Policy{TagIDs: []int64{}, Keywords: []KeywordRule{}} }

func NormalizeText(value string) string {
	value = norm.NFKC.String(strings.ToLower(strings.TrimSpace(value)))
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, value)
}

func (p Policy) Normalized() Policy {
	result := DefaultPolicy()
	result.AlwaysScanAllAges = p.AlwaysScanAllAges
	seenTags := map[int64]bool{}
	for _, id := range p.TagIDs {
		if id > 0 && !seenTags[id] {
			seenTags[id] = true
			result.TagIDs = append(result.TagIDs, id)
		}
	}
	for _, rule := range p.Keywords {
		term := NormalizeText(rule.Term)
		if term == "" || len(term) > 256 {
			continue
		}
		action := rule.Action
		if action != "blocked" {
			action = "sensitive"
		}
		fields := []string{}
		seen := map[string]bool{}
		for _, field := range rule.Fields {
			field = strings.TrimSpace(field)
			if field != "title" && field != "description" && field != "tags" && field != "all" {
				continue
			}
			if field == "all" {
				fields = []string{"all"}
				break
			}
			if !seen[field] {
				seen[field] = true
				fields = append(fields, field)
			}
		}
		if len(fields) == 0 {
			fields = []string{"all"}
		}
		result.Keywords = append(result.Keywords, KeywordRule{Term: term, Fields: fields, Action: action})
		if len(result.Keywords) >= 512 {
			break
		}
	}
	return result
}

func (p Policy) Assess(age string, fields map[string]string, tagIDs []int64) Assessment {
	p = p.Normalized()
	age = NormalizeText(age)
	general := age == "general" || age == "all" || age == "all age" || age == "all ages" || age == "全年齢" || age == "全年龄" || age == "全年齡"
	knownAge := general || strings.Contains(age, "r15") || strings.Contains(age, "r-15") || strings.Contains(age, "adult") || strings.Contains(age, "r18") || strings.Contains(age, "r-18")
	if !knownAge {
		return Assessment{Class: "unknown", Reason: "age rating is missing or unknown", Matches: []Match{}}
	}
	for _, id := range tagIDs {
		for _, configured := range p.TagIDs {
			if id == configured {
				return Assessment{Class: "sensitive", Reason: "a configured metadata tag matched", Matches: []Match{{Kind: "tag", Rule: "tag:" + stringID(id)}}}
			}
		}
	}
	if general && !p.AlwaysScanAllAges {
		return Assessment{Class: "safe", Reason: "all-ages rating", Matches: []Match{}}
	}
	matches := []Match{}
	for _, rule := range p.Keywords {
		for field, value := range fields {
			if !ruleApplies(rule.Fields, field) {
				continue
			}
			if strings.Contains(NormalizeText(value), rule.Term) {
				matches = append(matches, Match{Kind: "keyword", Rule: rule.Term, Field: field})
				break
			}
		}
	}
	if len(matches) == 0 {
		return Assessment{Class: "safe", Reason: "no configured rule matched", Matches: matches}
	}
	for _, rule := range p.Keywords {
		for _, match := range matches {
			if rule.Term == match.Rule && rule.Action == "blocked" {
				return Assessment{Class: "blocked", Reason: "a configured blocking rule matched", Matches: matches}
			}
		}
	}
	return Assessment{Class: "sensitive", Reason: "a configured sensitive rule matched", Matches: matches}
}

func ruleApplies(fields []string, field string) bool {
	for _, value := range fields {
		if value == "all" || value == field {
			return true
		}
	}
	return false
}
func stringID(value int64) string {
	if value == 0 {
		return "0"
	}
	result := ""
	for value > 0 {
		result = string(rune('0'+value%10)) + result
		value /= 10
	}
	return result
}
