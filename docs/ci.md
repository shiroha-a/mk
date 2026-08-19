# CI で回る項目

PR を出すと十数個の check が走る。**どれが何を見ていて、落ちたとき何を疑うか**の early reference。

## 全体像

`build` / `test` / `lint` の 3 つだけが **required check** (これが赤いとマージできない)。
残りは非ブロッキングで、落ちても merge 自体は可能。ただし非ブロッキングは「無視してよい」
意味ではなく、**merge をブロックするには不確実性が高い**という判断にすぎない。赤いまま
放置すると誰も見なくなるので、原因を切り分けてから進めること。

非ブロッキングを `continue-on-error: true` で実現している箇所は無い。あれは job を成功扱いに
するので失敗が完全に不可視になる。job は正しく失敗させ、required に入れないことで
非ブロッキングにしている。

## required check

| check | workflow | 見ているもの | 手元での再現 |
|---|---|---|---|
| `build` | CI | 全パッケージがコンパイルできるか | `go build ./...` |
| `lint` | CI | `go vet` + `gofmt -s -d` の差分 + 重複 fixture ID | `make lint` / `make fmt` |
| `test` | CI | 4-way shard の集約。どれか 1 つでも落ちれば赤 | `make test` |

### `test` が落ちたとき

実体は `test-shards (1)` 〜 `(4)`。集約 job のログではなく**失敗した shard のログ**を見る。

主な原因は 3 つ。

1. **カバレッジ閾値割れ** — パッケージごとに 90% (例外あり、CLAUDE.md Section 8 参照)。
   テストを足してから再 push する
2. **本物の失敗** — ローカルで `go test ./internal/<pkg>/... -count=1` を回して再現させる
3. **testcontainers の flaky** — PR と無関係なパッケージ (reaction の count_writer 等) で
   落ちていたら再実行を試す

### `lint` が落ちたとき

`gofmt` 差分なら `make fmt` を実行して再 push。`go vet` は repository interface に
メソッドを足したときに、手動 test fake が複数パッケージに散っていて漏れることが多い。
**push 前に `go vet ./...` を全体にかけること。**

## 非ブロッキングの check

| check | workflow | 見ているもの | 実測 | 手元での再現 |
|---|---|---|---|---|
| `vulncheck` | CI | 依存・Go stdlib の**到達可能な**既知脆弱性 + Go version の pin 整合 | 1 min | `GOOS=linux govulncheck ./...` |
| `frontend-check` | CI | fork frontend の型 (`vue-tsc --noEmit`) + `make plugins-all` と統合バイナリの build | 1.5 min | `make frontend-check` (型のみ) + `make plugins-all && go build ./cmd/misskey` |
| `plugin-tests` | CI | 同梱プラグインのテスト (別 module なので `go list ./...` に入らない) | 1 min | `make plugin-test` |
| `e2e (1/4)` 〜 `4/4` | Upstream backend e2e | **本家の backend e2e 1245 テスト**が mk-go に対して通るか | 3-7 min | `make upstream-e2e` |
| `diff` | Diff e2e | mk-go と TS の**レスポンスの値**が一致するか (endpoint 比較 30 件) | 4 min | `make diff-check` |
| `swap-test` | Drop-in e2e | TS→mk 切替で state が保たれるか | 5 min | `make dropin-swap-test` |
| `mkgo-born` | Drop-in e2e | **mk-go 生まれの DB を TS に引き渡せるか** (= ロックインの有無) | 5 min | `make dropin-mkgo-born-test` |
| `ed25519-verify` | Drop-in e2e | Fedibird-like mock との Ed25519 双方向 verify | 5 min | `make dropin-fedibird-test` |
| `federation` | Drop-in e2e | 本物の Misskey TS との実連合 (follow/note/reaction/renote/reply/mention/delete) | 4 min | `make federation-misskey-e2e` |
| `spec (mk-go 1/4)` 〜 `4/4` | Playwright | ブラウザからの統合互換 (289 spec ファイル) | 4-9 min | `make playwright-check` |
| `build-and-push` / `-bundled` | Docker | image がビルドできるか (PR では push しない) | 4 min | `docker build -f Dockerfile .` |

### e2e 系が「何を守っているか」の違い

守備範囲が重なっているように見えて、実は別のものを見ている。

