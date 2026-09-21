package entitycompat

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// レスポンス DTO と連合の shape に IP が現れないことを固定する (#3136)。
//
// **いまは漏れていない。** `internal/entity` の struct に IP のフィールドは無く、
// `internal/activitypub` は `user_ip` / `signin` を一切参照しない。#3066 の完了条件
// 「IP 情報が一般ユーザー向け API や連合へ露出しない」の担保が shapecheck の golden
// 照合しか無かったので、直接見る。
//
// **reflect で型を手で並べない。** 最初そう書いたところ、敵対的レビューで
// `entity.MeDetailed` (= `/api/i`) が対象から漏れていること、入れ子 struct・map の
// 値型・非公開型の匿名埋め込み (`encoding/json` は昇格させる) を辿れていないことが
// 実測された。**AST で全 struct のタグを読む**ほうが、型を列挙せずに済むぶん射程が
// 広く、入れ子も別 struct として自然に拾える。
//
// **射程の外**: handler が `map[string]any` を手で組んで返す経路は届かない。
// IP を意図的に出す `entity.PackSignin` がまさにそれで、あちらの露出範囲は #3114 の
// `TestShowUser_SigninIPsRequirePolicy` (モデレーション権限でゲート) と、本人だけが
// 自分の履歴を引く `i/signin-history` が担う。**「一般利用者に IP が出ない」ではなく
// 「他人の IP は出ない」が正確な主張。**
func TestResponseAndFederationShapesHaveNoIPField(t *testing.T) {
	root := repoRoot(t)
	dirs := []string{"internal/entity", "internal/activitypub"}

	var all []taggedField
	for _, d := range dirs {
		all = append(all, scanJSONTags(t, filepath.Join(root, d), root)...)
	}

	// **下限を持たせる。** 違反 0 件が正常な状態なので、抽出が壊れても「検出 0 件」と
	// 区別が付かない。実測 274 (2026-09-21)。
	require.GreaterOrEqualf(t, len(all), 200,
		"json タグを %d 件しか拾えていない。抽出が壊れている", len(all))

	// 代表キーを要求して、走査が縮んでいないことも見る。
	byKey := map[string]bool{}
	for _, f := range all {
		byKey[f.Key] = true
	}
	for _, want := range []string{"username", "avatarUrl", "description", "inbox", "preferredUsername"} {
		require.Containsf(t, byKey, want, "走査が %q を拾えていない。範囲が縮んでいる", want)
	}

	for _, f := range all {
		if !looksLikeIPKey(f.Key) {
			continue
		}
		reason, ok := ipShapeAllowlist[f.ref()]
		assert.Truef(t, ok,
			"%s の %s.%s (json:%q) が IP を指している。**他人の IP をレスポンスや連合へ"+
				"出さないこと** (#3136)。出す必要があるなら、モデレーション権限でゲートした"+
				"経路 (`admin/show-user`) か、本人だけが引ける経路 (`i/signin-history`) を"+
				"通し、ここに理由付きで足すこと。",
			f.File, f.Struct, f.Field, f.Key)
		if ok {
			assert.NotEmptyf(t, reason, "%s の allowlist に理由が無い", f.ref())
		}
	}

	for ref := range ipShapeAllowlist {
		found := false
		for _, f := range all {
			if f.ref() == ref {
				found = true
				break
			}
		}
		assert.Truef(t, found, "allowlist の %q が実在しない。移動したか消えた", ref)
	}
}

// ipShapeAllowlist は IP を出してよい shape と、その理由。**いまは空** —
// `internal/entity` / `internal/activitypub` の struct はどれも IP を持たない。
//
// **空であることも固定する** (`TestIPShapeAllowlistIsEmpty`)。entry を足すのは
// 「他人の IP をレスポンスに出す」と決めたときだけなので、黙って増やせないようにする。
// 上の「死んだ entry を落とす」検査は、空のうちは何も守らない。
var ipShapeAllowlist = map[string]string{}

func TestIPShapeAllowlistIsEmpty(t *testing.T) {
	assert.Empty(t, ipShapeAllowlist,
		"IP を出す shape が増えている。**他人の IP をレスポンスや連合へ出す判断**なので、"+
			"経路と権限ゲートを確かめてからこのアサーションを更新すること")
}

// 収集ロジックそのものを人工のソースで固定する (`testdata/ipscan`)。
//
// **実データには該当が無い形がある。** `internal/entity` / `internal/activitypub` には
// タグ無しの exported フィールドが 1 つも無いので、そこを拾わない変異は実データ相手には
// 何も起こさない (実測で素通りした)。`encoding/json` はタグが無ければフィールド名で
// 出すので、拾わないと**新しく足されたフィールドが検査から静かに外れる**。
func TestScanJSONTagsCollectsWhatEncodingJSONEmits(t *testing.T) {
	dir := filepath.Join("testdata", "ipscan")
	var keys []string
	for _, f := range scanJSONTags(t, dir, dir) {
		keys = append(keys, f.Key)
	}
	sort.Strings(keys)
	// `Untagged` はフィールド名で出る / `Skipped` と `hidden` は出ない /
	// 匿名埋め込みはキーを作らないが、埋め込み先の struct を走査して `deep` が出る。
	assert.Equal(t, []string{"Untagged", "deep", "tagged"}, keys)
}

