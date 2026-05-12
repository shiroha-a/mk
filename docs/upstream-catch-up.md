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

### 1-5. migration 適用

submodule bump PR には mk-go 側の migration が同梱されることが多い (例: PR #998 の `migration/000048_avatar_decoration_category.{up,down}.sql`)。本番環境では:

```bash
DATABASE_URL='postgres://...' make migrate-up
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

---

## 3. 参考リンク

- 直近の triage 例: [`docs/update/20260512-947-triage.md`](./update/20260512-947-triage.md)
- upstream release 差分まとめ: `docs/update/yyyymmdd*` 命名規則
- PR #998: 2026.3.2 → 2026.5.1 一括取り込みの reference 実装 (= Infrastructure + Wave 1-4 + follow-up audit + #17034)
- [api-compatibility.md](./api-compatibility.md): 互換性追跡
- [migration-from-ts.md](./migration-from-ts.md): TS → mk-go drop-in 切替
