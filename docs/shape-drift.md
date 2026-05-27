# Entity shape drift gate (Layer 0 / 2 / 3)

mk-goのentity DTO構造体・packer出力・実HTTPレスポンスを、Misskey API契約(misskey-jsの`types.ts`=OpenAPI `components.schemas`のミラー)とフィールド単位で突き合わせ、**3rd-partyクライアントのshape crashを引き起こすドリフト**を検出するゲート。

サーバー/ブラウザ/Docker不要で、決定的・ミリ秒で動く。CIでは`go test ./...`の一部として自動実行される(`TestEntityShapeDrift` + `Test*ShapeL2` + 各handler testの`shapetest.Assert`)。

検出は3レイヤに分かれる。下に行くほど「実際に出る値」に近づき、上ほど網羅的:

| Layer | 何を見るか | どこで | 射程 |
|---|---|---|---|
| **L0** | 構造体の**宣言**(reflection) | `internal/entitycompat`の`TestEntityShapeDrift` | 全family網羅。ただし宣言しか見ない |
| **L2** | **packer出力**(fixtureでmap生成) | `Test*ShapeL2` | map-based packer / union / populate漏れ |
| **L3** | **実HTTPレスポンス**(handler test) | 各api packageの`shapetest.Assert` | handlerがアドホックに組む応答・実際の値 |

L0は宣言を全網羅、L2/L3は「実際に出た値」を見るので**宣言は正しいが実行時にnull/欠落するバグ**を捕まえる。L3が最も実態に近く、このセッションで30経路超へ拡大した(下記)。

## なぜ必要か

Misskey互換クライアント(Miria等)は、misskey-jsの型に従ってレスポンスをデシリアライズする。`text: string`(non-null)と宣言された欄をmk-goが`null`で返したり、必須欄を省略すると、クライアントは非nullキャストに失敗してクラッシュする。

これは本質的に**スキーマ(shape)の不一致**であって、Playwrightやdrop-in E2Eのようなブラウザ越しの挙動テストで間接的に検出する問題ではない。本ゲートは契約レベルで直接diffを取るため、flakyゼロ・全フィールド網羅で検出できる。

## 構成

| ファイル | 役割 |
|---|---|
| `internal/entitycompat/shapecheck.go` | reflection / types.tsパーサ(`FieldShape`/`Elem`)/ diffロジック |
| `internal/entitycompat/mapping.go` | entity構造体 ↔ golden schemaのマッピング表 |
| `internal/entitycompat/gate.go` | snapshot/allowlistのロード、ゲート判定 |
| `internal/entitycompat/runtime.go` | L2/L3 validator(`ValidateValue`/`ValidateUnionValue`)、`L2FlatSchemaNames()` |
| `internal/entitycompat/response.go` | embedded golden(`//go:embed`)+ `ValidateResponse`(L3入口) |
| `internal/entitycompat/shapetest/` | handler testから呼ぶ`shapetest.Assert`(L3)。`testing`importを持つためprod非依存 |
| `internal/entitycompat/testdata/golden_schemas.json` | golden契約のスナップショット(commit対象) |
| `internal/entitycompat/testdata/allowlist.json` / `allowlist_l2.json` | 既知/意図的ドリフトのallowlist(baseline backlog) |
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

- **Notification**: golden側がtype別のdiscriminated union、mk-go側も`PackNotification`が`map[string]any`を手組みするため、reflection(L0)は届かない。**L2**(fixtureでpacker出力を検証)と**L3**(`/api/i/notifications`の実HTTP応答を`ValidateResponse`のunion dispatchで検証)でカバーする。

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

- `/api/i`等がhandler層で`map[string]any`にアドホック追加する欄(再利用可能なpackerを介さないため、fixtureで再現できない)。これは下記**L3**で実レスポンスを直接検証して埋める。

## Layer 3: 実HTTPレスポンス検証(handler test)

L2のfixtureは「packerが正しく呼ばれれば」を見るが、handlerがpackerを介さず`map[string]any`を直接組む経路(`admin/*`の多く、`/api/meta`、chat/reversi等)や、handlerがrelationをattachし忘れる実行時バグはfixtureで再現しづらい。

L3は**handler unit testが実際に返したJSON**を golden に突き合わせる。各api packageの既存テストに1行足すだけ:

```go
import "github.com/shiroha-a/mk/internal/entitycompat/shapetest"

func TestCreate_Success(t *testing.T) {
    // ... handlerを叩いて rec.Body を得る ...
    var resp map[string]any
    require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
    shapetest.Assert(t, "Clip", resp) // golden "Clip" に対し HIGH/MED drift があれば t.Error
}
```

