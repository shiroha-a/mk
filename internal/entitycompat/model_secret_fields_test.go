package entitycompat

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/pgarray"
)

// **モデルを JSON 化したときに秘密が出ないことを固定する。**
//
// mk-go にはモデルをそのまま JSON 化する経路があり、`json:"-"` が唯一の
// 防波堤になっているフィールドがある。実測で `model.User.Token` のタグを
// `json:"token"` に変えると **native token が JSON に出る**が、`make gates`
// も既存のテストも全て緑のままだった。native token を取れればそのユーザー
// として API を叩けるので、あらゆる権限ゲートを迂回できる。
//
// upstream Misskey TS は TypeORM の `select: false` で同じことをしており、
// **そこが抜けていたために生の user 行から native token が読める**という
// 実例がある。あちらは列の定義とレスポンスの形が離れているが、Go では
// 構造体タグ 1 つなので、**タグを守れば同じ間違いが起きない**。
//
// **名前だけでは判定できない。** `Meta` の captcha secret は `admin/meta` が
// 管理画面へ返す必要があるし、drive の `accessKey` は URL の構成要素で秘密
// ではない。したがって #2792 と同じ allowlist 方式にし、**出してよいものには
// 理由を書かせる**。
//
// **allowlist は該当集合と突き合わせる。** 実在しないキーを書いても黙って
// 無視される形だと、検査していないのに緑になる。初版は
// `Meta.SensitiveMediaDetectionAPIKey` を登録していたが、当時の正規表現は
// `ApiKey` しか見ておらず **`APIKey` には一致しないので何も検査していなかった**
// (この突き合わせを足して初めて発覚した)。副作用として、**正規表現を狭める
// 変異でも落ちる** — 検出対象が減ると allowlist のキーが該当集合から消える。
//
// **`internal/model` には置けない。** あそこは `_test.go` を 1 つも持たない
// ので、テストを足すと CI のカバレッジ閾値 (90%) の対象に**初めて**入り、
// wire 層と同じ理由で 0% に張り付いて落ちる (#462 と同型)。gate の置き場は
// ここで、対象パッケージのソースはファイルとして読む。
//
// **既知の取りこぼし**: 名前に該当の語を含まない秘密は拾えない。埋め込み
// 構造体の昇格フィールドも見ない。`Key` 全体には広げていない —
// `PublicKey` / `*SiteKey` (captcha のサイトキーはフロントへ配る公開値) /
// `SortKeys` / `ExcludeKeywords` が入って allowlist が誤検知で埋まり、
// 本物が紛れる。
var secretFieldNameRe = regexp.MustCompile(`Password|Passwd|Token|Secret|PrivateKey|PrivKey|Credential|ApiKey|APIKey|AuthKey|AccessKey`)

// serializableSecretLike lists fields that match the name pattern but are
// intentionally serialized. **理由を書くこと。**
var serializableSecretLike = map[string]string{
	// admin/meta が管理画面へ返す。運営者が設定する値で moderator 以上にしか
	// 届かない (upstream も同じものを返す)。
	"Meta.HcaptchaSecretKey":             "admin/meta が管理画面へ返す (upstream も同じ)",
	"Meta.RecaptchaSecretKey":            "同上",
	"Meta.TurnstileSecretKey":            "同上",
	"Meta.McaptchaSecretKey":             "同上",
	"Meta.SwPrivateKey":                  "同上",
	"Meta.ObjectStorageAccessKey":        "同上",
	"Meta.ObjectStorageSecretKey":        "同上",
	"Meta.SensitiveMediaDetectionAPIKey": "同上",
	"Meta.DeeplAuthKey":                  "同上",
	"Meta.TruemailAuthKey":               "同上",
	"Meta.VerifymailAuthKey":             "同上",
	// URL の構成要素であって秘密ではない (/files/:accessKey は公開 GET)。
	"DriveFile.AccessKey":            "URL の構成要素で秘密ではない",
	"DriveFile.ThumbnailAccessKey":   "同上",
	"DriveFile.WebpublicAccessKey":   "同上",
	"ChunkedUploadSession.AccessKey": "同上",
	// 本人 / 作成者にだけ返る。
	"AccessToken.Token":          "本人の app token 一覧に返る (upstream も同じ)",
	"App.Secret":                 "作成者にだけ返る (upstream も同じ)",
	"AuthSession.Token":          "認証フローで本人に返る (upstream も同じ)",
	"Webhook.Secret":             "作成者にだけ返る (upstream も同じ)",
	"SystemWebhook.Secret":       "管理者にだけ返る (upstream も同じ)",
	"PasswordResetRequest.Token": "メールのリンクに載せる (本人にだけ届く)",
	// 名前が引っかかるだけで秘密ではない。
	"UserProfile.UsePasswordLessLogin":     "真偽値であって秘密ではない",
	"UserSecurityKey.CredentialDeviceType": "WebAuthn の種別。秘密ではない",
	"UserSecurityKey.CredentialBackedUp":   "真偽値であって秘密ではない",
}

