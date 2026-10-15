package translation

import (
	"slices"
	"strconv"
	"strings"
)

// ProtectedText retains names locally. Only Text may be submitted to a model.
// Restore fails closed if a model loses, changes, or duplicates a placeholder.
// Callers converting script variants must apply the same protection boundary.
type ProtectedText struct {
	Text   string
	names  []string
	counts []int
}

const nameMarker = "⟪KIKOTO_NAME_"

func Protect(text string, names []string) (ProtectedText, error) {
	if strings.Contains(text, nameMarker) {
		return ProtectedText{}, failure("translation_protection_failed", "Protected names could not be preserved.")
	}
	ordered := slices.Clone(names)
	slices.SortFunc(ordered, func(a, b string) int { return len(b) - len(a) })
	p := ProtectedText{Text: text}
	pairs := []string{}
	for _, name := range ordered {
		if name == "" || !strings.Contains(text, name) || slices.Contains(p.names, name) {
			continue
		}
		placeholder := nameMarker + strconv.Itoa(len(p.names)) + "⟫"
		p.names = append(p.names, name)
		pairs = append(pairs, name, placeholder)
	}
	p.Text = strings.NewReplacer(pairs...).Replace(text)
	for i := range p.names {
		p.counts = append(p.counts, strings.Count(p.Text, nameMarker+strconv.Itoa(i)+"⟫"))
	}
	return p, nil
}

func (p ProtectedText) Restore(text string) (string, error) {
	for i, name := range p.names {
		placeholder := nameMarker + strconv.Itoa(i) + "⟫"
		if strings.Count(text, placeholder) != p.counts[i] {
			return "", failure("translation_protection_failed", "Protected names could not be preserved.")
		}
		text = strings.ReplaceAll(text, placeholder, name)
	}
	if strings.Contains(text, nameMarker) {
		return "", failure("translation_protection_failed", "Protected names could not be preserved.")
	}
	return text, nil
}
