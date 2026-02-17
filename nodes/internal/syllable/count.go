// Package syllable provides a heuristic English syllable counter.
//
// The algorithm uses vowel-group counting with common English adjustments
// (silent-e, diphthongs, suffixes). It is not perfect for all words but
// works well enough for haiku validation.
package syllable

import (
	"strings"
	"unicode"
)

// Count returns the estimated number of syllables in a single English word.
// It uses a vowel-group heuristic with adjustments for common patterns.
func Count(word string) int {
	word = strings.ToLower(strings.TrimSpace(word))
	if word == "" {
		return 0
	}

	// Remove non-letter characters.
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) {
			return r
		}
		return -1
	}, word)
	if cleaned == "" {
		return 0
	}

	vowels := "aeiouy"
	count := 0
	prevVowel := false

	for _, ch := range cleaned {
		isVowel := strings.ContainsRune(vowels, ch)
		if isVowel && !prevVowel {
			count++
		}
		prevVowel = isVowel
	}

	// Adjustments for common English patterns.

	// Silent -e at end (but not -le which adds a syllable).
	if strings.HasSuffix(cleaned, "e") && !strings.HasSuffix(cleaned, "le") {
		count--
	}

	// -ed at end is usually silent unless preceded by t or d.
	if strings.HasSuffix(cleaned, "ed") && len(cleaned) > 3 {
		beforeEd := cleaned[len(cleaned)-3]
		if beforeEd != 't' && beforeEd != 'd' {
			count--
		}
	}

	// Ensure at least one syllable per word.
	if count < 1 {
		count = 1
	}

	return count
}

// CountLine returns the total syllable count for a line of text.
func CountLine(line string) int {
	words := strings.Fields(line)
	total := 0
	for _, w := range words {
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
