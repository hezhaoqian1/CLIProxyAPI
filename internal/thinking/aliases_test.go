package thinking

import "testing"

func TestNormalizeThinkingLevelAlias(t *testing.T) {
	tests := map[string]ThinkingLevel{
		"mid":    LevelMedium,
		"MID":    LevelMedium,
		"medium": LevelMedium,
		" high ": LevelHigh,
	}

	for input, want := range tests {
		if got := NormalizeThinkingLevelAlias(input); got != want {
			t.Fatalf("NormalizeThinkingLevelAlias(%q) = %q, want %q", input, got, want)
		}
	}
}
