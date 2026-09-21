package entitycompat

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 一般利用者へ返す shape と連合へ出す shape に IP が現れないことを固定する (#3136)。
//
// **いまは漏れていない。** `entity.UserLite` / `UserDetailed` に IP のフィールドは
// 無く、`internal/entity` で IP を出すのは `PackSignin` だけ (用途は本人の main
// stream の `signin` イベントと `admin/show-user`。後者は #3114 で
// `canSearchIpHistory` ゲート済み)。`internal/activitypub` は `user_ip` / `signin` を
// 一切参照しない。
//
// **ただしそれを直接見るテストが無かった。** #3066 の完了条件「IP 情報が一般
// ユーザー向け API や連合へ露出しない」の担保が shapecheck の golden 照合しか
// 無く、`PackUserDetailed` にフィールドを足す変更や、AP の actor に属性を増やす
// 変更が緑のまま通る。
//
// **キー名は camelCase の語に分解して見る。** 完全一致の allowlist にすると
// `lastIp` / `ipAddress` / `signinIp` のような新しい名前で素通りするが、素朴な
// 部分一致 (`strings.Contains(k, "ip")`) は `description` / `flipH` / `clipId` を
// 誤検出する (実測)。語として `ip` / `ips` が現れるかを見る。
//
// **`PackSignin` は対象外。** あちらは意図的に IP を出す (モデレーション用) ので、
// ここで禁じると意図と食い違う。そちらの露出範囲は #3114 の
// `TestShowUser_SigninIPsRequirePolicy` が押さえている。
func TestPublicAndFederationShapesHaveNoIPField(t *testing.T) {
	// **代表キーを固定するのが要点。** 実モデルは全て陰性なので、「1 件も拾えて
	// いない」状態と「拾ったが違反が無い」状態を件数だけでは区別できない。走査を
	// 潰す変異も、埋め込みを辿らない変異も、それだけでは緑のまま通る (実測)。
	for _, c := range []struct {
		name    string
		typ     reflect.Type
		mustHav []string
	}{
		{"entity.UserLite", reflect.TypeOf(entity.UserLite{}), []string{"id", "username", "avatarUrl"}},
		// `description` は `UserDetailed` 直下、`username` は `UserLite` 埋め込み由来。
		{"entity.UserDetailed", reflect.TypeOf(entity.UserDetailed{}), []string{"description", "username"}},
		// `inbox` は `Person` 直下、`id` は `Object` 埋め込み由来。
		{"activitypub.Person", reflect.TypeOf(activitypub.Person{}), []string{"inbox", "preferredUsername", "id"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			keys := jsonKeysOfStruct(t, c.typ, nil)
			require.NotEmpty(t, keys, "キーを 1 つも拾えていない (検査が空振りしている)")
			for _, want := range c.mustHav {
				require.Containsf(t, keys, want,
					"%s の走査が %q を拾えていない。検査の範囲が縮んでいる (埋め込みを辿っているかも確認すること)", c.name, want)
			}
			for _, k := range keys {
				assert.Falsef(t, looksLikeIPKey(k),
					"%s の JSON キー %q が IP を指している。一般利用者や連合先へ IP を出さないこと (出す必要があるなら `PackSignin` 側の経路と、その権限ゲートを通すこと)", c.name, k)
			}
		})
	}
}

// jsonKeysOfStruct は struct の json タグ名を再帰的に集める。埋め込みも辿る。
//
// **タグが無い exported フィールドはフィールド名で出る**ので、そちらも集める
// (`encoding/json` の既定)。`json:"-"` は出ないので除く。
func jsonKeysOfStruct(t *testing.T, typ reflect.Type, seen map[reflect.Type]bool) []string {
	t.Helper()
	if seen == nil {
		seen = map[reflect.Type]bool{}
	}
	for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct || seen[typ] {
		return nil
	}
	seen[typ] = true

	var keys []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue // unexported は出ない
		}
		tag := f.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			// 埋め込みはキーを作らずフィールドを持ち上げる。
			keys = append(keys, jsonKeysOfStruct(t, f.Type, seen)...)
			continue
		}
		if name == "" {
			name = f.Name
		}
		keys = append(keys, name)
		keys = append(keys, jsonKeysOfStruct(t, f.Type, seen)...)
	}
	return keys
}

// **実際に Marshal もする。** 静的なタグ走査は `MarshalJSON` を自前で持つ型を
// 見落とす (`json.Marshaler` はタグと無関係にキーを作れる)。
func TestPublicAndFederationShapesMarshalWithoutIP(t *testing.T) {
	for _, c := range []struct {
		name    string
		v       any
		mustHav []string
	}{
		{"entity.UserLite", entity.UserLite{}, []string{"id", "username"}},
		{"entity.UserDetailed", entity.UserDetailed{}, []string{"description", "username"}},
		// `Person` は `omitempty` が多いので、zero value でも出るものだけを見る。
		{"activitypub.Person", activitypub.Person{}, []string{"inbox"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw, err := json.Marshal(c.v)
			require.NoError(t, err)
			var m map[string]any
			require.NoError(t, json.Unmarshal(raw, &m))
			require.NotEmpty(t, m, "キーを 1 つも拾えていない (検査が空振りしている)")
			for _, want := range c.mustHav {
				require.Containsf(t, m, want, "%s の Marshal 結果に %q が無い (検査の範囲が縮んでいる)", c.name, want)
			}
			for k := range m {
				assert.Falsef(t, looksLikeIPKey(k),
					"%s を Marshal した結果のキー %q が IP を指している", c.name, k)
			}
		})
	}
}

// looksLikeIPKey はキー名を camelCase の語に分解して `ip` / `ips` を探す。
//
// 連続する大文字は 1 語として扱う (`IPAddress` → `ip` + `address`)。素朴な
// 部分一致だと `description` / `flipH` / `clipId` を拾うので使えない。
func looksLikeIPKey(key string) bool {
	for _, w := range camelWords(key) {
		if w == "ip" || w == "ips" {
			return true
		}
	}
	return false
}

func camelWords(s string) []string {
	runes := []rune(s)
	var words []string
	var cur []rune
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prevLower := unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1])
			// `IPAddress` の `A` のように、大文字の連なりが終わる位置でも切る。
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if prevLower || (unicode.IsUpper(runes[i-1]) && nextLower) {
				words = append(words, strings.ToLower(string(cur)))
				cur = nil
			}
		}
		if r == '_' || r == '-' {
			if len(cur) > 0 {
				words = append(words, strings.ToLower(string(cur)))
				cur = nil
			}
			continue
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		words = append(words, strings.ToLower(string(cur)))
	}
	return words
}

// 検出ロジック自体を固定する。
//
// **実モデルは全て陰性なので、これが無いと判定の枝が一度も実行されない。**
// 緩めても厳しくしても「違反 0 件」で緑のままになる。
func TestLooksLikeIPKey(t *testing.T) {
	for _, c := range []struct {
		key  string
		want bool
	}{
		{"ip", true},
		{"IP", true},
		{"ips", true},
		{"lastIp", true},
		{"ipAddress", true},
		{"IPAddress", true},
		{"signinIps", true},
		{"last_ip", true},
		// 以下は偽陽性にしてはいけないもの (実モデルに存在する / しうる)。
		{"description", false},
		{"flipH", false},
		{"clipId", false},
		{"participants", false},
		{"recipientId", false},
		{"emojis", false},
	} {
		assert.Equalf(t, c.want, looksLikeIPKey(c.key), "looksLikeIPKey(%q)", c.key)
	}
}