// 自前 `MarshalJSON` を持つ型は AST のタグ走査から外れる。一覧を固定して、増えたら
// 気付けるようにする (増えた型が IP を吐くかは人が見る)。
func TestCustomJSONMarshalersAreKnown(t *testing.T) {
	root := repoRoot(t)
	var found []string
	for _, d := range []string{"internal/entity", "internal/activitypub"} {
		found = append(found, scanCustomMarshalers(t, filepath.Join(root, d), root)...)
	}
	sort.Strings(found)
	assert.Equal(t, []string{
		// `[]Multikey` をそのまま出すだけ。IP は持たない。
		"internal/activitypub/types.go#MultikeyList",
	}, found,
		"自前 `MarshalJSON` を持つ型が増減した。**タグ走査では中身が見えない**ので、"+
			"新しい型が IP を吐かないことを人が確かめてから一覧を更新すること")
}

// 判定そのものを固定する。
//
// **実データは全て陰性なので、これが無いと判定の枝が一度も実行されない。** 緩めても
// 厳しくしても「違反 0 件」で緑のままになる。陰性側は実在するキーから採ってある。
func TestLooksLikeIPKey(t *testing.T) {
	for _, c := range []struct {
		key  string
		want bool
	}{
		{"ip", true},
		{"IP", true},
		{"ips", true},
		{"IPs", true}, // Go の複数形イニシャリズム。最初の実装はここを落とした
		{"lastIp", true},
		{"lastIPs", true},
		{"ipAddress", true},
		{"IPAddress", true},
		{"signinIps", true},
		{"signinIPs", true},
		{"last_ip", true},
		{"ipv4", true},
		{"IPv4", true},
		{"lastIpv4", true},
		{"ipv6Address", true},
		// 偽陽性にしてはいけないもの (実在する / しうるキー)。
		{"description", false},
		{"flipH", false},
		{"clipId", false},
		{"participants", false},
		{"recipientId", false},
		{"membership", false},
		{"relationship", false},
		{"zip", false},
		{"emojis", false},
		{"IPFS", false},
		{"IPsec", false},
	} {
		assert.Equalf(t, c.want, looksLikeIPKey(c.key), "looksLikeIPKey(%q) / 正規化=%q", c.key, spaceCamelKey(c.key))
	}
}

type taggedField struct {
	File   string // repo 相対
	Struct string
	Field  string
	Key    string // JSON に出る名前
}

func (f taggedField) ref() string { return f.File + "#" + f.Struct + "." + f.Field }

// looksLikeIPKey はキーを語に割ってから `ip` 系を探す。
//
// **素朴な部分一致は使えない** (`description` / `flipH` / `clipId` を誤検出する)。
// **語の切り方も素朴ではいけない** — `lastIPs` を `last` + `i` + `ps` に割る実装だと、
// Go の命名規約に素直に従った名前だけが素通りする (実測)。
func looksLikeIPKey(key string) bool { return ipTokenRe.MatchString(spaceCamelKey(key)) }

var ipTokenRe = regexp.MustCompile(`(?:^| )ip(?:s|v4|v6|v4s|v6s|address|addresses)?(?:$| )`)

// spaceCamelKey は camelCase / snake_case を空白区切りの小文字へ均す。
//
// 境界は「小文字または数字の直後の大文字」だけに置く。大文字の連なりは割らないので
// `IPs` / `IPv4` / `IPAddress` が 1 語のまま残り、`ip` で始まるかを見れば済む。
func spaceCamelKey(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1])) {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(b.String()))
}

// scanJSONTags は dir 以下の非テスト Go から struct フィールドの JSON キーを集める。
//
// **タグの無い exported フィールドも拾う** (`encoding/json` はフィールド名で出す)。
// `json:"-"` は出ないので除く。匿名埋め込みはキーを作らないので飛ばす — 埋め込み先の
// struct 自体が別途走査されるので、そちらで見える。
func scanJSONTags(t *testing.T, dir, repo string) []taggedField {
	t.Helper()
	var out []taggedField
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		rel, rerr := filepath.Rel(repo, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)

		var typeName string
		ast.Inspect(f, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok {
				typeName = ts.Name.Name
				return true
			}
			st, ok := n.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fld := range st.Fields.List {
				key := ""
				if fld.Tag != nil {
					tv := reflect.StructTag(strings.Trim(fld.Tag.Value, "`")).Get("json")
					key, _, _ = strings.Cut(tv, ",")
				}
				if key == "-" {
					continue
				}
				if len(fld.Names) == 0 {
					continue // 匿名埋め込み: キーを作らない
				}
				for _, id := range fld.Names {
					if !id.IsExported() {
						continue
					}
					k := key
					if k == "" {
						k = id.Name
					}
					out = append(out, taggedField{File: rel, Struct: typeName, Field: id.Name, Key: k})
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err, "walk %s", dir)
	return out
}

func scanCustomMarshalers(t *testing.T, dir, repo string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		rel, rerr := filepath.Rel(repo, path)
		if rerr != nil {
			return rerr
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "MarshalJSON" || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			typ := fn.Recv.List[0].Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			if id, ok := typ.(*ast.Ident); ok {
				out = append(out, filepath.ToSlash(rel)+"#"+id.Name)
			}
		}
		return nil
	})
	require.NoError(t, err, "walk %s", dir)
	return out
}
