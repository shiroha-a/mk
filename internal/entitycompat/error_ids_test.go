package entitycompat

import (
	_ "embed"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// embeddedErrorIDsJSON is the committed golden per-endpoint error-id snapshot
// (endpoint path -> code -> UUID), extracted from Misskey's meta.errors by
// `go run ./tools/erroriddiff`. It is embedded so the gate runs without the
// third_party submodule present (CI does not check it out).
//
//go:embed testdata/golden_error_ids.json
var embeddedErrorIDsJSON []byte

// errorIDExcludedPrefixes are endpoint families whose error ids are NOT gated
// against vanilla Misskey: mk-go's chat/* is derived from yojo-art/cherrypick
// (federated chat extension) and legitimately carries cherrypick error ids
// rather than vanilla ones.
//
// reversi/* は #1553 で show-game / match / surrender / verify の error id を
// vanilla upstream の endpoint 固有 UUID に揃えたため gate 対象に戻す
// (golden_error_ids.json は既に vanilla UUID を持つ)。NOT_STARTED 等の
// cherrypick 固有 code は golden に無いので gate では skip され影響しない。
var errorIDExcludedPrefixes = []string{"chat/"}

// validUUID matches a well-formed lowercase UUID. Golden ids that fail this are
// skipped: a few upstream meta.errors entries carry malformed ids (e.g. a
// leading space in sw/update-registration, or a non-hex character in
// i/2fa/update-key). Those are upstream typos with no faithful target, so the
// gate cannot meaningfully align to them.
var validUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// TestErrorIDDrift is the static "error-id gate". It scans every mk-go API
// handler's emitted error (code, id) — inline literals, UUID-constant
// references, and apierr helper calls — resolves each handler method to its
// route via router.go, and checks the emitted id against the per-endpoint
// golden extracted from Misskey. A handler that emits a code Misskey also
// defines for that endpoint must use Misskey's exact id, otherwise drop-in
// clients that branch on error.id misclassify the error.
//
// Only confidently-resolved cases are gated: an emission is checked when its
// route resolves to an endpoint, the golden defines that code for the endpoint,
// and the golden id is a well-formed UUID. mk-go-only codes and unresolved
// routes are not flagged (they are not drift against the upstream contract).
func TestErrorIDDrift(t *testing.T) {
	root := repoRoot(t)

	var golden map[string]map[string]string
	if err := json.Unmarshal(embeddedErrorIDsJSON, &golden); err != nil {
		t.Fatalf("parse golden_error_ids.json: %v", err)
	}

	routes := parseRoutes(t, filepath.Join(root, "internal/server/router.go"))
	helpers, consts := parseApierr(t, filepath.Join(root, "internal/api/apierr/errors.go"))
	// echo.go の JSONXxx(c) ラッパーも emission として解決する。これらは
	// handler の最頻送出経路 (例: apierr.JSONNoSuchNote(c)) なので、外すと
	// gate が大半の error を素通しする。helperCallRe は apierr.JSONXxx( にも
	// マッチするため、helper 表に併合すれば scanEmissions がそのまま解決する。
	for name, em := range parseJSONWrappers(t, filepath.Join(root, "internal/api/apierr/echo.go"), helpers) {
		helpers[name] = em
	}

	type drift struct{ endpoint, code, got, want string }
	var drifts []drift
	resolved := 0

	apiDir := filepath.Join(root, "internal/api")
	err := filepath.WalkDir(apiDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		pkg := filepath.Base(filepath.Dir(path))
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, em := range scanEmissions(string(src), helpers, consts) {
			for _, route := range routes[pkg+"\x00"+em.fn] {
				resolved++
				ep := strings.TrimPrefix(route, "/")
				if excludedEndpoint(ep) {
					continue
				}
				want, ok := golden[ep][em.code]
				if !ok || !validUUID.MatchString(want) {
					continue
				}
				if em.uuid != want {
					drifts = append(drifts, drift{ep, em.code, em.uuid, want})
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk api dir: %v", err)
	}

	// silent-zero guard: regex ベースの抽出/解決が upstream フォーマット変更や
	// リファクタで空振りすると、emission が 0 件になり gate が無意味に PASS して
	// しまう。実際の解決数は数百件あるので、大きく下回ったら parser 破損とみなす。
	if resolved < 400 {
		t.Fatalf("error-id gate resolved only %d emissions (expected >=400); the source/router parser likely broke", resolved)
	}
	if len(golden) < 100 {
		t.Fatalf("golden has only %d endpoints (expected >=100); regenerate with `make shapecheck-gen`", len(golden))
	}

	if len(drifts) > 0 {
		sort.Slice(drifts, func(i, j int) bool {
			if drifts[i].endpoint != drifts[j].endpoint {
				return drifts[i].endpoint < drifts[j].endpoint
			}
			return drifts[i].code < drifts[j].code
		})
		var b strings.Builder
		b.WriteString("error-id drift: handler emits an id that differs from Misskey's per-endpoint id.\n")
		b.WriteString("Align the handler to the upstream id (or exclude the endpoint if it is cherrypick-derived).\n")
		for _, d := range drifts {
			b.WriteString("  " + d.endpoint + " " + d.code + ": got " + d.got + " want " + d.want + "\n")
		}
		t.Fatal(b.String())
	}
}

// emission is one error response a handler emits: the enclosing method name,
// the error code, the resolved UUID, the kind discriminator (for the kind
// gate), and (for the status gate) the HTTP status.
type emission struct {
	fn     string
	code   string
	uuid   string
	kind   string
	status int
}

var (
	// funcDeclRe matches a function declaration — both methods `func (recv T) Name(`
	// and package-level free functions `func Name(`. Recognizing free functions is
	// essential: handlers delegate error responses to package-level helpers (e.g.
	// `func notFound(c echo.Context) error`), and without matching their boundary
	// their emissions would be mis-attributed to the preceding method.
	funcDeclRe = regexp.MustCompile(`func (?:\(\w+ \*?\w+\) )?(\w+)\(`)
	// inlineErrRe matches apierr.Error("CODE", <msg>, <id>) and the kind-aware
	// apierr.ErrorWithKind("CODE", <msg>, <id>, apierr.KindXxx) (#1608). msg may
	// be a string literal, a parenless function call (err.Error(),
	// fmt.Sprintf("...")), or an identifier; id may be a literal UUID or a UUID
	// constant. A trailing comma (gofmt adds one to multi-line calls) is
	// tolerated. `(?s)` lets the pattern span the multi-line calls gofmt
	// produces. Capture group 3 holds the Kind constant suffix ("Server" etc.,
	// empty for the 3-arg Error form = default client).
	inlineErrRe = regexp.MustCompile(`(?s)apierr\.Error(?:WithKind)?\(\s*"([A-Z_]+)"\s*,\s*(?:"(?:[^"\\]|\\.)*"|[\w.]+\([^)]*\)|[\w.]+)\s*,\s*("[0-9a-f-]{36}"|(?:apierr\.)?UUID\w+)\s*(?:,\s*apierr\.Kind(\w+))?\s*,?\s*\)`)
	// helperCallRe matches apierr.Helper( — any apierr.X( call; non-helpers
	// (e.g. Error) are filtered against the parsed helper table.
	helperCallRe = regexp.MustCompile(`apierr\.(\w+)\(`)
)

// scanEmissions extracts every error emission from one Go source file, keyed by
// the enclosing method. Splitting on method boundaries keeps multi-line
// apierr.Error(...) calls attached to the right route.
func scanEmissions(src string, helpers map[string]emission, consts map[string]string) []emission {
	locs := funcDeclRe.FindAllStringSubmatchIndex(src, -1)
	var out []emission
	for i, loc := range locs {
		fn := src[loc[2]:loc[3]]
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := src[loc[0]:end]

		for _, m := range inlineErrRe.FindAllStringSubmatch(body, -1) {
			out = append(out, emission{fn: fn, code: m[1], uuid: resolveUUID(m[2], consts), kind: kindFromConstSuffix(m[3])})
		}
		for _, m := range helperCallRe.FindAllStringSubmatch(body, -1) {
			if h, ok := helpers[m[1]]; ok {
				out = append(out, emission{fn: fn, code: h.code, uuid: h.uuid, kind: h.kind})
			}
		}
	}
	return out
}

// resolveUUID turns the 3rd Error() argument into a concrete UUID: a quoted
// literal is unquoted, a (possibly apierr-qualified) UUID constant is looked up.
func resolveUUID(expr string, consts map[string]string) string {
	if strings.HasPrefix(expr, `"`) {
		return strings.Trim(expr, `"`)
	}
	return consts[strings.TrimPrefix(expr, "apierr.")]
}

// kindFromConstSuffix maps a captured Kind constant suffix ("Client" /
// "Server" / "Permission") to the envelope kind value. An empty capture means
// the 3-arg Error form, whose kind is the upstream ApiError default "client".
func kindFromConstSuffix(suffix string) string {
	if suffix == "" {
		return "client"
	}
	return strings.ToLower(suffix)
}

var (
	constRe        = regexp.MustCompile(`\b(UUID\w+)\s*=\s*"([0-9a-f-]{36})"`)
	funcDeclBareRe = regexp.MustCompile(`func (\w+)\(`)
	helperBodyRe   = regexp.MustCompile(`Error(?:WithKind)?\(\s*"([A-Z_]+)"\s*,\s*(?:"(?:[^"\\]|\\.)*"|[\w.]+)\s*,\s*("[0-9a-f-]{36}"|UUID\w+)(?:\s*,\s*Kind(\w+))?`)
)

// parseApierr reads internal/api/apierr/errors.go and returns the UUID-constant
// table and the helper table (helper func name -> {code, uuid}). Helpers are
// the zero/var-arg wrappers such as NoSuchUser() that return Error(code, _, id).
func parseApierr(t *testing.T, path string) (helpers map[string]emission, consts map[string]string) {
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read apierr: %v", err)
	}
	consts = map[string]string{}
	for _, m := range constRe.FindAllStringSubmatch(string(src), -1) {
		consts[m[1]] = m[2]
	}
	helpers = map[string]emission{}
	locs := funcDeclBareRe.FindAllStringSubmatchIndex(string(src), -1)
	for i, loc := range locs {
		name := string(src[loc[2]:loc[3]])
		if name == "Error" || name == "ErrorWithKind" {
			continue
		}
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if m := helperBodyRe.FindStringSubmatch(string(src[loc[0]:end])); m != nil {
			helpers[name] = emission{code: m[1], uuid: resolveUUID(m[2], consts), kind: kindFromConstSuffix(m[3])}
		}
	}
	return helpers, consts
}

var jsonWrapperRe = regexp.MustCompile(`func (JSON\w+)\(c echo\.Context\) error \{`)

// jsonWrapperBodyRe matches the inner helper call of a JSON wrapper, e.g.
// `c.JSON(http.StatusNotFound, NoSuchNote())` -> inner helper "NoSuchNote".
var jsonWrapperBodyRe = regexp.MustCompile(`c\.JSON\([^,]+,\s*(\w+)\(`)

// parseJSONWrappers reads internal/api/apierr/echo.go and returns the echo
// wrapper table (JSONXxx -> {code, uuid}) by resolving each wrapper's inner
// helper call against the helper table. Handlers most often emit errors via
// these wrappers, so without this the gate would miss the majority of routes.
func parseJSONWrappers(t *testing.T, path string, helpers map[string]emission) map[string]emission {
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read echo wrappers: %v", err)
	}
	out := map[string]emission{}
	locs := jsonWrapperRe.FindAllStringSubmatchIndex(string(src), -1)
	for i, loc := range locs {
		name := string(src[loc[2]:loc[3]])
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if m := jsonWrapperBodyRe.FindStringSubmatch(string(src[loc[0]:end])); m != nil {
			if h, ok := helpers[m[1]]; ok {
				out[name] = h
			}
		}
	}
	return out
}

var routeRe = regexp.MustCompile(`\.(?:GET|POST|PUT|DELETE)\("(/[^"]*)",\s*(\w+)\.(\w+)`)

// parseRoutes reads router.go and returns a map keyed by "pkg\x00method" to the
// route paths registered to that handler method. Handler variables are resolved
// to their package directory via the import-alias and constructor tables, so a
// method like notes.FavoritesCreate maps to /notes/favorites/create.
func parseRoutes(t *testing.T, path string) map[string][]string {
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read router: %v", err)
	}
	s := string(src)

	// import alias -> package dir basename (aliased and bare forms).
	alias2dir := map[string]string{}
	for _, m := range regexp.MustCompile(`(\w+)\s+"github\.com/shiroha-a/mk/(internal/api/[\w/]+)"`).FindAllStringSubmatch(s, -1) {
		alias2dir[m[1]] = filepath.Base(m[2])
	}
	for _, m := range regexp.MustCompile(`\n\s+"github\.com/shiroha-a/mk/(internal/api/[\w/]+)"`).FindAllStringSubmatch(s, -1) {
		b := filepath.Base(m[1])
		if _, ok := alias2dir[b]; !ok {
			alias2dir[b] = b
		}
	}
	// handler var -> package dir (via `h := alias.NewHandler(...)`).
	var2dir := map[string]string{}
	for _, m := range regexp.MustCompile(`(\w+)\s*:?=\s*(\w+)\.New\w*\(`).FindAllStringSubmatch(s, -1) {
		if d, ok := alias2dir[m[2]]; ok {
			var2dir[m[1]] = d
		} else {
			var2dir[m[1]] = m[2]
		}
	}

	routes := map[string][]string{}
	for _, m := range routeRe.FindAllStringSubmatch(s, -1) {
		dir, ok := var2dir[m[2]]
		if !ok {
			continue
		}
		key := dir + "\x00" + m[3]
		routes[key] = append(routes[key], m[1])
	}
	return routes
}

func excludedEndpoint(ep string) bool {
	for _, p := range errorIDExcludedPrefixes {
		if strings.HasPrefix(ep, p) {
			return true
		}
	}
	return false
}

// repoRoot finds the module root by walking up from the test's working
// directory (which `go test` sets to the package dir) until it finds go.mod.
// This is robust under -trimpath, where runtime.Caller paths are unusable.
func repoRoot(t *testing.T) string {
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
