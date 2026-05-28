package achievement

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

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

// Misskey ACHIEVEMENT_TYPES の件数 (submodule 不在の CI でも効く軽量 tripwire)。
func TestCount(t *testing.T) {
	if got := Count(); got != 78 {
		t.Errorf("Count() = %d, want 78 (Misskey ACHIEVEMENT_TYPES)", got)
	}
}

// TestTypes_MatchUpstream は submodule の ACHIEVEMENT_TYPES と types map が完全
// 一致することを検証する drift gate。submodule 不在 (CI 等) では skip する。
// submodule bump 時 (= ローカル) に追加 / 削除 / リネームのいずれも検出するので、
// 件数が変わらないリネームも TestCount をすり抜けずに捕まえられる。
func TestTypes_MatchUpstream(t *testing.T) {
	path := filepath.Join(repoRoot(t), "third_party", "misskey", "packages", "backend", "src", "models", "UserProfile.ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("submodule UserProfile.ts not available, skipping drift check: %v", err)
	}
	upstream := extractAchievementTypes(string(data))
	if len(upstream) == 0 {
		t.Fatal("no ACHIEVEMENT_TYPES parsed from submodule (upstream format change?)")
	}
	for name := range upstream {
		if !IsValidType(name) {
			t.Errorf("upstream ACHIEVEMENT_TYPES has %q but mk-go types map is missing it", name)
		}
	}
	for name := range types {
		if _, ok := upstream[name]; !ok {
			t.Errorf("mk-go types map has %q but upstream ACHIEVEMENT_TYPES does not", name)
		}
	}
}

// extractAchievementTypes pulls the quoted names out of the
// `export const ACHIEVEMENT_TYPES = [ ... ] as const;` block.
func extractAchievementTypes(src string) map[string]struct{} {
	block := regexp.MustCompile(`(?s)export const ACHIEVEMENT_TYPES = \[(.*?)\] as const`).FindStringSubmatch(src)
	if len(block) != 2 {
		return nil
	}
	out := map[string]struct{}{}
	for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(block[1], -1) {
		out[m[1]] = struct{}{}
	}
	return out
}

// repoRoot walks up from the test's working directory to the module root
// (the directory containing go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for d := wd; ; {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			t.Fatalf("go.mod not found from %s", wd)
		}
		d = parent
	}
}
