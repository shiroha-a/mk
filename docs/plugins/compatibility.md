# プラグイン — 互換性ポリシー

mk-go 本体を変更する人向け。**公開面を広げてよい条件**と、壊してよい範囲を定める。

## 公開面とは

| | 場所 |
|---|---|
| Go | `plugin/` と `plugin/plugintest/` |
| TypeScript | `third_party/misskey/packages/frontend/src/plugin-api.ts` |
| HTTP | `/api/plugin/<name>/` の名前空間 |
| ページ | `/plugin/<name>/` と `/admin/plugin/<name>/` の名前空間 |
| ナビ | `navbarItemDef` の `plugin:<name>` キー |
| DB | `plugin_<name>` schema と、そこへ渡す `*sql.DB` |
| 設定 | `.config/default.yml` の `plugins:` セクション |

`internal/` の中身は公開面ではない。**プラグインは Go の internal ルールにより import できない**ので、自由に変えてよい。

## 変更の分類

### 追加（マイナー）

新しい型・メソッド・スロットの追加。既存のプラグインは影響を受けない。

**`APIVersion` は上げない。**

### 破壊的変更（メジャー）

既存のシグネチャ変更、削除、意味の変更。

**`plugin.APIVersion` を上げる。** 合わないプラグインは `mk-plugin.yml` の `apiVersion` 検査でビルド時に落ちる。黙って動かない状態にはならない。

破壊的変更を入れるときは、

1. 非推奨期間を置く（可能なら新旧を並立させる）
2. `APIVersion` を上げる
3. `plugins/status/` を追従させる（サンプルが壊れたまま残らないように）

### 上流追従による破壊

`plugin-api.ts` が再公開している Misskey のコンポーネント（`MkInput` 等）は、upstream が props を変えると壊れる。

これは**受け入れている**。見た目の完全一致と引き換えのコストで、どのプラグイン機構でも追従は必要という判断。`frontend-check`（`vue-tsc`）で検出できる。

## 公開面を広げてよい条件

**実際のプラグインが要求したときだけ。**

想像で足さない。#2484 で実際にプラグインを 2 本書いたところ、想定していなかった不足が 7 件見つかり、逆に**想定して作った機能のうち使われなかったもの**もあった。

追加するときは以下を満たすこと。

- [ ] 具体的なプラグインの具体的な用途がある（「あると便利そう」では足さない）
- [ ] 内部の型を露出していない（`echo.Context` / `gorm.DB` / `model.User` などを渡さない）
- [ ] 代替手段が無い（既存の組み合わせで書けないか確認した）
- [ ] `plugins/status/` か新しいサンプルで実際に使われる、または doc に用例がある

### 露出してはいけないもの

| | 理由 |
|---|---|
| `echo.Context` | Echo は内部の選択。差し替えたときにプラグインが全滅する |
| `*gorm.DB` | 同上。標準の `*sql.DB` に留める |
| `model.*` | DB モデルが契約になり、migration が打てなくなる |
| ActivityPub 関連 | 不具合の症状が他人のサーバー側に出る。**後から塞げない** |
| repository / service | 可視性判定などのアプリケーション側のガードを迂回できる |
| ルーターの定義そのもの | プラグインが本体のパスを奪える。名前空間を切った登録だけを許す |

## drift gate

公開面は golden で固定してある。

```
internal/entitycompat/testdata/golden_plugin_surface.txt
```

export が増減すると `TestPluginSurfaceDrift` が落ちる。意図した変更なら再生成する。

```bash
go run ./tools/pluginspec -write
```

**golden の差分は必ずレビューで意図を確認すること。** これが「うっかり公開面が広がる」ことを防ぐ唯一の仕組み。

対象は `plugin/` と `plugin/plugintest/` の両方。テスト用だからと外すと、そこだけ黙って育つ。

## サンプルプラグイン

`plugins/status/` は**リポジトリに同梱**してある。

別リポジトリに置くと、`plugin/` を変えたときに壊れても CI で気付けない。同梱していれば公開面を壊した時点でビルドが落ちる。**サンプルの一番の価値は「常に動くこと」**。

`plugins/*` は gitignore されているが、`!plugins/status/` で例外指定してある。

## 変更時のチェック

- [ ] `go run ./tools/pluginspec -write` で golden を更新した（差分をレビューで説明できる）
- [ ] 破壊的変更なら `plugin.APIVersion` を上げた
- [ ] `plugins/status/` が通る（`cd plugins/status && go test ./...`）
- [ ] `plugin-api.ts` を変えたなら `make frontend-check` が通る
- [ ] `docs/plugins/authoring.md` の公開面一覧を更新した

## 参考

- 設計の経緯と却下した案: #2476
- 実プラグインで確定させた経緯: #2484
