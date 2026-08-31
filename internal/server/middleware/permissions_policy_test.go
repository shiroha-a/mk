package middleware

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermissionsPolicy_AppliesEverywhere(t *testing.T) {
	for _, p := range []string{"/", "/api/meta", "/files/abc.pdf", "/embed/notes/abc"} {
		assert.Equal(t, permissionsPolicyValue,
			serveHeaderMW(t, PermissionsPolicy(), p).Header().Get("Permissions-Policy"), "path=%s", p)
	}
}

// **値の中身を固定する。** 「header が付いている」だけを見ると、機能を全部
// 許可する値に変えても通ってしまう。
func TestPermissionsPolicy_Value(t *testing.T) {
	got := serveHeaderMW(t, PermissionsPolicy(), "/").Header().Get("Permissions-Policy")
	require.NotEmpty(t, got)

	// 使わないと確認した機能は空 allowlist で落とす。
	for _, feature := range []string{"microphone", "geolocation", "payment"} {
		assert.Containsf(t, got, feature+"=()", "%s を落としていない", feature)
	}

	// **落としてはいけないもの。** ここに現れたら実際に機能が壊れる:
	//
	//   - camera: /qr の読み取りタブが qr-scanner 経由で getUserMedia を呼ぶ
	//   - fullscreen: iframe プレイヤーの allow と native <video controls>
	//   - display-capture: Sentry のフィードバック用スクリーンショット
	for _, feature := range []string{"camera", "fullscreen", "display-capture"} {
		assert.NotContainsf(t, got, feature,
			"%s を落とすと実際に機能が壊れる (permissions_policy.go のコメントを参照)", feature)
	}

	// 空 allowlist 以外の指定が混ざっていないこと (`microphone=*` のような緩和)。
	for _, part := range strings.Split(got, ",") {
		assert.Truef(t, strings.HasSuffix(strings.TrimSpace(part), "=()"),
			"空 allowlist 以外の指定がある: %q", strings.TrimSpace(part))
	}
}
