# コントリビューション

## ワークフロー

1. **Issueを作成** — すべての作業は対応するissueを先に作成してから着手する
2. **ブランチを切る** — `develop`から`feature/<phase>-<要約>`または`fix/<対象>-<要約>`
3. **実装 + テスト** — カバレッジ90%以上を維持
4. **PR作成** — `gh pr create`で作成し、`Closes #<issue番号>`を本文に記載

## Issue

- タイトル形式: `Phase〇 <内容>` (例: `Phase 10 管理機能`)
- 大きな機能はサブフェーズに分割 (例: `Phase 10-1`, `Phase 10-2`)
- 本文に含める項目: 背景・目的、実装詳細、影響範囲、完了条件

## ブランチ

| ブランチ | 役割 |
|---|---|
| `main` | リリース |
| `develop` | 開発統合 |
| `feature/<phase>-<要約>` | 機能追加 |
| `fix/<対象>-<要約>` | バグ修正 |

## Pull Request

- タイトル: `Phase〇 <内容>` または作業の要約
- 本文に含める項目:
  - **Summary**: 変更の概要と目的
  - **主な変更点**: 変更ファイルの要約
  - **テスト**: 追加したテスト、実行方法
  - **Closes**: `Closes #<issue番号>`

## コミット前チェック

```bash
make fmt    # gofmt -s -w .
make lint   # go vet ./...
make test   # go test ./... -v
```

CIで`gofmt`差分チェック、`go vet`、カバレッジ閾値チェックが走る。

PR を出すと十数個の check が走る。**required なのは `build` / `test` / `lint` の 3 つだけ**で、
残りは非ブロッキング。どれが何を見ていて落ちたとき何を疑うかは [CI で回る項目](ci.md) に
まとめてある。

## コーディング規約

- `gofmt -s`で整形
- Early returnでネストを浅く保つ
- エラーは`fmt.Errorf("context: %w", err)`でラップ
- GoDocは英語、インラインコメントは日本語

詳細はCLAUDE.md Section 5を参照。

## ライセンス

[GNU AGPL-3.0](../LICENSE)
