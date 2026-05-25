// Command shapediff regenerates the golden API-contract snapshot used by the
// entitycompat drift gate, and (with -report) prints a full drift report.
//
// The golden snapshot is extracted from the misskey-js autogen `types.ts`
// (which mirrors the OpenAPI `components.schemas`) for exactly the schemas the
// entitycompat family table references, and written to
// internal/entitycompat/testdata/golden_schemas.json. Regenerate it whenever
// the third_party/misskey submodule is bumped to a new upstream version.
//
// Usage:
//
//	go run ./tools/shapediff            # regenerate golden snapshot
//	go run ./tools/shapediff -report    # also print a full drift report
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/shiroha-a/mk/internal/entitycompat"
)

func main() {
	typesPath := flag.String("types", "third_party/misskey/packages/misskey-js/src/autogen/types.ts", "path to misskey-js autogen types.ts")
	outPath := flag.String("out", "internal/entitycompat/testdata/golden_schemas.json", "golden snapshot output path")
	unionOut := flag.String("union-out", "internal/entitycompat/testdata/golden_unions.json", "golden union snapshot output path")
	report := flag.Bool("report", false, "print a full drift report after regenerating")
	flag.Parse()

	data, err := os.ReadFile(*typesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shapediff: read types.ts: %v\n", err)
		os.Exit(1)
	}
	lines := strings.Split(string(data), "\n")

	names := entitycompat.GoldenSchemaNames()
	golden := entitycompat.ParseTypesFile(lines, names)

	// すべての referenced schema が見つかったか確認 (typo / upstream rename の検出)。
	for _, n := range names {
		if _, ok := golden[n]; !ok {
			fmt.Fprintf(os.Stderr, "shapediff: WARNING golden schema %q not found in types.ts\n", n)
		}
	}

	buf, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "shapediff: marshal: %v\n", err)
		os.Exit(1)
	}
	buf = append(buf, '\n')
	if err := os.WriteFile(*outPath, buf, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "shapediff: write %s: %v\n", *outPath, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d golden schemas)\n", *outPath, len(golden))

	// L2: discriminated-union snapshots, keyed by variant `type` literal.
	unions := map[string]map[string]entitycompat.Schema{}
	for _, n := range entitycompat.UnionSchemaNames() {
		if v := entitycompat.ParseUnion(lines, n); len(v) > 0 {
			unions[n] = v
		} else {
			fmt.Fprintf(os.Stderr, "shapediff: WARNING union schema %q not found in types.ts\n", n)
		}
	}
	ubuf, err := json.MarshalIndent(unions, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "shapediff: marshal unions: %v\n", err)
		os.Exit(1)
	}
	ubuf = append(ubuf, '\n')
	if err := os.WriteFile(*unionOut, ubuf, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "shapediff: write %s: %v\n", *unionOut, err)
		os.Exit(1)
	}
	nVariants := 0
	for _, v := range unions {
		nVariants += len(v)
	}
	fmt.Printf("wrote %s (%d unions, %d variants)\n", *unionOut, len(unions), nVariants)

	if *report {
		printReport(golden)
	}
}

func printReport(golden entitycompat.GoldenSet) {
	total := 0
	for _, fam := range entitycompat.Families() {
		findings := entitycompat.DiffFamily(fam, golden)
		fmt.Printf("\n### family %s\n", fam.Name)
		if len(findings) == 0 {
			fmt.Println("  (no drift)")
			continue
		}
		// stable layer ordering for readability
		sort.SliceStable(findings, func(i, j int) bool { return findings[i].Layer < findings[j].Layer })
		for _, f := range findings {
			if f.Sev == entitycompat.SevHigh || f.Sev == entitycompat.SevMed {
				total++
			}
			fmt.Printf("  %-5s [%-8s] %-28s (%s <- %s) %s\n",
				f.Sev, f.Kind, f.Field, f.Layer, f.Golden, f.Detail)
		}
	}
	fmt.Printf("\n==> %d gated (HIGH/MED) finding(s)\n", total)
}
