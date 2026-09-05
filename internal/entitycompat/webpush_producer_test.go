package entitycompat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// webPushProducerAllowlist は production の呼び出し元を持たなくてよい Push*。
//
// **理由を持たせる。** 空にできる形だと「呼ぶ側を消して allowlist に足す」が
// 通ってしまう。件数も固定して、黙って増えないようにする。
var webPushProducerAllowlist = map[string]string{
	"PushUnreadAntennaNote": "upstream にも producer が無い (PushNotificationService.ts に型宣言と SW の受け口だけ)。" +
		"mk-go だけ送ると upstream に無い通知を出すことになるので送らない",
}

// Push* に production の呼び出し元があることを検査する (#2840)。
//
// **実装が揃っていても呼ぶ側だけ無い状態が実在した。** #2831 の
// readAllNotifications は `PushReadAllNotifications` の実装も
// `sw_subscription.sendReadMessage` 列も `/api/sw/*` の受け口も queue の
// processor も揃っているのに、**呼び出しが自分のテストからしか無かった**。
// nil は「push しない」なので build もテストも通り、**エラーもログも出ない** —
// 設定を ON にしても何も起きないだけで、気付く手段が無い。#2840 の
// newChatMessage も同じ状態だった。
//
// **対象を webpush.Service の Push* に絞る。** `internal/core/**` の孤立メソッド
// を一般に探す形は実測でノイズが多すぎた (39 件出て真の穴は 1 件。残りは
// テスト用の Set* seam と go-webauthn の interface 実装)。
func TestWebPushProducersAreWired(t *testing.T) {
	root := repoRoot(t)
	methods := webPushMethods(t, filepath.Join(root, "internal/core/webpush"))

	// **1 つも読めなかったら落とす。** 定義の書き方が変わって拾えなくなると、
	// 検査していないのに緑になる。
	if len(methods) == 0 {
		t.Fatal("webpush.Service の Push* を 1 つも読めなかった。定義の形が変わったならこの gate も直すこと")
	}

	// allowlist は実在する method だけを指すこと (綴り違いで空振りさせない)。
	for name := range webPushProducerAllowlist {
		if !containsString(methods, name) {
			t.Errorf("allowlist の %q は webpush.Service に存在しない。消したなら allowlist からも消すこと", name)
		}
	}

	callers := nonTestCallers(t, root, methods)
	for _, m := range methods {
		reason, allowed := webPushProducerAllowlist[m]
		switch {
		case callers[m] > 0 && allowed:
			t.Errorf("%s は production から呼ばれているのに allowlist に残っている。allowlist から消すこと (理由: %s)", m, reason)
		case callers[m] == 0 && !allowed:
			t.Errorf("%s に production の呼び出し元が無い。配線を忘れているか、"+
				"意図的に送らないなら理由付きで webPushProducerAllowlist へ足すこと。"+
				"nil は「push しない」なので build もテストも通り、エラーもログも出ない", m)
		}
	}
}

// webPushMethods returns the exported Push* method names declared anywhere in
// the webpush package.
//
// **AST で読む。** 文字列一致だと receiver 名を変えるだけで数え違える。
// **package ディレクトリ全体を見る。** 1 ファイルだけを parse する形だと、
// 別ファイルに Push* を足したときにゲートの視界から外れる (実測)。
func webPushMethods(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	var out []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, d := range file.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || !fn.Name.IsExported() || !strings.HasPrefix(fn.Name.Name, "Push") {
					continue
				}
				out = append(out, fn.Name.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// nonTestCallers counts references to each method from non-test Go files
// outside the webpush package itself.
func nonTestCallers(t *testing.T, root string, methods []string) map[string]int {
	t.Helper()
	counts := make(map[string]int, len(methods))
	for _, dir := range []string{"internal", "cmd", "plugin"} {
		walkGoFiles(t, filepath.Join(root, dir), func(path string, src []byte) {
			sep := string(filepath.Separator)
			// 定義そのものは数えない。
			if strings.Contains(path, sep+"core"+sep+"webpush"+sep) {
				return
			}
			// **testutil も数えない。** `_test.go` ではないので walk の除外に
			// 掛からないが、CLAUDE.md §4 が mock の置き場と定めている場所なので、
			// ここに mock を書くだけでゲートが空振りする (実測)。
			if strings.Contains(path, sep+"testutil"+sep) {
				return
			}
			// **コメント行は数えない。** コメントアウトして残すのは消すのと同じ
			// (CLAUDE.md §8 の wiring-check と同じ doctrine)。
			for _, line := range strings.Split(string(src), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				for _, m := range methods {
					if strings.Contains(trimmed, "."+m+"(") {
						counts[m]++
					}
				}
			}
		})
	}
	return counts
}

func walkGoFiles(t *testing.T, dir string, fn func(path string, src []byte)) {
	t.Helper()
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		fn(path, src)
		return nil
	})
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
