package reactionlegacy

import "testing"

// The legacy table is frozen and must match upstream ReactionService `legacies`
// exactly. This locks the entries (via Convert) so an accidental edit is caught.
func TestConvert_FrozenEntries(t *testing.T) {
	want := map[string]string{
		"like":     "👍",
		"love":     "❤",
		"laugh":    "😆",
		"hmm":      "🤔",
		"surprise": "😮",
		"congrats": "🎉",
		"angry":    "💢",
		"confused": "😥",
		"rip":      "😇",
		"pudding":  "🍮",
		"star":     "⭐",
	}
	for k, v := range want {
		got, ok := Convert(k)
		if !ok {
			t.Errorf("Convert(%q) ok=false, want true", k)
		}
		if got != v {
			t.Errorf("Convert(%q) = %q, want %q", k, got, v)
		}
	}
}

func TestConvert_NonLegacyKeysUnchanged(t *testing.T) {
	for _, k := range []string{"👍", ":smile:", ":smile@.:", ":smile@remote.example:", ""} {
		if got, ok := Convert(k); ok {
			t.Errorf("Convert(%q) ok=true (got %q), want false", k, got)
		}
	}
}
