// Package syllable provides English syllable counting via a Go port of
// github.com/wooorm/syllable (the npm "syllable" package).
//
// The upstream library handles vowel-consonant patterns plus an extensive
// set of corner-case regex rules covering thousands of English words,
// replacing the previous 65-line vowel-group heuristic.
//
// syllables.In() overcounts on multi-word strings (counts word
// boundaries), so we split into individual words and sum.
//
// Known overrides fix words where the upstream library consistently
// miscounts (usually misidentifying vowel pairs as diphthongs).
package syllable

import (
	"strings"

	"github.com/wonglyxng/syllables"
)

// overrides corrects known miscounts in the upstream library.
// Each word is lowercased before lookup.
var overrides = map[string]int{
	"chaos":    2, // ao is not a diphthong in "chaos"
	"poem":     2, // oe is not a diphthong in "poem"
	"poems":    2,
	"farewell": 2, // "fare" + "well", not a triphthong
	"goodbye":  2, // "good" + "bye"
	"goodbyes": 2,
}

// Count returns the estimated number of syllables in a single English word.
func Count(word string) int {
	key := strings.ToLower(word)
	if n, ok := overrides[key]; ok {
		return n
	}
	return syllables.In(word)
}

// CountLine returns the total syllable count for a line of text by
// splitting into words and summing per-word counts.
func CountLine(line string) int {
	total := 0
	for w := range strings.FieldsSeq(line) {
		total += Count(w)
	}
	return total
}

// ValidateHaiku checks whether the given text follows the 5-7-5 syllable
// structure. It expects exactly three lines. Returns the per-line syllable
// counts and whether the structure is valid.
func ValidateHaiku(text string) (counts [3]int, valid bool) {
	lines := splitLines(text)
	if len(lines) != 3 {
		return counts, false
	}

	expected := [3]int{5, 7, 5}
	for i, line := range lines {
		counts[i] = CountLine(line)
	}

	valid = counts[0] == expected[0] && counts[1] == expected[1] && counts[2] == expected[2]
	return counts, valid
}

// splitLines splits text into non-empty trimmed lines.
func splitLines(text string) []string {
	raw := strings.Split(text, "\n")
	var lines []string
	for _, l := range raw {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
