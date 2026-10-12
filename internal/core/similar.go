package core

import (
	"strings"

	"impamind/internal/store"
)

// Tokens returns the set of normalized words of length >= 3.
func Tokens(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, w := range strings.Fields(store.NormalizeText(s)) {
		if len([]rune(w)) >= 3 {
			out[stem(w)] = struct{}{}
		}
	}
	return out
}

// stem drops a few common plural and verb endings so "testes"/"teste" and
// "uses"/"use" compare equal. It is deliberately conservative.
func stem(w string) string {
	for _, suf := range []string{"ções", "coes", "ing", "es", "s"} {
		if len(w) > len(suf)+3 && strings.HasSuffix(w, suf) {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

// Jaccard similarity of two token sets.
func Jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	return float64(inter) / float64(union)
}

// Similarity compares two memories by title and content.
func Similarity(titleA, contentA, titleB, contentB string) float64 {
	return Jaccard(Tokens(titleA+" "+contentA), Tokens(titleB+" "+contentB))
}

// NearDuplicate is the threshold above which an observation is treated as a
// candidate duplicate and not stored without distinct_from.
const NearDuplicate = 0.72

// ConflictCandidate is the lower threshold used to flag possibly conflicting
// decisions on the same subject.
const ConflictCandidate = 0.30

// Trivial reports content that is too short or generic to be worth storing.
func Trivial(title, content string) bool {
	t := Tokens(title + " " + content)
	return len(strings.TrimSpace(content)) < 12 || len(t) < 3
}
