# Misskey TS upstream 追従アップデート手順

mk-go は `third_party/misskey` submodule で Misskey TS の特定 release tag を pin して、frontend asset + drop-in 互換性の参照点として利用している。upstream の新 release が出るたびに backend 差分の triage + 取り込み + submodule bump を行う必要がある。

本書は **submodule bump を含む PR がマージされた後、各開発者 / operator が必要な手順** と、**新 upstream release が出た時の triage 運用** を説明する。

---

## 1. 既存環境への適用 (= submodule bump PR マージ後)

`git pull` だけでは submodule の working tree は更新されない (= 親リポの gitlink ポインタが移動するのみ)。`third_party/misskey/` 配下の実 file を新 release に揃えるには明示的な submodule update が必要。

### 1-1. 推奨: 1 コマンドで pull + submodule update

```bash
git pull --recurse-submodules
```

このフラグは `git pull` 単体だと毎回つける必要がある。常時 on にしたい場合は次の設定を 1 度実行:

```bash
git config submodule.recurse true
```

→ 以後 `git pull` / `git checkout` / `git merge` で自動的に submodule も追従する。`.git/config` (= repo local) に書かれるため、他開発者には伝播しない。各人が一度設定すること。

### 1-2. 手動 (一括設定なし)

```bash
git pull
git submodule update --init --recursive
```

### 1-3. 確認

```bash
git -C third_party/misskey describe --tags HEAD
# → 例: 2026.5.1-mk.0
git -C third_party/misskey log --oneline HEAD -1
# → 例: 8c292244e7 fix(frontend): add null guard for MkModal content children[0]
```

### 1-4. frontend asset の rebuild が必要なケース

`third_party/misskey/packages/frontend/` の vite ビルド成果物を mk-go が serve しているため、submodule bump 後に **frontend asset を再ビルド** しないと UI に古い JS が残る:

```bash
make uds-frontend-build
# = make e2e-frontend-build と同じ (alias)
```

数分〜10分かかる。docker daemon が必要。UDS / dropin / e2e いずれも同じビルド成果物を共有する。

### 1-5. UDS production stack の再ビルド

[compose.uds.yaml](../compose.uds.yaml.example) で本番運用している場合、Misskey TS の prebuilt image を pull しているわけではなく **mk-go バイナリ + submodule の静的アセットを image に焼き込んでビルドしている** ([`deploy/uds/Dockerfile.mkgo`](../deploy/uds/Dockerfile.mkgo) の `COPY . .` 経由)。submodule update + frontend rebuild 後に image を作り直さないと古い asset が image にキャッシュされたまま:

```bash
# 1-1 / 1-2 と 1-4 を済ませた状態 (= submodule + frontend asset が最新) で
docker compose -f compose.uds.yaml up --build -d
```

**重要**:
- `--build` フラグ必須。`docker compose up -d` 単体だと前回 build 済の image が再利用され、submodule 更新が反映されない
- 確実に再ビルドさせたい場合は `--build --force-recreate` を併用
- `make uds-frontend-build` を skip すると Dockerfile builder の sanity check (`test -f .../1f004.svg` 等) で早期 fail する

`postgres` / `valkey` / `nginx` / `video-thumb` 等の外部 image は Misskey 無関係なので submodule bump で影響を受けない。

### 1-6. migration 適用

