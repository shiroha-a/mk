package processors_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/processors"
)

// captureLogs swaps the default slog handler for the duration of the test.
//
// **JSON で取る。** text 形式だと `err` の中の `signer=...` と属性としての
// `signer=...` を区別できず、専用の属性を落とす変異を見逃す (#2716)。
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// findLogRecord returns the first captured record whose msg contains want.
func findLogRecord(t *testing.T, buf *bytes.Buffer, want string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if msg, _ := rec["msg"].(string); strings.Contains(msg, want) {
			return rec
		}
	}
	t.Fatalf("log record containing %q not found in:\n%s", want, buf.String())
	return nil
}

// activity を捨てたときに**相手が判る**こと (#2716)。
//
// 実運用では 24 時間の警告の半分がこの破棄で、しかも `host=""` で相手が一切
// 残っていなかった。activity を捨てる経路なので、誤って落としていた場合に
// 気付く手段がこのログしか無い。
func TestInboxProcessor_DropLogIdentifiesTheSender(t *testing.T) {
	// 署名者 (alice) と body の actor (mallory) を食い違わせる = 転送活動扱い。
	// LD-Signature が無いので破棄される。
	body := []byte(`{"id":"https://other.example/notes/1/activity","type":"Create","actor":"https://other.example/users/mallory","object":{"id":"https://other.example/notes/1","type":"Note"}}`)
	v, payload := signedReplayFixture(t, body)

	buf := captureLogs(t)
	stub := &stubFedProcessor{}
	p := processors.NewInboxProcessor(stub)
	p.SetSignatureVerifier(v)
	require.NoError(t, p.Handle(context.Background(), queue.NewInboxTask(payload)))

	// **判定は変えていない。** 転送活動で LD-Signature が無ければ従来どおり捨てる。
	assert.Empty(t, stub.calls, "捨てるはずの activity が処理されている")

	// **属性ごとに見る。** `err` にも signer / actor が入っているので、
	// 文字列の含有だけでは専用の属性を落とす変異を見逃す。
	rec := findLogRecord(t, buf, "dropping activity")
	assert.Equal(t, "remote.example", rec["host"], "署名者の host が出ていない")
	assert.Equal(t, "https://remote.example/users/alice", rec["signer"], "signer が出ていない")
	assert.Equal(t, "https://other.example/users/mallory", rec["actor"], "body の actor が出ていない")
	assert.Equal(t, "Create", rec["activityType"], "activity の type が出ていない")
}

// ExtractActivityType は inbox gate と同じ unwrap+Normalize を通ること (#2716)。
//
// 生 body を直接見ると、配列 wrap や `as:type` で type が読めずログが空になる。
func TestExtractActivityType(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"plain", `{"type":"Create","actor":"https://h/u"}`, "Create"},
		{"singleton array wrap", `[{"type":"Announce","actor":"https://h/u"}]`, "Announce"},
		{"array type takes the first", `{"type":["Create","Public"],"actor":"https://h/u"}`, "Create"},
		{"missing", `{"actor":"https://h/u"}`, ""},
		{"broken json", `{`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, federation.ExtractActivityType([]byte(tc.body)))
		})
	}
}
