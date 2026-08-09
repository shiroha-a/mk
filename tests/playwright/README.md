# Playwright e2e (#744)

mk-go 向けの Playwright e2e。spec は upstream Misskey TS の API 互換挙動を
期待値として書き、mk-go backend に対して走らせて drop-in 互換 regression を
検出する。

## 使い方

```bash
# stack 起動 (postgres / redis / mkgo)
make playwright-up

# Playwright runner で spec 実行
make playwright-test

# stack 停止 (volume 削除)
make playwright-down
```

## 構成

```
[Playwright runner] → [mkgo] → [postgres / redis]
```

Phase 1 PR-1 では frontend は bundle せず API 中心 spec のみ。後続 PR で
`page.goto` ベースの spec を追加するときに frontend asset を組み込む。

## upstream / mkgo の分割

```
specs/
├── upstream/   # upstream Misskey にも存在する機能の検証
└── mkgo/       # mk-go 独自機能の検証 (現時点で空)
```

**現在の 269 spec はすべて `upstream/`。** 分割時に全 spec を確認したが、mk-go 独自
機能 (cherrypick 由来の chat 拡張、`mkGoVersion` 等の additive field) を検証するものは
1 件も無かった。むしろ `i/profile_extra.spec.ts` のように **mk-go 拡張を明示的に scope
外としている** spec もある。

この境界には 2 つの意味がある。

  - **`upstream/` の spec は Misskey TS backend に対しても通る**。`make playwright-ts-test`
    と nightly CI がそれを担保している。期待値そのものが upstream の実挙動と一致している
    ことの検証になる
  - **本家へ還元しやすい**。`upstream/` の spec は本家にも存在する機能を検証しているので、
    本家の Playwright スイートへ持っていける

mk-go 独自機能の spec を書くときは `mkgo/` に置く。そちらは TS backend では通らないので、
`upstream/` に混ぜると `playwright-ts-test` が壊れる。

## ライセンスヘッダ

全 spec と fixtures に SPDX ヘッダを付けている。

```
/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */
```

mk-go リポジトリ自体が AGPL-3.0 なのでファイル単位のヘッダは必須ではないが、**由来を
明示する**ために入れている。本家の SPDX チェック
(`.github/workflows/check-spdx-license-id.yml`) は `SPDX-FileCopyrightText: syuilo and
misskey-project` **または** `SPDX-License-Identifier: AGPL-3.0-only` の OR 条件なので、
この表記のままでも本家 CI は通る。

## ディレクトリ構造

```
tests/playwright/
├── playwright.config.ts        # multi-browser 設定 (現状 chromium のみ)
├── package.json                # @playwright/test 依存
├── Dockerfile.runner           # Playwright runner image
├── instance.yml                # mk-go config
├── specs/
│   ├── upstream/               # upstream にもある機能 (現在 269 spec)
│   └── mkgo/                   # mk-go 独自 (現時点で空)
├── fixtures/
│   ├── api.ts                  # POST /api/<endpoint> ラッパ
│   └── auth.ts                 # signup / signin helper
└── specs/
    └── smoke/                  # Phase 1 minimum
        └── signup.spec.ts      # signup → /api/i 確認
```

## Phase 計画 (#744 ref)

- **Phase 1 PR-1 (本 PR)**: 基盤 + smoke spec 1 本
- **Phase 1 PR-2-N**: 残り Phase 1 spec (notes / streaming / drive ほか)
- **Phase 1 final**: CI nightly workflow 統合
- **Phase 2-6**: timeline / users / chat / notification / ... を段階的に追加

## design 原則

- spec は backend-agnostic (= TS / mk-go 両方で同 spec が pass するべき)
- 失敗 = 非互換 / regression として issue 化する運用
- Cypress (\`tests/dropin_frontend/\`) / pytest (\`tests/dropin/\`) は並走で残す
