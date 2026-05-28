package achievement

import "testing"

func TestIsValidType(t *testing.T) {
	valid := []string{"notes1", "login1000", "bubbleGameDoubleExplodingHead", "iLoveMisskey", "markedAsCat"}
	for _, n := range valid {
		if !IsValidType(n) {
			t.Errorf("IsValidType(%q) = false, want true", n)
		}
	}
	invalid := []string{"", "notes2", "bogus", "Notes1", "achievementEarned"}
	for _, n := range invalid {
		if IsValidType(n) {
			t.Errorf("IsValidType(%q) = true, want false", n)
		}
	}
}

// Misskey ACHIEVEMENT_TYPES の件数 (submodule bump 時のずれ検出)。
func TestCount(t *testing.T) {
	if got := Count(); got != 78 {
		t.Errorf("Count() = %d, want 78 (Misskey ACHIEVEMENT_TYPES)", got)
	}
}
