#!/usr/bin/env bash
# docs/plugins/authoring.md の Go スニペットが実際にコンパイルできるかを検査する。
#
# **推論では判別できないので実行する。** #2639 では「コンパイルできない例を直す」
# 作業自体が 4 周にわたって別のコンパイルエラーを作り続けた
# (Call の第 1 引数 -> no new variables -> declared and not used)。
#
#   make plugin-doc-check
#
# 断片は `lookup(...)` のようなプラグイン側のヘルパを意図的に省くので
# `undefined: X` だけは正常として捨てる。**それ以外のエラーは種類を問わず落とす**
# (allow-list にすると型の不一致を取りこぼす)。
#
# 一覧の署名の方は internal/entitycompat の TestPluginDoc_* が CI で見ている。
# こちらは別 module の解決が走るので CI には載せていない。doc を触ったら回すこと。
set -uo pipefail

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail() {
	echo "NG: $*" >&2
	exit 1
}

command -v go >/dev/null || fail "go が PATH に無い"
[ -f "$repo/go.mod" ] || fail "repo root を解決できない: $repo"

testify=$(awk '/^\tgithub.com\/stretchr\/testify /{print $1" "$2; exit}' "$repo/go.mod")
[ -n "$testify" ] || fail "go.mod から testify の版を読めない"

cat > "$work/go.mod" <<EOF
module plugindoccheck

go $(awk '/^go [0-9]/ {print $2; exit}' "$repo/go.mod")

require (
	github.com/shiroha-a/mk v0.0.0
	$testify
)

replace github.com/shiroha-a/mk => $repo
EOF
cp "$repo/go.sum" "$work/go.sum"

kept=$(python3 "$(dirname "${BASH_SOURCE[0]}")/extract.py" "$repo/docs/plugins/authoring.md" "$work") ||
	fail "fence の抽出に失敗した"
[ "$kept" -gt 0 ] || fail "検査対象の fence が 0 件 (doc の書式が変わった?)"
echo "検査対象の fence: $kept"

cd "$work" || fail "作業ディレクトリへ移動できない"

# -gcflags=all=-e: 既定はパッケージあたり 10 件でエラーを打ち切る。断片には
# 意図的な undefined が並ぶので、打ち切られると本物の欠陥が押し出される。
out=$(GOFLAGS=-mod=mod GOWORK=off go build -gcflags=all=-e ./snippets/... 2>&1)
rc=$?

# 断片ゆえに出るものだけを捨てる。ハーネスが置いたパッケージ変数の未使用も同様。
noise='undefined: |imported and not used|max \(built-in\) must be called'
real=$(printf '%s\n' "$out" | grep -vE "$noise" | grep -E 'snippets/s[0-9]+_[a-z]+/x\.go:[0-9]+:' || true)

# **fence ごとに 3 通りの文脈を試している。** どれか 1 つでも通れば良しとし、
# 全滅したものだけを報告する (断片の置かれる文脈は top-level 宣言 /
# `(any, error)` を返すハンドラ / `error` を返す routes・jobs の 3 通りある)。
fences=$(printf '%s\n' "$real" | sed -E 's|^snippets/(s[0-9]+)_[a-z]+/.*|\1|' | sort -u)
report=""
for f in $fences; do
	[ -n "$f" ] || continue
	survived=0
	for v in top any err; do
		printf '%s\n' "$real" | grep -q "snippets/${f}_${v}/" || survived=1
	done
	[ "$survived" -eq 1 ] && continue
	report="${report}$(printf '%s\n' "$real" | grep "snippets/${f}_any/")"$'\n'
done

if [ -n "${report//[[:space:]]/}" ]; then
	echo
	echo "NG: どの文脈に置いてもコンパイルできない fence がある"
	printf '%s' "$report"
	echo
	echo "  declared and not used  -> 値を実際に使うか if 文の中で受ける"
	echo "  no new variables       -> 2 度目以降は := ではなく ="
	echo "  does not implement     -> Call の第 1 引数は req.Context()"
	exit 1
fi

# **ビルドが成立しなかった場合を成功と取り違えない。** go が無い / go.sum が
# ずれた / module が解決できない等は上の grep に掛からないので、ここで落とす。
if [ $rc -ne 0 ] && [ -z "$real" ]; then
	echo
	echo "NG: ビルドが成立しなかった (検査できていない):"
	printf '%s\n' "$out"
	exit 1
fi

echo "OK: すべての fence がいずれかの文脈でコンパイルできる"