// scanSecretLikeFields returns every exported struct field in src whose name
// looks like a secret, keyed by "<Type>.<Field>" and valued by its json tag.
func scanSecretLikeFields(src []byte, filename string) (map[string]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}

	out := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, fld := range st.Fields.List {
			if len(fld.Names) == 0 || fld.Tag == nil {
				continue
			}
			for _, name := range fld.Names {
				if !name.IsExported() || !secretFieldNameRe.MatchString(name.Name) {
					continue
				}
				tag := reflect.StructTag(strings.Trim(fld.Tag.Value, "`"))
				out[ts.Name.Name+"."+name.Name] = tag.Get("json")
			}
		}
		return true
	})
	return out, nil
}

// leakingSecretFields returns the keys of found whose json tag serializes the
// value and that allow does not justify. **判定はここ 1 箇所にまとめる** —
// 人工ソースのテストと実モデルのテストが同じ枝を通るようにするため。
func leakingSecretFields(found, allow map[string]string) []string {
	var leaks []string
	for key, jsonTag := range found {
		if jsonTag == "-" {
			continue
		}
		if _, ok := allow[key]; ok {
			continue
		}
		leaks = append(leaks, key)
	}
	sort.Strings(leaks)
	return leaks
}

// modelSecretLikeFields scans internal/model for secret-looking fields.
func modelSecretLikeFields(t *testing.T) map[string]string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(repoRoot(t), "internal", "model", "*.go"))
	require.NoError(t, err)

	out := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		got, err := scanSecretLikeFields(src, f)
		require.NoErrorf(t, err, "parse %s", f)
		for k, v := range got {
			out[k] = v
		}
	}
	return out
}

// **検出そのものが動くことを、実モデルとは独立に固定する。**
// 実モデルは allowlist で全て許可済みなので leak が 0 件であり、実モデルだけを
// 見ていると**検出の枝が一度も実行されない**。ここで人工のソースを食わせる。
func TestScanSecretLikeFields(t *testing.T) {
	src := []byte(`package p

type Sample struct {
	ID                 string  ` + "`json:\"id\"`" + `
	Token              *string ` + "`json:\"token\"`" + `
	HiddenToken        *string ` + "`json:\"-\"`" + `
	TwoFactorSecret    *string ` + "`json:\"twoFactorSecret,omitempty\"`" + `
	ObjectStorageAccessKey *string ` + "`json:\"objectStorageAccessKey\"`" + `
	unexportedSecret   string  ` + "`json:\"unexportedSecret\"`" + `
	Nickname           string  ` + "`json:\"nickname\"`" + `
	PublicKey          string  ` + "`json:\"publicKey\"`" + `
	NoTag              string
}

type Other struct {
	Password string ` + "`json:\"password\"`" + `
}
`)

	got, err := scanSecretLikeFields(src, "sample.go")
	require.NoError(t, err)

	require.Equal(t, map[string]string{
		"Sample.Token":                  "token",
		"Sample.HiddenToken":            "-",
		"Sample.TwoFactorSecret":        "twoFactorSecret,omitempty",
		"Sample.ObjectStorageAccessKey": "objectStorageAccessKey",
		"Other.Password":                "password",
	}, got, "秘密らしい名前のフィールドだけを、json タグ付きで拾うこと")

	// leak 判定そのもの。**allowlist は人工のものを渡す** — 実の
	// serializableSecretLike に依存させると、そちらを編集しただけでこの
	// テストが落ちて、検出ロジックの検査という役目を果たさなくなる。
	allow := map[string]string{"Sample.ObjectStorageAccessKey": "テスト用"}
	require.Equal(t,
		[]string{"Other.Password", "Sample.Token", "Sample.TwoFactorSecret"},
		leakingSecretFields(got, allow),
		"json:\"-\" でなく allowlist にも無いものが leak として出ること "+
			"(HiddenToken は json:\"-\"、ObjectStorageAccessKey は allowlist なので出ない)")
}

