package status

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shiroha-a/mk/plugin"
	"github.com/shiroha-a/mk/plugin/plugintest"
)

/*
 * plugin/plugintest を使うと、mk-go を起動せずにルートとジョブを直接叩ける。
 *
 * DB だけは本物を使う。SQL の挙動を模したフェイクは本物とずれ、通ったのに本番で
 * 落ちるテストになる。
 */

const testSchema = "plugin_status_test"

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	base := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		envOr("TEST_DB_HOST", "localhost"), envOr("TEST_DB_PORT", "5432"),
		envOr("TEST_DB_USER", "mk"), envOr("TEST_DB_PASS", "mk"),
		envOr("TEST_DB_NAME", "misskey_test"))

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close() //nolint:errcheck // 使い捨て
	for _, q := range []string{
		`DROP SCHEMA IF EXISTS ` + testSchema + ` CASCADE`,
		`CREATE SCHEMA ` + testSchema,
	} {
		if _, err := admin.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	db, err := sql.Open("pgx", base+" search_path="+testSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		if a, err := sql.Open("pgx", base); err == nil {
			_, _ = a.Exec(`DROP SCHEMA IF EXISTS ` + testSchema + ` CASCADE`)
			_ = a.Close()
		}
	})
	return db
}

// stubAPI answers users/show without a running mk-go.
type stubAPI struct{ suspended bool }

func (a *stubAPI) Anonymous() plugin.Caller    { return &stubCaller{api: a} }
func (a *stubAPI) AsUser(string) plugin.Caller { return &stubCaller{api: a} }

type stubCaller struct{ api *stubAPI }

func (c *stubCaller) Call(context.Context, string, any) (json.RawMessage, error) {
	return json.Marshal(map[string]any{"isSuspended": c.api.suspended})
}

func setup(t *testing.T, api plugin.API) (plugintest.Handlers, *sql.DB) {
	t.Helper()
	db := testDB(t)
	h := plugintest.New(t).WithName("status").WithDB(db).WithAPI(api).Routes(Plugin)
	return h, db
}

func TestSetAndShow(t *testing.T) {
	h, _ := setup(t, &stubAPI{})

	if _, err := h.Call(t, "POST /me/set",
		plugintest.Request{UserID: "u1", Body: `{"text":"作業中","duration":"1h"}`}); err != nil {
		t.Fatal(err)
	}

	res, err := h.Call(t, "POST /show", plugintest.Request{Body: `{"userId":"u1"}`})
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["set"] != true || m["text"] != "作業中" {
		t.Fatalf("想定と違う: %+v", m)
	}
}

// 未設定は「無い」であってエラーではない。表示側はこれを見て何も描かない。
func TestShow_Unset(t *testing.T) {
	h, _ := setup(t, &stubAPI{})

	res, err := h.Call(t, "POST /show", plugintest.Request{Body: `{"userId":"nobody"}`})
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["set"] != false {
		t.Fatalf("set=false であるべき: %+v", res)
	}
}

// **凍結された利用者の分は出さない。** 判断は mk-go の API に任せている。
func TestShow_HidesSuspendedUser(t *testing.T) {
	h, _ := setup(t, &stubAPI{suspended: true})

	if _, err := h.Call(t, "POST /me/set",
		plugintest.Request{UserID: "u1", Body: `{"text":"x","duration":""}`}); err != nil {
		t.Fatal(err)
	}

	res, _ := h.Call(t, "POST /show", plugintest.Request{Body: `{"userId":"u1"}`})
	if res.(map[string]any)["set"] != false {
		t.Fatalf("凍結された利用者の分は出さない: %+v", res)
	}
}

func TestRequiresLogin(t *testing.T) {
	h, _ := setup(t, &stubAPI{})

	for _, key := range []string{"POST /me", "POST /me/set"} {
		if _, err := h.Call(t, key, plugintest.Request{Body: `{"text":"x"}`}); err == nil {
			t.Fatalf("%s: 未ログインを弾いていない", key)
		}
	}
}

// **文字数は rune で数える。** len() だと日本語が 3 倍に数えられ、
// 「30文字まで」と言いながら 10 文字で弾くことになる。
func TestMaxLength_CountsRunes(t *testing.T) {
	h, _ := setup(t, &stubAPI{})

	ok := strings.Repeat("あ", 30)
	if _, err := h.Call(t, "POST /me/set",
		plugintest.Request{UserID: "u1", Body: `{"text":"` + ok + `","duration":""}`}); err != nil {
		t.Fatalf("30 文字は通る: %v", err)
	}

	ng := strings.Repeat("あ", 31)
	_, err := h.Call(t, "POST /me/set",
		plugintest.Request{UserID: "u1", Body: `{"text":"` + ng + `","duration":""}`})
	if err == nil {
		t.Fatal("31 文字は弾く")
	}
	if !strings.Contains(err.Error(), "30 文字") {
		t.Fatalf("上限が伝わらない: %v", err)
	}
}

