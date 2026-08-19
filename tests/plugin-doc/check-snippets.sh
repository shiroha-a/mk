#!/usr/bin/env bash
# docs/plugins/authoring.md の Go スニペットを、**実際に噛んだ 3 クラスの
# コンパイルエラー**について検査する。
#
#   declared and not used   値を受けたまま使っていない
#   no new variables        2 度目以降も := で受けている
#   does not implement      plugin.Context を context.Context の位置に渡している等
#
# **推論では判別できないので実行する。** #2639 では「コンパイルできない例を直す」
# 作業自体が 4 周にわたって別のコンパイルエラーを作り続けた
# (Call の第 1 引数 -> no new variables -> declared and not used)。
#
#   ./tests/plugin-doc/check-snippets.sh
#
# **完全なコンパイル検査ではない。** doc の断片は `lookup(...)` のようなプラグイン
# 側のヘルパを意図的に省くので、`undefined: X` は正常。上の 3 クラスだけを見る。
# 一覧の署名の方は internal/entitycompat の TestPluginDoc_* が CI で見ている。
#
# CI には載せていない (別 module の解決が走るため)。doc を触ったら手で回すこと。
set -uo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cat > "$work/go.mod" <<EOF
module plugindoccheck

go $(awk '/^go [0-9]/ {print $2; exit}' "$repo/go.mod")

require (
	github.com/shiroha-a/mk v0.0.0
	github.com/stretchr/testify v1.11.1
)

replace github.com/shiroha-a/mk => $repo
EOF
cp "$repo/go.sum" "$work/go.sum"

python3 - "$repo/docs/plugins/authoring.md" "$work" <<'PY'
import pathlib, re, sys

doc, work = pathlib.Path(sys.argv[1]).read_text(), pathlib.Path(sys.argv[2])
header = '''import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/shiroha-a/mk/plugin"
	"github.com/shiroha-a/mk/plugin/plugintest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _, _, _, _, _, _ = context.Background, json.Marshal, http.StatusOK, plugintest.New, require.NoError, assert.Equal
'''
kept = 0
for i, body in enumerate(re.findall(r"```go\n(.*?)```", doc, re.S)):
    head = body.lstrip()
    if head.startswith("package ") or "..." in body:
        continue  # 完全なファイル例 / 意図的な省略記号
    kept += 1
    pkg = work / "snippets" / f"s{i:02d}"
    pkg.mkdir(parents=True, exist_ok=True)
    if head.startswith(("var ", "func ", "type ", "const ")):
        src = f"package s{i:02d}\n\n{header}\n{body}"
    else:
        # 断片は「ルートハンドラの中身」か「テストの中身」。呼び出し側の変数を仮に置く。
        src = (f"package s{i:02d}\n\n{header}\n"
               "func f(ctx plugin.Context, req plugin.Request, t *testing.T, "
               "id, userID, roleID, moderatorID string, params map[string]any, "
               "raw json.RawMessage) (any, error) {\n" + body + "\n\treturn nil, nil\n}\n")
    (pkg / "x.go").write_text(src)
print(f"検査対象の fence: {kept}")
PY

cd "$work" || exit 1
out=$(GOFLAGS=-mod=mod GOWORK=off go build ./snippets/... 2>&1)
hits=$(printf '%s\n' "$out" | grep -E "declared and not used|no new variables|does not implement" || true)

if [ -z "$hits" ]; then
	echo "OK: 対象 3 クラスのエラーなし"
	exit 0
fi
echo
echo "NG:"
printf '%s\n' "$hits"
echo
echo "  declared and not used  -> 値を実際に使うか if 文の中で受ける"
echo "  no new variables       -> 2 度目以降は := ではなく ="
echo "  does not implement     -> Call の第 1 引数は req.Context()"
exit 1
