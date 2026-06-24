package syllable

import "testing"

func TestCount(t *testing.T) {
	tests := []struct {
		word string
		want int
	}{
		{"the", 1},
		{"water", 2},
		{"beautiful", 3},
		{"a", 1},
		{"I", 1},
		{"syllable", 3},
		{"hello", 2},
		{"world", 1},
		{"ancient", 2},
		{"pond", 1},
		{"frog", 1},
		{"leaps", 1},
		{"in", 1},
		{"sound", 1},
		{"of", 1},
		{"silence", 2},
		// Edge cases that broke the old vowel-group heuristic.
		{"leaves", 1},
		{"gives", 1},
		{"lives", 1},
		{"waves", 1},
		{"fire", 1},
		{"poem", 2}, // overridden from 1 (wooorm/syllable bug: oe→diphthong)
		{"rhythm", 2},
		{"crimson", 2},
		{"descend", 2},
		{"descends", 2},
	}
	for _, tt := range tests {
		t.Run(tt.word, func(t *testing.T) {
			got := Count(tt.word)
			if got != tt.want {
				t.Errorf("Count(%q) = %d, want %d", tt.word, got, tt.want)
			}
		})
	}
}

func TestCountLine(t *testing.T) {
	tests := []struct {
		line string
		want int
	}{
		{"An old silent pond", 5},
		{"A frog leaps in to the pond", 7},
		{"Sound of the water", 5},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := CountLine(tt.line)
			if got != tt.want {
				t.Errorf("CountLine(%q) = %d, want %d", tt.line, got, tt.want)
			}
		})
	}
}

func TestValidateHaiku(t *testing.T) {
	valid := "An old silent pond\nA frog leaps in to the pond\nSound of the water"
	counts, ok := ValidateHaiku(valid)
	if !ok {
		t.Errorf("expected valid haiku, got counts %v", counts)
	}
	if counts != [3]int{5, 7, 5} {
		t.Errorf("expected [5, 7, 5], got %v", counts)
	}

	invalid := "Hello world\nThis is not a haiku\nAt all"
	_, ok = ValidateHaiku(invalid)
	if ok {
		t.Error("expected invalid haiku")
	}

	twoLines := "Only two lines\nNot enough"
	_, ok = ValidateHaiku(twoLines)
	if ok {
		t.Error("expected invalid for two lines")
	}

	// Real haiku from the demo that the old counter mishandled (6-7-5 → actually 5-7-5).
	realHaiku := "Crimson leaves descend\nCool whispers drift in twilight\nGolden silence falls"
	counts, ok = ValidateHaiku(realHaiku)
	if !ok {
		t.Errorf("expected valid haiku, got counts %v", counts)
	}
	if counts != [3]int{5, 7, 5} {
		t.Errorf("expected [5, 7, 5], got %v", counts)
	}

	// Invalid haiku (6-7-5).
	invalid2 := "Crimson leaves descending\nCool whispers drift in twilight\nGolden silence falls"
	_, ok = ValidateHaiku(invalid2)
	if ok {
		t.Error("expected invalid haiku")
	}
}