`shapetest.Assert(t, schemaName, actual)`は embedded golden(`//go:embed`)に対し`ValidateResponse`を回し、HIGH/MEDのfindingがあればテストを落とす。**packerは要らない**(`internal/entitycompat/shapetest`は`testing`importを持つがプロダクションには含まれない)。

### L3を足す手順

1. golden schema名を`L2FlatSchemaNames()`に追加(L2と共通の抽出リスト。L0 family済みの`UserLite`等は不要)。
2. `make shapecheck-gen`でsnapshot再生成。
3. 対象handlerのsuccess testで応答を`map[string]any`にdecodeし、`shapetest.Assert(t, "<Schema>", resp)`を足す。配列応答なら`resp[0]`を渡す。
4. テスト実行。drift が出たら**修正の前に現Misskey実装を確認**(下記lineage / verify-before-fix)。

### 配列応答・ネスト

- 配列を返すendpoint(`mute/list`等)は`rows[0]`をassert。
- ネストした子objectを検証したいとき(`/api/meta`の`counts`等)はその子だけ渡す: `shapetest.Assert(t, "QueueCount", resp["counts"].(map[string]any))`。
- 合成型(`MetaDetailed = MetaLite & MetaDetailedOnly`)はparserがflat抽出できないので、**構成要素を個別にassert**する(同じ応答に`MetaLite`と`MetaDetailedOnly`の2回)。

### discriminated union(`Notification`)

`shapetest.Assert(t, "Notification", resp[0])`は**flatでもunionでも同じ呼び方**で通る。`ValidateResponse`が **flat golden → union(`golden_unions.json`)→ not-found** の順でdispatchし、unionなら値の`type` discriminatorでvariantを引いて`ValidateUnionValue`に回す(未知typeはHIGH、variant必須欄の欠落/nullもHIGH)。

L2のfixtureは「packerが正しく呼ばれれば」を見るのに対し、L3は`/api/i/notifications`の**実HTTP応答**を直接unionに突合する。`type=follow`等のvariantはhandlerが`user`(reaction系は`note`も)を解決して埋める必要があるので、`SetRepos`をwireして対象userを seed したtestで配線する。

## array要素型の検出(`Elem`)

`string[]`と`{id,name}[]`はどちらもcoarseには`"array"`で、要素型を見ないと取り違えを見逃す(例: `EmojiDetailedAdmin.roleIdsThatCanBeUsedThisEmojiAsReaction`)。

parserは`FieldShape.Elem`に要素型(`string`/`number`/`boolean`/`object`/`array`/`other`)を記録し、`ValidateValue`は**非空配列の先頭要素**型をElemと突き合わせる(空配列は判定不能でskip、`json.RawMessage`はdecodeしてから判定)。多行`{...}[]`も正しく`array`+`elem:object`として抽出する。

## lineage判定: vanilla か cherrypick 派生か

goldenは**vanilla Misskey**のmisskey-jsから生成される。一方mk-goの一部endpointは**yojo-art/cherrypick**由来で、vanillaと契約が異なる:

- **`/api/chat/*`**: cherrypick federated chat由来。ただし`ChatRoom`/`ChatMessage`はvanillaとshapeが一致したのでgate済み。
- **`/api/reversi/*`**: yojo-art/cherrypick + **連合対戦拡張**。`crc32`等のvanilla goldenに無い独自field・federation関連で乖離が大きく、**vanilla golden gateの対象外**。

**新しいendpointをgateする前に、それがvanilla由来か派生由来かを確認する。** 派生由来でvanilla goldenと乖離するものは、無理にgateすると「lineage差」を「drift」と誤認する。

### verify-before-fix(driftを見つけたら直す前に確認)

L0/L2/L3がdriftを出しても、**修正の前に現misskey-ts実装を確認する**。golden(契約)とMisskeyの実packer出力がズレているケースがあるため:

- **vestigial field**: 契約には残るが機能削除済みのfield(例: `antenna.notify`はカラム削除済でpackerが定数`false`を返す)。goldenにあってもmk-goで実装し直すのは誤り。
- **endpoint取り違え**: goldenの同名schemaが別endpointの契約のことがある(例: `EmojiDetailed`は`admin/emoji/list`、`EmojiDetailedAdmin`は`v2/admin/emoji/list`)。
- 確認先: `third_party/misskey/.../core/entities/*EntityService.ts`(packer)、endpoint定義の`res`スキーマ、migration。

## gateの盲点と補い方

- **`Record<string, never>`等は`"other"`型**になり、`ValidateValue`の型検査をskipする(`QueueJob.progress`/`data`等)。ただし**required欄の欠落**と**non-null欄のnull**は型に関係なく検出されるので、`failedReason`欠落・`returnValue` null は捕まえられる。`progress`がnumberかobjectかのような「otherの中身」はtest側で明示assertして補う。
- **lite/detail等のfield partition**は集合演算で体系的に差分を取る: goldenの該当schema(`MetaLite`/`MetaDetailedOnly`)のtop-level fieldをparserで抽出し、handlerの応答キーと突き合わせて「必須なのに欠落」「detail専用なのにleak」を算出する(`/api/meta`の`#1306`で使用)。