// maxLength は運営者が伸ばせる。
func TestMaxLength_IsConfigurable(t *testing.T) {
	db := testDB(t)
	h := plugintest.New(t).WithName("status").WithDB(db).
		WithConfig(map[string]any{"maxLength": 50}).
		WithAPI(&stubAPI{}).Routes(Plugin)

	if _, err := h.Call(t, "POST /me/set",
		plugintest.Request{UserID: "u1", Body: `{"text":"` + strings.Repeat("あ", 50) + `","duration":""}`}); err != nil {
		t.Fatal(err)
	}
}

// 空文字は削除として扱う (UI から消せないと不便)。
func TestClearWithEmptyText(t *testing.T) {
	h, _ := setup(t, &stubAPI{})

	if _, err := h.Call(t, "POST /me/set",
		plugintest.Request{UserID: "u1", Body: `{"text":"x","duration":""}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Call(t, "POST /me/set",
		plugintest.Request{UserID: "u1", Body: `{"text":"  ","duration":""}`}); err != nil {
		t.Fatal(err)
	}

	res, _ := h.Call(t, "POST /me", plugintest.Request{UserID: "u1"})
	if res.(map[string]any)["text"] != nil {
		t.Fatalf("消えていない: %+v", res)
	}
}

func TestRejectsUnknownDuration(t *testing.T) {
	h, _ := setup(t, &stubAPI{})

	if _, err := h.Call(t, "POST /me/set",
		plugintest.Request{UserID: "u1", Body: `{"text":"x","duration":"100y"}`}); err == nil {
		t.Fatal("未知の表示期間を弾いていない")
	}
}

// **掃除ジョブを待たずに期限切れを隠す。** ジョブは 1 時間ごとなので、
// 読み取り側でも見ないと最大 1 時間ずれる。
func TestExpired_IsHiddenBeforePrune(t *testing.T) {
	h, db := setup(t, &stubAPI{})

	if _, err := h.Call(t, "POST /me/set",
		plugintest.Request{UserID: "u1", Body: `{"text":"x","duration":"1h"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE statuses SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}

	res, _ := h.Call(t, "POST /show", plugintest.Request{Body: `{"userId":"u1"}`})
	if res.(map[string]any)["set"] != false {
		t.Fatalf("期限切れは出さない: %+v", res)
	}
}

// --- ジョブ ---

func TestPrune_DeletesOnlyExpired(t *testing.T) {
	db := testDB(t)
	harness := plugintest.New(t).WithName("status").WithDB(db).WithAPI(&stubAPI{})
	routes := harness.Routes(Plugin)

	if _, err := routes.Call(t, "POST /me/set",
		plugintest.Request{UserID: "expired", Body: `{"text":"a","duration":"1h"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := routes.Call(t, "POST /me/set",
		plugintest.Request{UserID: "kept", Body: `{"text":"b","duration":""}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE statuses SET expires_at = now() - interval '1 hour' WHERE user_id = 'expired'`); err != nil {
		t.Fatal(err)
	}

	jobs := plugintest.New(t).WithName("status").WithDB(db).Jobs(Plugin)
	if err := jobs.Run(t, "prune", ""); err != nil {
		t.Fatal(err)
	}

	var users []string
	rows, err := db.Query(`SELECT user_id FROM statuses ORDER BY user_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close() //nolint:errcheck // 読み取りのみ
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			t.Fatal(err)
		}
		users = append(users, u)
	}
	if len(users) != 1 || users[0] != "kept" {
		t.Fatalf("無期限のものまで消している: %v", users)
	}
}

func TestPrune_IsScheduled(t *testing.T) {
	jobs := plugintest.New(t).WithName("status").WithDB(testDB(t)).Jobs(Plugin)

	if len(jobs.Schedules) != 1 || jobs.Schedules[0].Name != "prune" {
		t.Fatalf("想定と違う: %+v", jobs.Schedules)
	}
}

// 設定が壊れていたら起動を止める (黙って既定値で動かない)。
func TestBadConfigFailsSetup(t *testing.T) {
	_, err := loadConfig(plugintest.New(t).
		WithConfig(map[string]any{"maxLength": 9999}).Context())
	if err == nil {
		t.Fatal("範囲外の maxLength を弾いていない")
	}
}
