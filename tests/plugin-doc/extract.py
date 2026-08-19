"""docs/plugins/authoring.md の Go fence を、コンパイル検査用の使い捨てパッケージへ展開する。

check-snippets.sh から呼ばれる。標準出力へ展開した fence の数を出す。
"""

import pathlib
import re
import sys

# 断片が参照する変数は**パッケージ変数**として置く。関数の引数にすると、断片側の
# `id := req.Param("id")` が「no new variables」になり、「= に変えろ」という誤った
# 直し方を案内してしまう。パッケージ変数なら断片内の := は正当なシャドウになる。
HEADER = '''import (
\t"context"
\t"encoding/json"
\t"net/http"
\t"testing"

\t"github.com/shiroha-a/mk/plugin"
\t"github.com/shiroha-a/mk/plugin/plugintest"
\t"github.com/stretchr/testify/assert"
\t"github.com/stretchr/testify/require"
)

var (
\tctx                             plugin.Context
\treq                             plugin.Request
\tt                               *testing.T
\tid, userID, roleID, moderatorID string
\tparams                          map[string]any
\traw                             json.RawMessage
)

var _, _, _, _, _, _ = context.Background, json.Marshal, http.StatusOK, plugintest.New, require.NoError, assert.Equal
'''

# **断片ごとに置かれる文脈が違う** — top-level 宣言、`(any, error)` を返すハンドラの
# 中、`error` を返す routes / jobs の中。1 通りに決め打ちするとハーネス由来のエラーが
# 出て本物の欠陥と見分けがつかないので 3 通り展開し、呼び出し側が「どれか 1 つでも
# 通れば良し」と判定する。
VARIANTS = {
    "top": "package {pkg}\n\n{header}\n{body}",
    "any": "package {pkg}\n\n{header}\nfunc snippet() (any, error) {{\n{body}\n\treturn nil, nil\n}}\n",
    "err": "package {pkg}\n\n{header}\nfunc snippet() error {{\n{body}\n\treturn nil\n}}\n",
}


def main() -> int:
    doc = pathlib.Path(sys.argv[1]).read_text()
    work = pathlib.Path(sys.argv[2])

    kept = 0
    for i, body in enumerate(re.findall(r"```go\n(.*?)```", doc, re.S)):
        head = body.lstrip()
        # 完全なファイル例 (package 宣言つき) と、意図的に省略記号を含むものは対象外。
        if head.startswith("package ") or "..." in body:
            continue
        kept += 1
        for name, tmpl in VARIANTS.items():
            pkg = f"s{i:02d}_{name}"
            d = work / "snippets" / pkg
            d.mkdir(parents=True, exist_ok=True)
            (d / "x.go").write_text(tmpl.format(pkg=pkg, header=HEADER, body=body))
    print(kept)
    return 0


if __name__ == "__main__":
    sys.exit(main())
