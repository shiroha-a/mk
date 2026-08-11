package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/config"
)

// secretBearingConfig builds a config where every secret-carrying field holds a
// distinctive value, so a leak is unmistakable.
func secretBearingConfig() *config.Config {
	return &config.Config{
		URL:  "https://example.com",
		Port: 3000,
		ID:   "aidx",
		DB: config.DBOptions{
			Host: "db.internal", Port: 5432, DB: "misskey", User: "mk",
			Pass: "LEAK-db-pass",
		},
		Redis:             config.RedisOptions{Host: "r", Port: 6379, Pass: "LEAK-redis-pass"},
		RedisForJobQueue:  config.RedisOptions{Host: "r", Port: 6379},
		RedisForPubsub:    config.RedisOptions{Host: "r", Port: 6379},
		RedisForTimelines: config.RedisOptions{Host: "r", Port: 6379},
		RedisForReactions: config.RedisOptions{Host: "r", Port: 6379},
		SetupPassword:     "LEAK-setup-password",
		MediaProxySecret:  []byte("LEAK-media-proxy-secret"),
		Proxy:             "http://user:LEAK-proxy-pass@proxy.internal:3128",
		ProxySmtp:         "http://user:LEAK-smtp-pass@smtp-proxy.internal:3128",
	}
}

// **秘密が出力に混じらないことを固定する。** 診断のための機能が認証情報の
// 漏洩経路になってはいけない。新しい設定項目を dump に足すときは、ここに
// 値を積んで落ちないことを確かめること。
func TestBuildConfigDump_NeverLeaksSecrets(t *testing.T) {
	d := BuildConfigDump(secretBearingConfig(), config.RoleBoth)
	rendered := RenderConfigDump(d)

	for _, secret := range []string{
		"LEAK-db-pass", "LEAK-redis-pass", "LEAK-setup-password",
		"LEAK-media-proxy-secret", "LEAK-proxy-pass", "LEAK-smtp-pass",
	} {
		assert.NotContainsf(t, rendered, secret, "%s が出力に漏れている", secret)
	}
}

// 有無は分かるようにする。「未設定」と「設定済み」の区別は診断に要る。
func TestBuildConfigDump_ShowsWhetherSecretsAreSet(t *testing.T) {
	set := RenderConfigDump(BuildConfigDump(secretBearingConfig(), config.RoleBoth))
	assert.Contains(t, set, redactedPlaceholder)

	bare := &config.Config{URL: "https://example.com", Port: 3000}
	unset := RenderConfigDump(BuildConfigDump(bare, config.RoleBoth))
	assert.Contains(t, unset, unsetPlaceholder)
}

// proxy の host は診断に要るので残し、認証情報だけ落とす。
func TestRedactURLUserinfo(t *testing.T) {
	assert.Equal(t, "http://<マスク>@proxy.internal:3128",
		redactURLUserinfo("http://user:pw@proxy.internal:3128"))
	// userinfo が無ければそのまま。
	assert.Equal(t, "http://proxy.internal:3128", redactURLUserinfo("http://proxy.internal:3128"))
	assert.Equal(t, unsetPlaceholder, redactURLUserinfo(""))
	// parse できない値でも落とさない (診断が止まる方が困る)。
	assert.NotEmpty(t, redactURLUserinfo("://bad"))
}

// **本機能の要点。** rate limit は worker ごとに効くので、設定名から想像する
// 「queue 全体の上限」にはならない。実効値と警告の両方を出す。
func TestBuildConfigDump_SurfacesRateTimesConcurrency(t *testing.T) {
	four, hundred := 4, 100
	cfg := &config.Config{
		URL: "https://example.com", Port: 3000,
		DeliverJobConcurrency: &four,
		DeliverJobPerSec:      &hundred,
	}
	d := BuildConfigDump(cfg, config.RoleBoth)

	var rate string
	for _, e := range d.Effective {
		if e.Key == "rate: deliver" {
			rate = e.Value
		}
	}
	assert.Equal(t, "400 jobs/sec", rate, "設定値 100 × worker 4")

	require.NotEmpty(t, d.Warnings)
	assert.Contains(t, strings.Join(d.Warnings, "\n"), "400 jobs/sec")
}

// worker が 1 なら設定値と実効値が一致するので警告しない (ノイズにしない)。
func TestBuildConfigDump_NoWarningWhenSingleWorker(t *testing.T) {
	one, fifty := 1, 50
	cfg := &config.Config{
		URL: "https://example.com", Port: 3000,
		DeliverJobConcurrency: &one,
		DeliverJobPerSec:      &fifty,
	}
	d := BuildConfigDump(cfg, config.RoleBoth)
	assert.Empty(t, d.Warnings)
}

// rate 未設定なら rate 行を出さない。
func TestBuildConfigDump_NoRateEntryWhenUnset(t *testing.T) {
	d := BuildConfigDump(&config.Config{URL: "https://example.com", Port: 3000}, config.RoleBoth)
	for _, e := range d.Effective {
		assert.NotContains(t, e.Key, "rate: ")
	}
	assert.Empty(t, d.Warnings)
}

// 既定値と明示指定を区別する。どこから来た値かが分からないと診断に使えない。
func TestBuildConfigDump_MarksExplicitOverrides(t *testing.T) {
	eight := 8
	cfg := &config.Config{URL: "https://example.com", Port: 3000, InboxJobConcurrency: &eight}
	d := BuildConfigDump(cfg, config.RoleBoth)

	notes := map[string]string{}
	for _, e := range d.Effective {
		notes[e.Key] = e.Note
	}
	assert.Equal(t, "明示指定", notes["worker: inbox"])
	assert.Equal(t, "既定値", notes["worker: deliver"])
}

// role が反映されること (#2459)。
func TestBuildConfigDump_ShowsProcessRole(t *testing.T) {
	for _, tc := range []struct {
		role config.ProcessRole
		want string
	}{
		{config.RoleBoth, "both"},
		{config.RoleServer, "server"},
		{config.RoleQueue, "queue"},
	} {
		d := BuildConfigDump(&config.Config{URL: "u", Port: 1}, tc.role)
		var got string
		for _, e := range d.Effective {
			if e.Key == "process role" {
				got = e.Value
			}
		}
		assert.Equal(t, tc.want, got)
	}
}

// socket 指定時は port を出さない (使われないので出すと誤解を招く)。
func TestBuildConfigDump_SocketHidesPort(t *testing.T) {
	d := BuildConfigDump(&config.Config{URL: "u", Port: 3000, Socket: "/run/mk.sock"}, config.RoleBoth)
	keys := map[string]bool{}
	for _, e := range d.Settings {
		keys[e.Key] = true
	}
	assert.True(t, keys["socket"])
	assert.False(t, keys["port"])
}

// clusterLimit は mk-go では no-op。設定されていたらその旨を示す。
func TestBuildConfigDump_ClusterLimitIsMarkedNoOp(t *testing.T) {
	n := 4
	d := BuildConfigDump(&config.Config{URL: "u", Port: 1, ClusterLimit: &n}, config.RoleBoth)
	var note string
	for _, e := range d.Effective {
		if e.Key == "clusterLimit" {
			note = e.Note
		}
	}
	assert.Contains(t, note, "no-op")
}
