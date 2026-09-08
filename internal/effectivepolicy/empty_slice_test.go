package effectivepolicy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 空の既定を持つ []string policy が JSON で `null` にならないこと (#2898)。
//
// **`append([]string(nil), 空...)` は nil を返す。** その結果
// `meta.policies.optOutNotificationTypes` が `null` として配信され、配列を
// 期待する frontend が壊れる (本番反映後の確認で実際に `null` が返っていた)。
// uploadableFileTypes は既定が非空なので、この穴は今まで露見していなかった。
func TestDefaults_EmptySliceMarshalsAsArray(t *testing.T) {
	got := Defaults()

	var sliceKeys int
	for key, value := range defaults {
		if _, ok := value.([]string); !ok {
			continue
		}
		sliceKeys++
		v, ok := got[key].([]string)
		require.True(t, ok, "%s は []string で返ること", key)
		require.NotNil(t, v, "%s: 空でも nil にしない (JSON で null になる)", key)

		b, err := json.Marshal(got[key])
		require.NoError(t, err)
		require.NotEqual(t, "null", string(b), "%s が JSON で null になっている", key)
	}
	require.NotZero(t, sliceKeys, "[]string の policy が 1 つも無い; この gate は何も検査していない")

	// 空の既定を持つキーが実在することも確かめる (非空だけだと穴を再現できない)。
	require.Equal(t, "[]", marshalKey(t, got, "optOutNotificationTypes"))
}

// コピーが元を共有しないこと (Defaults が「mutable copy」を返す契約)。
func TestDefaults_SliceIsCopied(t *testing.T) {
	a := Defaults()["uploadableFileTypes"].([]string)
	b := Defaults()["uploadableFileTypes"].([]string)
	require.NotEmpty(t, a)
	a[0] = "mutated"
	require.NotEqual(t, "mutated", b[0], "Defaults() の戻り値が元を共有している")
}

func marshalKey(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	b, err := json.Marshal(m[key])
	require.NoError(t, err)
	return string(b)
}
