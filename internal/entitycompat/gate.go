package entitycompat

import (
	"encoding/json"
	"fmt"
	"os"
)

// AllowEntry records a known or intentional drift the gate must not fail on.
// The baseline allowlist captures drift that existed when the gate was
// introduced; each entry is a backlog item to burn down, not a permanent
// exemption. The Reason documents why (intentional extension vs. tracked gap).
type AllowEntry struct {
	Layer  string `json:"layer"`
	Field  string `json:"field"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// LoadGoldenSnapshot reads the committed golden contract snapshot.
func LoadGoldenSnapshot(path string) (GoldenSet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read golden snapshot: %w", err)
	}
	var g GoldenSet
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, fmt.Errorf("decode golden snapshot: %w", err)
	}
	return g, nil
}

// LoadAllowlist reads the baseline/intentional drift allowlist.
func LoadAllowlist(path string) ([]AllowEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read allowlist: %w", err)
	}
	var a []AllowEntry
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("decode allowlist: %w", err)
	}
	return a, nil
}

// GatedFindings returns the HIGH and MED findings across all families. LOW and
// INFO findings are informational and not subject to the gate.
func GatedFindings(golden GoldenSet) []Finding {
	var out []Finding
	for _, fam := range Families() {
		for _, f := range DiffFamily(fam, golden) {
			if f.Sev == SevHigh || f.Sev == SevMed {
				out = append(out, f)
			}
		}
	}
	return out
}

type allowKey struct{ layer, field, kind string }

// RunGate filters gated findings through the allowlist. It returns the
// findings not covered by the allowlist (gate violations) and the allowlist
// entries that matched no current finding (stale entries to remove once the
// corresponding drift is fixed).
func RunGate(findings []Finding, allow []AllowEntry) (violations []Finding, stale []AllowEntry) {
	allowed := make(map[allowKey]bool, len(allow))
	used := make(map[allowKey]bool, len(allow))
	for _, a := range allow {
		allowed[allowKey{a.Layer, a.Field, a.Kind}] = true
	}
	for _, f := range findings {
		k := allowKey{f.Layer, f.Field, f.Kind}
		if allowed[k] {
			used[k] = true
			continue
		}
		violations = append(violations, f)
	}
	for _, a := range allow {
		if !used[allowKey{a.Layer, a.Field, a.Kind}] {
			stale = append(stale, a)
		}
	}
	return violations, stale
}