func TestModelSecretFieldsAreNotSerialized(t *testing.T) {
	found := modelSecretLikeFields(t)

	// 1 つも拾えなかったら落とす。書式が変わって正規表現が空振りすると、
	// 検査していないのに緑になる (#2874 / #2828 と同じ判断)。
	require.NotEmpty(t, found, "秘密らしい名前のフィールドを 1 つも拾えない (書式が変わった?)")

	leaks := leakingSecretFields(found, serializableSecretLike)
	for i, key := range leaks {
		leaks[i] = key + " (json:\"" + found[key] + "\")"
	}
	require.Emptyf(t, leaks,
		"秘密のフィールドが JSON に出る: %v\n"+
			"モデルをそのまま JSON 化する経路があるので、`json:\"-\"` を付けるか、\n"+
			"出してよい理由を serializableSecretLike に書くこと", leaks)
}

// **allowlist に死んだ項目を残さない。** 実在しないキーが黙って無視される形
// だと、そのフィールドを守っているつもりで何も検査していない状態になる。
func TestSerializableSecretLikeHasNoDeadEntries(t *testing.T) {
	found := modelSecretLikeFields(t)

	var dead []string
	for key, reason := range serializableSecretLike {
		require.NotEmptyf(t, reason, "%s: 出してよい理由を書くこと", key)
		if _, ok := found[key]; !ok {
			dead = append(dead, key)
		}
	}
	sort.Strings(dead)
	require.Emptyf(t, dead,
		"serializableSecretLike に、検出対象に無いキーが残っている: %v\n"+
			"rename / 削除で消えたか、secretFieldNameRe が一致しなくなっている", dead)
}

// **実際に JSON 化して漏れないことも見る。** タグの静的検査だけだと、
// `MarshalJSON` を自前で実装した型で抜ける。
func TestModelJSONDoesNotContainSecrets(t *testing.T) {
	tok := "must-not-appear-token"
	b, err := json.Marshal(model.User{ID: "u1", Username: "alice", Token: &tok})
	require.NoError(t, err)
	require.NotContainsf(t, string(b), tok,
		"User を JSON 化すると native token が出る。これを取れると全ての権限ゲートを迂回できる")

	pw, sec, bak, tmp := "pw-hash", "totp-secret", "backup-secret", "temp-secret"
	pb, err := json.Marshal(model.UserProfile{
		UserID:                "u1",
		Password:              &pw,
		TwoFactorSecret:       &sec,
		TwoFactorBackupSecret: pgarray.StringArray{bak},
		TwoFactorTempSecret:   &tmp,
	})
	require.NoError(t, err)
	for _, s := range []string{pw, sec, bak, tmp} {
		require.NotContainsf(t, string(pb), s, "UserProfile を JSON 化すると %q が出る", s)
	}

	kb, err := json.Marshal(model.UserKeypair{UserID: "u1", PrivateKey: "PRIVATE-PEM"})
	require.NoError(t, err)
	require.NotContainsf(t, string(kb), "PRIVATE-PEM", "UserKeypair を JSON 化すると署名鍵が出る")

	eb, err := json.Marshal(model.UserKeypairExtra{UserID: "u1", Ed25519PrivateKey: "ED25519-PEM"})
	require.NoError(t, err)
	require.NotContainsf(t, string(eb), "ED25519-PEM", "UserKeypairExtra を JSON 化すると Ed25519 の署名鍵が出る")

	ub, err := json.Marshal(model.UserPending{ID: "p1", Username: "bob", Password: "pending-pw-hash"})
	require.NoError(t, err)
	require.NotContainsf(t, string(ub), "pending-pw-hash", "UserPending を JSON 化すると password hash が出る")
}