| | 見ているもの |
|---|---|
| `e2e` (本家 backend e2e) | **本家のテストが通るか** |
| `diff` | **同じ入力に対する値そのもの** |
| shape drift (`test` に含まれる) | フィールドの有無・型 |
| `swap-test` | DB を引き継いだときに壊れないか |
| `mkgo-born` | **mk-go が作った DB を TS が受け取れるか** |
| `federation` / `ed25519-verify` | 他実装と実際に喋れるか |
| `vulncheck` | **自分のコードではなく依存**に既知の穴が無いか |

shape が合っていても値が違う類のバグは `diff` でしか捕まらない。ユニットテストは
「自分で署名して自分で検証する」ことしか保証しないので、相互運用は `federation` /
`ed25519-verify` でしか担保できない。

`vulncheck` だけは毛色が違い、**自分が書いたコードを一切見ない**。テストが全部通っていても
依存の既知脆弱性は素通りするので、別の signal として要る (導入時、通常テストが緑のまま
到達可能な脆弱性が 11 件見つかっている)。

`swap-test` と `mkgo-born` は似て見えるが、**DB を作った側が違う**。

|  | DB を作ったのは | 経路 |
|---|---|---|
| `swap-test` | TypeORM | TS → mk-go → TS |
| `mkgo-born` | **mk-go の migration** | mk-go → TS |

後者の方が厳しい。TS が一度も触っていない schema を受け取るので、カラム型・制約・
enum・index 名・default のどれかが TypeORM の期待とずれていれば起動しない。
`TestMigrationSeed_CoversUpstream` は seed 一覧と upstream の migration file を
**静的に突き合わせる**だけで、実際に TS を起動して確かめてはいない。

運用上これは**ロックインの有無そのもの**にあたる。「mk-go で始めた人が Misskey に
移れるか」に答えられるのはこの経路だけで、実際この経路の初回実行で、RSA 秘密鍵が
PKCS#1 のため TS 側の送信連合が全滅する不具合が見つかっている (#2380)。

### `e2e` (本家 backend e2e) が落ちたとき

まず **意図的な乖離かどうか**を判断する。mk-go では『通らないことが正しい』テストが
あり、`tests/upstream-e2e/known-divergences.json` に根拠付きで登録して expected-failure
として扱っている。

- 一覧に載っているテストが `Expect test to fail` で落ちた → **乖離が解消した**。一覧から外す
- 載っていないテストが落ちた → 互換性の regression。直す

`skip` ではなく expected-failure なのは、乖離が解消したときに気付けるようにするため。
詳細は [upstream-backend-e2e.md](upstream-backend-e2e.md)。

### `diff` が落ちたとき

**ignore-list を安易に広げないこと。** 空振りさせると本物の乖離が埋もれる。

mk-go 独自の additive field が原因なら `tests/diff/test_endpoints.py` の ignore-list に
**理由付きで**登録する。その際 [divergence.md](divergence.md) に対応する記述があるかを
確認すること。`META_IGNORE` と `USER_IGNORE` は別定義で後者は前者を継承していないので、
`policies` のように両方に現れるキーは両方へ足す必要がある。

そうでなければ値レベルの regression。条件付きの field 出し分けで壊すことが多い。

### drop-in / federation 系が落ちたとき

federation delivery に flaky 要素があるので、まず再実行を試す価値はある。ただし
**繰り返し落ちるなら本物**。失敗時は `docker compose logs` が
`dropin-logs-<scenario>` artifact として 14 日残るので、それを見る。

artifact には 2 種類のログが入る。`compose.log` / `ps.log` は orchestrator が
`down -v` する**前**に自分で残したもの、`compose-post.log` / `ps-post.log` は
workflow が後から集めたもの。前者がある場合はそちらが本命で、後者は stack が
既に撤去されていて空のことがある。

`mkgo-born` だけは落ち方が他と違い、原因が段階からほぼ特定できる。

| 落ちた段階 | 意味 |
|---|---|
| stage 4b (TS-A healthy 待ちで timeout) | mk-go の migration が作った schema を TypeORM が受け付けなかった |
| stage 4d (migrations digest 不一致) | migration seed (`000029`) に漏れがあり TS が再実行した |
| stage 5 (pytest) | schema は通ったがデータを読めない / 連合が続かない |

手元で再現するときは各 make target を直接叩く。いずれも専用の compose project
(`mk-dropin` / `mk-federation` / `mkdiff`) で隔離されており、**本番 UDS の project `mk` には
触れない**。

### `vulncheck` が落ちたとき

2 つの step があり、落ちた step で意味が違う。