## 既知の修正済みドリフト(本ゲートで検出)

L3拡大の過程で検出・修正した実ドリフトの代表例(いずれも現misskey-ts確認の上で対応):

| 対象 | ドリフト | 種別 |
|---|---|---|
| `pages` (PackPage) | `content`が空時null(golden `PageBlock[]`非null) | null-array |
| `channels` | `createdAt`/`bannerUrl`欠落、`pinnedNoteIds`がnull | 欠落 + null-array |
| `chat/rooms/create` | `owner`(UserLite)未attach | embed漏れ |
| `chat/messages/create` | `fromUser`未attach | embed漏れ |
| `admin/abuse-report/notification-recipient` | `updatedAt`列欠落、`userId`/`systemWebhookId`がnull | schema gap + null |
| `v2/admin/emoji/list` | `roleIds`が`string[]`(golden `{id,name}[]`) | array要素型 |
| `admin/queue/show-job` | `progress`/`returnValue`/`failedReason`/`data` | 型 + null + 欠落 |
| `/api/meta` (lite) | MetaLite必須5欄をomit、MetaDetailedOnly 3欄をleak | partition |

## Error-id drift gate（error.id の per-endpoint 整合）

shape gateがレスポンス**ボディ**の契約を守るのに対し、これは**エラーレスポンスのid**を守る別ゲート(`TestErrorIDDrift`)。

Misskeyのエラーは`{code, message, id}`形式で、クライアントは`code`だけでなく**endpoint固有のUUID `id`**でエラーを識別する。mk-goは「1 code = 1 UUID使い回し」になりがちだが、Misskeyは**endpointごとに別id**を割り当てる(例: `NO_SUCH_WEBHOOK`はshow/update/delete/testで4つ別id)。idがズレると`code`が正しくてもdrop-inクライアントがエラーを誤分類する。

### 仕組み

完全に静的(サーバ起動不要)。`internal/api/**/*.go`を走査し、各handler **method**が返すエラーの`(code, id)`を4経路で抽出する:

- **inline literal**: `apierr.Error("CODE", msg, "uuid")`
- **UUID定数参照**: `apierr.Error("CODE", msg, apierr.UUIDxxx)`(`errors.go`の定数表で解決)
- **helper呼び出し**: `apierr.NoSuchUser()`等(`errors.go`のhelper表で`(code, uuid)`に解決)
- **echo wrapper**: `apierr.JSONNoSuchNote(c)`等(`echo.go`の`JSONXxx`→内部helper→`(code, uuid)`に解決)

echo wrapperはhandlerの最頻送出経路なので外すとgateが大半のrouteを素通しする(実例: これを足すと解決数が577→652に増え、未検出だったdrift 25件が露出した)。

`router.go`の`path→handler`登録(import alias→pkg、`xxx := pkg.NewHandler()`のvar→pkg、ルート登録)を解決して各methodをendpointへ対応づけ、`(endpoint, code)`をgoldenの値と突合する。regexベースの抽出が空振りした場合の **silent-zero** を防ぐため、解決できたemissionが下限(400)を下回ったらgateを失敗させる。

### golden

`tools/erroriddiff`がMisskeyの`endpoints/*.ts`の`meta.errors`から`endpoint → {code: id}`を抽出し、`internal/entitycompat/testdata/golden_error_ids.json`へ生成・embedする(third_party非依存でCI実行可)。

### 除外（lineage / upstream typo）

- **reversi/* ・ chat/***: cherrypick派生。idもvanillaへ寄せない(`errorIDExcludedPrefixes`)。
- **不正UUIDのgolden値はskip**: upstreamのtypoでidが壊れている箇所(`sw/update-registration`は先頭スペース、`i/2fa/update-key`等は非hex文字を含む)は揃える対象が無いため、`validUUID`で弾く(自己文書化された除外)。
- **mk-go独自code・route未解決**: 対応するMisskey契約が無いので対象外(driftではない)。

確実に解決できたケースのみgateする方針なので、誤検出より見落としに倒している。

### 運用

```bash
make errorid-check    # gate をローカル実行 (TestErrorIDDrift)
make shapecheck-gen   # shape golden と合わせて golden_error_ids.json も再生成
```

upstream bump時は`make shapecheck-gen`で両goldenを再生成してcommit。新しいerror idを返すendpointを足すときは、verify-before-fixに従い対応するMisskey endpointの`meta.errors`のidを確認してから実装する。
