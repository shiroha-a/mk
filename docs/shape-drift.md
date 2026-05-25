# Entity shape drift gate (Layer 0)

mk-goのentity DTO構造体を、Misskey API契約(misskey-jsの`types.ts`=OpenAPI `components.schemas`のミラー)とフィールド単位で突き合わせ、**3rd-partyクライアントのshape crashを引き起こすドリフト**を静的に検出するゲート。

サーバー/ブラウザ/Docker不要で、決定的・ミリ秒で動く。CIでは`go test ./...`内の`TestEntityShapeDrift`として自動実行される。

## なぜ必要か

Misskey互換クライアント(Miria等)は、misskey-jsの型に従ってレスポンスをデシリアライズする。`text: string`(non-null)と宣言された欄をmk-goが`null`で返したり、必須欄を省略すると、クライアントは非nullキャストに失敗してクラッシュする。

これは本質的に**スキーマ(shape)の不一致**であって、Playwrightやdrop-in E2Eのようなブラウザ越しの挙動テストで間接的に検出する問題ではない。本ゲートは契約レベルで直接diffを取るため、flakyゼロ・全フィールド網羅で検出できる。

## 構成

| ファイル | 役割 |
|---|---|
| `internal/entitycompat/shapecheck.go` | reflection / types.tsパーサ / diffロジック |
| `internal/entitycompat/mapping.go` | entity構造体 ↔ golden schemaのマッピング表 |
| `internal/entitycompat/gate.go` | snapshot/allowlistのロード、ゲート判定 |
| `internal/entitycompat/testdata/golden_schemas.json` | golden契約のスナップショット(commit対象) |
| `internal/entitycompat/testdata/allowlist.json` | 既知/意図的ドリフトのallowlist(baseline backlog) |
| `tools/shapediff/` | snapshot再生成 + 全family drift report |

golden側は**commit済みスナップショット**を読むため、テスト時にsubmoduleを必要としない(hermetic)。

## 検出するドリフトの種類

| Kind | Severity | 意味 |
|---|---|---|
| `missing` (required) | HIGH | golden必須欄をmk-goが全く出さない |
| `missing` (optional) | LOW | golden optional欄が無い(非ブロッキング) |
| `nullable` | HIGH | goldenがnon-nullなのにmk-goが`null`を出しうる(omitempty無しポインタ) |
| `omit` | MED | goldenが必須なのにmk-goが`omitempty`で省略しうる |
| `extra` | INFO | mk-go独自欄(拡張/alias候補) |
| `layer` | INFO | 同family内の別layerに存在(配置違いだが出力上は存在) |

ゲートは**HIGH/MED**のみをブロック対象とする。LOW/INFOはレポートのみ。

## マッピングの考え方

goldenはユーザー shapeを合成可能な部品(`UserLite` / `UserDetailedNotMeOnly` / `MeDetailedOnly`)に分解し、mk-goは構造体埋め込みでこれを写している。よって各埋め込みlayerを対応する`*Only`スキーマに突き合わせる。standaloneなentity(Note等)はfamily of one。

### 対象外

- **Notification**: golden側がtype別のdiscriminated union、mk-go側も`PackNotification`が`map[string]any`を手組みするため、reflectionが届かない。map-based packerとunion型は静的検出の射程外で、差分HTTP(L2)で見る。

## 運用

### 日常(CI)

`go test ./...`で`TestEntityShapeDrift`が走る。新しいドリフトが入ったPRはここで落ちる。ローカル確認は:

```bash
make shapecheck          # gateをCIと同じ判定で実行
make shapecheck-report   # 全familyのdrift一覧(severity付き)
```

### allowlist

ゲート導入時点で存在したドリフトはbaselineとして`allowlist.json`に記録済み。各エントリは**潰すべきbacklog**であって恒久免除ではない。新規ドリフトは:

1. 構造体を修正してドリフトを解消する、または
2. 理由を添えて`allowlist.json`に追加する(意図的拡張等)

allowlistに登録済みのドリフトを修正すると、そのエントリは**stale**になりゲートが落ちる(allowlistの掃除を促す)。

### upstream catch-up時

`third_party/misskey`を新バージョンにbumpしたら、goldenスナップショットを再生成してcommitする:

```bash
make shapecheck-gen   # testdata/golden_schemas.json を再生成
git add internal/entitycompat/testdata/golden_schemas.json
```

これで新バージョンで追加/変更された契約欄が次回のゲートに反映される。

## Layer 2: 実行時shape検証(map-based / union)

L0の静的reflectionは「宣言されたshape」しか見ない。以下はその射程外:

- `map[string]any`で手組みするpacker(`PackNotification`等)
- discriminated union(通知のtype別variant)
- 構造体は正しいが、handlerが実行時に値をpopulateし忘れるバグ

L2は**packerが実際に出力したJSON値**を契約に突き合わせてこれを埋める。二backend(TS)起動は重くflakyなので避け、**fixtureでpacker出力を生成 → golden契約に検証**する軽量版を採る。

| ファイル | 役割 |
|---|---|
| `internal/entitycompat/runtime.go` | union parser(`ParseUnion`)+ 実行時validator(`ValidateValue` / `ValidateUnionValue`) |
| `internal/entitycompat/runtime_test.go` | `Test*ShapeL2`(実packer出力を検証)+ 単体テスト |
| `testdata/golden_unions.json` | union契約のスナップショット(type literal別variant、commit対象) |
| `testdata/allowlist_l2.json` | L2 baseline backlog |

### 対象packer

L2は2種の検証関数を持つ:

- **`ValidateUnionValue`**: discriminated union(`Notification`)を`type`でvariant dispatchして検証。`UnionSchemaNames()`が対象。
- **`ValidateValue`**: flatなmap-based packer(`Announcement`等)を golden flat schema に対して検証。`L2FlatSchemaNames()`が対象。golden schemaは`golden_schemas.json`に同居(L0と同じflat形式)。

map-based packer(`map[string]any`を手組みするもの)はL0のreflectionが届かないため、L2で実出力を検証する。新しいmap-based packerを守りたいときは`L2FlatSchemaNames()`にgolden schema名を足し、fixtureで`Test*ShapeL2`を書く。

### ValidateUnionValue の判定

通知mapの`type`でgolden variantを引き、そのvariantに対して検証する:

- `type`に対応するvariantが無い → HIGH(strictクライアントがunion dispatchで弾く)
- variantの必須欄が実行時に欠落 / null → HIGH
- scalar型不一致 → MED
- golden未知の欄 → INFO(拡張)

### L2 baseline(実検出例)

L2導入時点で検出された実ドリフト:

- `type=pollVote` / `type=importCompleted`(HIGH): mk-goが出す通知typeがgolden union(`pollEnded` / `exportCompleted`)に無い。strictクライアントがdispatchできない。
- `noteId`(INFO): mk-goが通知に足す独自欄(契約外、非ブロッキング)。

### まだ残る射程外

- `/api/i`等がhandler層で`map[string]any`にアドホック追加する欄(再利用可能なpackerを介さないため、fixtureで再現できない)。L0 baseline allowlistの`MeDetailed`系(`unreadAnnouncements`等)がこれ。最終的には二backend差分HTTP、または該当handlerの統合テストで確定する。