**Go version pin の不一致** — `go.mod` の `go` directive と Dockerfile の builder tag が
ずれている。両方を同じ patch version に揃える。分けて検査しているのは、`govulncheck` が
見るのは `go.mod` 側だけで、**Dockerfile だけ古いと CI は緑のまま配る image が脆弱**に
なるため。builder を `golang:1.26-alpine` のような floating tag に戻すのも不可
(pull 時期で stdlib の patch が変わり、再現可能な形で「既知脆弱性を含まない」と言えない)。

**govulncheck の検出** — 手元で同じコマンドを回す。

```
go install golang.org/x/vuln/cmd/govulncheck@latest
GOOS=linux "$(go env GOPATH)/bin/govulncheck" ./...
```

`GOOS=linux` を付けるのは、実際にデプロイするのが Linux だから。付けないと host 依存の
package load エラーで解析が空振りしうる。**ローカルの `go` が古いと govulncheck 自身が
古い toolchain でビルドされ、`package requires newer Go version` で解析できない。**
その場合は `GOTOOLCHAIN=go1.26.6 go install ...` のように明示してビルドし直す。

検出されるのは**呼び出しが到達可能なもの**だけで、import しているだけの脆弱性は落ちない。
無視リストを育てずに運用できる設計なので、**抑制するより直すこと**。対応は原則 2 つ。

- 依存モジュール → `go get <module>@<fixed>` で修正版へ。**修正版の指定は govulncheck の
  `Fixed in:` をそのまま使う。** 同じモジュールに複数の脆弱性があると必要な版が別々で、
  一番低い版に上げても残ることがある
- Go stdlib → `go mod edit -go=<patch>` と Dockerfile の builder tag を両方上げる

新しい CVE が公開されると、**コードを変えていない PR でも落ちる**。これは required check に
していない理由でもある。落ちたときは自分の変更が原因とは限らないので、まず `Found in:` の
モジュールが PR で触ったものかを見ること。

### `frontend-check` が落ちたとき

fork frontend (`third_party/misskey`) の型崩れ。`make frontend-check` で再現する。

**`make uds-frontend-build` / `e2e-frontend-build` は検証に使わないこと。** 本番が
bind-mount している `third_party/misskey/built` を書き換えてしまう。`vue-tsc --noEmit` なら
出力物を作らない。

## nightly のみ

PR では回らない。失敗は Actions 上で確認して別 PR で対処する。

| workflow | 内容 | 時刻 |
|---|---|---|
| Drop-in frontend e2e | 3 TS インスタンス + cypress で frontend 視点の drop-in 互換 | 19:00 UTC |
| Queue-bench smoke | queue driver がジョブを落としていないか (`ok == sent`) | 17:30 UTC |

## 手動実行のみ

| workflow | 内容 | 実行方法 |
|---|---|---|
| Playwright (`spec (ts 1/4)` 〜 `4/4`) | 同じ spec を **Misskey TS backend** に対して実行し、spec が mk-go の挙動に引きずられていないかを検証 | upstream 追従で submodule を bump したとき |
| Docker (`workflow_dispatch`) | 過去のリリースタグから image を publish し直す | `gh workflow run docker.yml -f tag=1.1.1` |

`spec (ts …)` を常時回さないのは、upstream が変わらない限り答えが変わらないため。詳細は
#2289。

## 落ちたときの一般的な注意

**「手元では通るのに CI で落ちる」場合、手元の生成物を疑う。** 過去に何度も踏んでいる。

- `third_party/misskey/built/*` — 過去の docker build が root 所有で残っていることがある
- `packages/*/built` — 同上。`i18n` / `misskey-bubble-game` が無いと frontend の型が通らない
- `built/.config.json` / `built/meta.json` — 本家の `loadConfig()` が読む生成物

CI はまっさらな環境なので、手元にある生成物が要件を隠す。新しく CI に載せる workflow は
**その PR 自身の paths に含めて、PR 上で発火させて確認する**こと。

## 関連ドキュメント

- [`testing.md`](testing.md) — テストの種類と書き方
- [`upstream-backend-e2e.md`](upstream-backend-e2e.md) — 本家 e2e と既知乖離の運用
- [`diff-e2e.md`](diff-e2e.md) — 値レベル差分比較ハーネス
- [`dropin-e2e.md`](dropin-e2e.md) — drop-in 切替の検証
- [`shape-drift.md`](shape-drift.md) — entity shape / error id の drift gate
- [`divergence.md`](divergence.md) — 意図的な差分のカタログ
- [`contributing.md`](contributing.md) — コントリビューション手順