submodule bump PR には mk-go 側の migration が同梱されることが多い (例: PR #998 の `migration/000048_avatar_decoration_category.{up,down}.sql`)。本番環境では:

```bash
# 接続先は -config (既定 .config/default.yml) から決まる
make migrate-up
```

migration 連番は `migration/00NNNN_*.up.sql` の命名規則に従う (= 各 migration の番号は前との連続性を保つ)。

---

## 2. 新 upstream release 取り込み手順 (= 開発側)

新 Misskey TS release が出た時、mk-go 側で必要な作業フロー:

### 2-1. tracker issue を起票

`gh issue create --title "Tracker: Misskey TS <prev> → <new> への upstream 追従"` で tracker を作成。次の内容を含める:

- 対象 release tag (例: `2026.5.1`)
- backend 関連 commits 一覧 (`git -C third_party/misskey log --oneline --no-merges <prev>..<new> -- packages/backend/src/ packages/backend/migration/`)
- 関連 frontend / TS-only 変更の参考リスト
- 完了条件 (= sub-issue 全 close + submodule bump PR マージ)

### 2-2. triage doc を作成

`docs/update/yyyymmdd-<tracker-issue>-triage.md` を新規作成。前例: `docs/update/20260512-947-triage.md`。

各 upstream commit について:
- `git -C third_party/misskey show <sha>` で diff 精読
- mk-go 該当箇所を `grep` で特定し file_path:line_number で記録
- Gap 判定 (`既対応 / 部分対応 / 未対応 / 影響なし`)
- 推定難易度 (`S / M / L / N/A`)
- 推定実装方針

末尾に Wave 1-N の推奨実行順 (= まず close 候補をまとめてから S → M → L の順に進む) を記載すると後続作業が読みやすい。

### 2-3. sub-issue 化

triage で判定した item を `gh issue create` で 1 件 1 issue として起票。命名規則 `#<tracker> sub-N (upstream #XXXXX): <要約>`。

各 sub-issue 本文には:
- 親 tracker への参照 (`parent tracker: #<n>`)
- triage doc の該当 item への参照 (`triage detail: PR #<n> (\`docs/update/...\` の item N)`)
- 概要 / 実装方針 / 完了条件

**注意**: GitHub の cross-repo 自動 link を避けるため、upstream PR 参照は `upstream PR <N>` (plain text、`misskey-dev/misskey#N` 形式は使わない) と書く。

### 2-4. submodule bump + Wave 単位の実装 PR

実装方針 (PR #998 で確立):

1. **Infrastructure 先行**: submodule bump (新 tag) + `MisskeyVersion` 定数更新 + hardcode 修正
2. **Wave 1 (close 候補)**: comment + regression test で意思表明
3. **Wave 2 (S 難易度)**: 1 commit / 1 sub-issue (or 関連を bundle) で順次
4. **Wave 3 (M 難易度)**: PR 1 本ずつ / commit 1 件ずつで review しやすく
5. **Wave 4 (L 難易度)**: submodule bump とセット (例: 削除 endpoint)
6. **Final audit**: 残り upstream commits も triage 突き合わせて drift を確認、結果を triage doc 末尾に追記
7. **Follow-up**: review で挙がった improvement を nit commit で取り込む

各 commit は `2026.X.Y Wave N (M/N): <要約>` 命名で、`Closes #<sub-issue>` で sub-issue を自動 close する。

### 2-5. submodule bump 時の fork 運用

mk-go は `shiroha-a/misskey-ts` fork を経由して submodule を pin している (= upstream の release tag + mk 固有のパッチを cherry-pick したもの)。新 release を取り込む手順:

```bash
cd third_party/misskey
git fetch upstream <tag>
# 例: <tag>=2026.5.1
git checkout -b <tag>-fix <tag>
git cherry-pick <既存 patch sha>  # 例: 79ccc36ec0 (MkModal null guard)
git tag <tag>-mk.0
git push origin <tag>-fix
git push origin <tag>-mk.0
cd -
git add third_party/misskey
# commit + PR
```

`<tag>-mk.N` の `N` は revision 番号。同 release base で追加 patch が増えたら `.1` `.2` と上げる。

### submodule bump 後に必須: shape drift snapshot の再生成

`third_party/misskey` を bump したら、entity shape drift gate の golden snapshot を
再生成して commit すること。新バージョンで追加 / 変更された契約フィールドが次回の
`TestEntityShapeDrift` に反映される。

```bash
make shapecheck-gen           # internal/entitycompat/testdata/ の golden を全て再生成
make shapecheck               # gate がまだ通るか確認 (新規 drift が出たら allowlist or 修正)
go test ./internal/entitycompat/   # schema / migration seed gate も含めて確認
git add internal/entitycompat/testdata/
```

詳細は [shape-drift.md](./shape-drift.md)。

### submodule bump 後に必須: TypeORM migrations seed の追加

upstream に新しい migration が入った場合、`migrations` テーブルへの seed も追加する。
これが漏れると、mk-go で動かした DB に本家を繋ぎ直したときに TypeORM が当該
migration を未実行と判定して**再実行**し、適用済み DDL への `ADD COLUMN` 重複や
`DROP COLUMN` によるデータ喪失につながりうる (#2244)。

`TestMigrationSeed_CoversUpstream` が漏れを検出するので、落ちたら
`migration/000067_migrations_typeorm_names.up.sql` と同じ形式で seed を足す。

**seed する前に、その migration の DDL が mk-go 側にも入っているか必ず確認すること。**
入っていないまま seed すると、本家が「適用済み」と誤認して skip し、schema が
ずれたまま放置される。DDL が未実装なら先に mk-go 側の migration を書く。

### submodule bump 後に必須: index golden の再生成

upstream が index を足した場合、`golden_upstream_indexes.json` も撮り直す。これは
TypeORM の decorator から正規形を再現できないため **実 DB から採る** 必要がある
(手順は [shape-drift.md](./shape-drift.md#golden-の再生成))。

撮り直したら `TestIndexNaming_NoNewUpstreamDuplicates` を走らせる。mk-go 側に
同内容・別名の index があれば検出されるので、upstream 名に揃えるか
`known_duplicate_indexes.json` に追加して `000068` の扱いを見直す (#2246)。

### submodule bump 後に必須: divergence doc の件数

`golden_upstream_columns.json` を撮り直すと `TestDivergenceDoc_ColumnCountMatchesSchema` が動く。**upstream が列を DROP すると、その列は「mk-go 独自カラム」に転じる**ので `docs/divergence.md` §2-2 の件数が増える (`note_favorite.createdAt` がその経緯で独自列になっている)。

落ちたら doc の件数・内訳・冒頭サマリ・表の行をまとめて直す。gate は 4 箇所すべてを見るので、どれか 1 つを直し忘れると通らない (#2634)。

### submodule bump 後に必須: 比較対象の TS image を全部揃える

mk-go と Misskey TS を並べて比較するハーネスは、**比較対象の image tag を
`MisskeyVersion` と同じ版に上げる**こと。ここがずれていると upstream 自身の
バージョン間差分が差分として出てしまい、mk-go 固有の乖離と区別できない。

| ファイル | 対象 |
|---|---|
| `docker-compose.diff.yml` | 差分比較ハーネス ([diff-e2e.md](./diff-e2e.md)) |
| `docker-compose.playwright.ts.yml` | Playwright の TS baseline |
| `.github/workflows/playwright.yml` | 上記の pre-pull (tag が sync していないと pull が無駄になる) |

**除外リストの「version-gap」注記は、版を揃えたら必ず読み直す。** 実例として、
diff harness の `META_IGNORE` には `app192IconUrl` / `app512IconUrl` /
`singleUserMode` が「mk-go 2026.6.0 が持ち TS 2026.5.4 に無い」として除外されて
いたが、TS を 2026.7.0 に揃えたら 3 件とも残った。実際は upstream では
`admin/meta` にしか無く公開 `/api/meta` には元から含まれない = **mk-go の余剰
フィールド**で、版ずれが誤診断を固定していた (#2303)。

### submodule bump 後に必須: TS baseline で Playwright を回す

```bash
gh workflow run playwright.yml --ref <branch>   # TS backend も含めて実行される
# または手元で
make playwright-ts-up && make playwright-ts-test && make playwright-ts-down
```

Playwright spec は普段 mk-go backend に対してしか走っていない (PR トリガーでも
mk-go のみ)。**TS backend に対して回すのは upstream 追従のタイミングだけ**という
運用にしている。

理由は、spec が「mk-go の挙動を正解として」書かれてしまう事故を、追従の節目で
検出するため。実際 #2276 で 3 ヶ月ぶりに TS backend で回したところ、spec が
mk-go 側の挙動に引きずられていた箇所が 19 件見つかり、そのうち 5 件は mk-go の
実バグだった (#2283 renoteCount の加算条件 / #2284 必須パラメータの未検証 /
#2285 `user.updatedAt` のセマンティクス / #2286 ユーザー検索の実装乖離 /
#2287 余剰フィールド)。

一方で常時 (nightly や PR で) 回す価値は薄い。同一 CI 環境・同一 spec で
所要時間を比較すると mk-go と TS に実用上の差は無く (TS/mk-go の中央値 0.94)、
得られるのは所要時間ではなく **spec の前提が upstream とずれていないか**という
一点だけだから。upstream が変わらない限りその答えも変わらない。

失敗した spec を見るときは以下に注意する。

- mk-go には `docs/divergence.md` に記録した**意図的な差分**がある
  (例: `NO_SUCH_*` を upstream は 400、mk-go は意味的に正確な 404 で返す)。
  spec 側は `tests/playwright/fixtures/backend.ts` の `NOT_FOUND_STATUS` の
  ように backend ごとの期待値で吸収する。ただし**この逃げ道を足すたびに、その
  spec は parity を証明しなくなる**ので、安易に増やさない
- upstream 固有の前提でしか成立しない挙動もある (例: `state:'alive'` は
  `updatedAt > now-5d` で絞るが、upstream が local user の `updatedAt` を
  更新するのは note 投稿時だけなので、signup 直後の user は一覧に出ない)。
  この種は spec の前提条件を直す

### mk-go 側の migration を書くときの必須ルール

mk-go の migration は Misskey TS が作った既存 DB にも流れる。以下は
`TestMigrationIdempotency_RequiresIfExists` が強制する。

- `CREATE TABLE` / `ADD COLUMN` / `CREATE INDEX` は必ず `IF NOT EXISTS`
- `DROP TABLE` / `DROP COLUMN` / `DROP INDEX` は必ず `IF EXISTS`
- upstream に同じ内容の index があるなら **upstream の index 名をそのまま使う**
  (`000058` が前例)。名前が違うと `IF NOT EXISTS` が効かず TS 製 DB で二重化する

---

## 3. 参考リンク

- 直近の triage 例: [`docs/update/20260512-947-triage.md`](./update/20260512-947-triage.md)
- upstream release 差分まとめ: `docs/update/yyyymmdd*` 命名規則
- PR #998: 2026.3.2 → 2026.5.1 一括取り込みの reference 実装 (= Infrastructure + Wave 1-4 + follow-up audit + #17034)
- [api-compatibility.md](./api-compatibility.md): 互換性追跡
- [migration-from-ts.md](./migration-from-ts.md): TS → mk-go drop-in 切替
