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

## ディレクトリ構造

```
tests/playwright/
├── playwright.config.ts        # multi-browser 設定 (現状 chromium のみ)
├── package.json                # @playwright/test 依存
├── Dockerfile.runner           # Playwright runner image
├── instance.yml                # mk-go config
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
