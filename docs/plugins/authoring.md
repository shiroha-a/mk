# プラグイン — 作者向け

## 最初に知っておくこと

**プラグインはサーバーと同じ権限で動く。** サンドボックスは無い。運営者はあなたを信頼して組み込む。

これは自由であると同時に責任でもある。外部への通信、ファイルの読み書き、実行時間、どれも mk-go は制限しない。

## 作る

```
plugins/myplugin/
├── mk-plugin.yml       これがあるものだけがプラグインとして扱われる
├── go.mod              **必須**
├── plugin.go
└── frontend/           UI が要る場合だけ
    └── index.ts
```

### `mk-plugin.yml`

```yaml
name: myplugin
apiVersion: 1
```

`apiVersion` はコンパイル前に検査される。合わないとビルドが止まり、理由が出る。

`disabled: true` を書くとビルドから除外される（詳細は[運営者向け](operating.md)）。この判定は `apiVersion` の検査より先に行われるので、ビルドが通らない状態のプラグインでも無効化はできる。

### `go.mod`

```
module github.com/you/mk-plugin-myplugin

go 1.26.5

require github.com/shiroha-a/mk v0.0.0
```

**独立した Go module である必要がある。** これは形式ではなく、Go の internal ルールにより「mk-go の内部パッケージを import できない」ことを保証する仕組み。`go.mod` を持たないディレクトリはビルド時にエラーになる。

### `plugin.go`

`Plugin` という名前のパッケージ変数を公開する。生成される登録コードがこれを参照する。

```go
package myplugin

import "github.com/shiroha-a/mk/plugin"

var Plugin = plugin.Definition{
	Name:       "myplugin",
	Version:    "1.0.0",
	APIVersion: plugin.APIVersion,
	Migrations: migrations,
	Routes:     routes,
	Jobs:       jobs,
}
```

名前は URL path / queue の task type / PostgreSQL schema の 3 箇所で使われるので、**小文字英数字とハイフン**のみ。

## 開発しながら動かす

```bash
make plugin-dev PLUGIN=plugins/myplugin
```

ソースを監視して 生成 → ビルド → 再起動 を繰り返す。frontend の HMR を使うには
Vite dev server も立てる。詳細は[開発環境](development.md)。

## 動く実例

[`plugins/status/`](../../plugins/status/) が公開面のほとんどを使う。外部サービスに依存しないので、組み込んで動かしながら読める。

以下は要点だけを抜き出したもの。

## ルート

```go
func routes(ctx plugin.Context, r plugin.Router) error {
	r.POST("/me", func(req plugin.Request) (any, error) {
		me := req.UserID()          // 未認証なら空文字
		if me == "" {
			return nil, plugin.Errorf(http.StatusUnauthorized, "ログインが必要です")
		}
		return map[string]any{"ok": true}, nil
	})
	return nil
}
```

`/api/plugin/<name>/` の下に生える。戻り値は JSON エンコードされ、`nil` なら 204。

**フロントから呼ぶものは POST にする。** `host.api`（= `misskeyApi`）は POST 固定で、Misskey 本体の API も POST 基本。`GET` は `<img>` から読ませる場合などに使う。

### エラー

```go
return nil, plugin.Errorf(http.StatusBadRequest, "%d 文字以内にしてください", max)
return nil, plugin.ErrNotFound("見つかりません")
```

**素の `error` を返すとメッセージは外に出ない**（ログに残り、利用者には汎用メッセージが返る）。プラグインの内部事情が利用者に見えるのを防ぐため。利用者が直せるものだけ `StatusError` で返す。

### バイナリ応答

```go
return plugin.Blob{
	ContentType:  "image/png",
	Body:         data,
	CacheControl: "public, max-age=86400",
}, nil
```

主な用途は画像のプロキシ。本体の CSP は `img-src 'self' data: blob:` なので、**外部の画像を `<img>` で直接読めない**。同一オリジンで配信すれば CSP を緩めずに済む。

mk-go は `X-Content-Type-Options: nosniff` を必ず付ける。**取得元の `Content-Type` をそのまま流さず、扱う型を決めて検証すること。** 読み込みの上限（`io.LimitReader` など）もプラグイン側の責務。

## ストレージ

自分専用の PostgreSQL schema が渡される。

```go
var migrations = []plugin.Migration{
	{Version: 1, SQL: `CREATE TABLE items (id serial PRIMARY KEY)`},
}
```

**`Definition.Migrations` で宣言する。** `Routes` の中で `Storage().Migrate()` を呼ぶと、ロールを分割したとき（`MK_ONLY_SERVER` / `MK_ONLY_QUEUE`）片方でしか走らず、queue 側がテーブルの無い schema でジョブを回すことになる。

```go
db := ctx.Storage().DB()   // *sql.DB (search_path は自分の schema に固定)
```

GORM を使いたければプラグイン側で包む（`postgres.New(postgres.Config{Conn: db})`）。mk-go が GORM を使っているのは内部の選択であって契約ではない。

### 守ること

- **mk-go 本体のテーブルに触れない。** ノートの可視性判定はアプリケーション側にあり DB には無いので、`SELECT * FROM note` は非公開ノートを含む。本体のデータは API 経由で取る
- **冪等に書く。** ストレージへの書き込みと API 呼び出しは同じトランザクションに入れられない
- 一度適用した version の SQL を書き換えても再実行されない。変更は新しい version で

## 本体の API を呼ぶ

```go
raw, err := ctx.API().Anonymous().Call(ctx, "users/show", map[string]any{"userId": id})
raw, err := ctx.API().AsUser(userID).Call(ctx, "notes/create", params)
```

mk-go の既存エンドポイントをプロセス内で呼ぶ。**可視性・権限・レート制限が自動的に効く**ので、モデレーション状態などを自前で持たなくてよい。

`AsUser` はその利用者として振る舞う（レート制限もその利用者のものが適用される）。「すべてを迂回する」経路は用意していない。管理操作が必要なら管理者の ID を渡す。

非 2xx は `*plugin.APIError` になる。本文が入っているので Misskey のエラーコードで分岐できる。

## ジョブ

```go
func jobs(ctx plugin.Context, j plugin.Jobs) error {
	j.Handle("prune", func(c context.Context, payload json.RawMessage) error {
		return nil
	})
	j.Schedule("0 * * * *", "prune", nil)
	return nil
}
```

task type は `plugin:<name>:<job>` として名前空間が付く。maintenance キューで動くので、遅い処理が連合の配送を止めることはない。

**任意のタイミングで enqueue する経路は無い。** プロセス内で完結する非同期処理は `ctx.Go()` を使う（recover 付き）。

> **プラグインが素の `go` を書くとプロセスごと落ちる。** Go は他 goroutine の panic を回収できず、mk-go 側の recover では止められない。

## 設定

```go
type config struct {
	MaxLength int `json:"maxLength"`
}

c := config{MaxLength: 30}          // 既定値を入れてから渡す
if err := ctx.Config().Unmarshal(&c); err != nil {
	return err
}
```

設定が書かれていなければ `v` は変更されないので、既定値がそのまま残る。

**キーの大文字小文字に注意。** 読み込みに使っている Viper はキーを小文字化する（`apiKey` → `apikey`）。構造体のフィールドは `encoding/json` が大文字小文字を無視して照合するので camelCase のタグで問題ないが、**map で受ける設定はキーの大小が復元されない**。

## フロントエンド

`frontend/index.ts` を置くと Vite のビルドに取り込まれる。

```ts
import { definePlugin } from '@/plugin-api.js';
import MyCard from './MyCard.vue';

export default definePlugin({
	name: 'myplugin',
	setup(host) {
		host.slot('profile:info', { component: MyCard });
	},
});
```

### スロット

| 名前 | 位置 |
|---|---|
| `profile:info` | ユーザーのプロフィール（`ctx.user` が渡る） |
| `settings:profile` | 設定 > プロフィール |

**位置は意味で定義されている。** upstream がコンポーネント名を変えても壊れない。

### 独自ページ・管理画面

**`setup` ではなく `definePlugin` で宣言する。**

```ts
export default definePlugin({
	name: 'myplugin',
	pages: [
		{ path: '/', component: TopPage, navTitle: 'マイプラグイン', navIcon: 'ti ti-puzzle' },
		{ path: '/', component: AdminPage, admin: true },
	],
	setup(host) { ... },
});
```

| | パス |
|---|---|
| 通常 | `/plugin/<name><path>` |
| `admin: true` | `/admin/plugin/<name><path>` |

**名前空間を切るのは意図的**で、本体のパスと混ざると upstream が同名のページを足したときに衝突する。

`navTitle` を指定すると**メニューに項目が出る**。

| | 出る場所 |
|---|---|
| 通常のページ | **「もっと」（ランチパッド）**。利用者が設定でサイドバーに常設できる |
| `admin: true` | コントロールパネルの左メニュー（「プラグイン」の節） |

**省くとルートは生えるがメニューには出ない。** 別の画面から遷移させる場合はそれでよいが、管理画面で省くと URL を直打ちするしかなくなる。

> **既定のサイドバーには入らない。** サイドバーの並びは利用者ごとの設定 (`prefer.r.menu`) で決まり、そこに列挙された項目だけが描画される。プラグインが勝手に常設されると、利用者の持ち物を運営者が占領することになるので、そうしていない。欲しい利用者は「ナビゲーションバーを編集」から追加できる。

> **なぜ宣言なのか。** ルーターは**モジュール読み込み時**に現在の URL を解決する。`setup` で登録すると、その時点では未登録なので**直接 URL を開いたときだけ 404 になる**（画面遷移では動くので気付きにくい）。宣言なら読み込み順に依存しない。

管理画面は `/admin` の配下に入るので**モデレーター以上にしか表示されない**。

> **画面を隠すのは UI の都合でしかない。** バックエンドは別途守ること。

```go
r.POST("/admin/stats", func(req plugin.Request) (any, error) {
	if !req.IsModerator() {
		return nil, plugin.Errorf(http.StatusForbidden, "権限がありません")
	}
	...
})
```

`Request.IsModerator()` / `IsAdministrator()` が使える。**未認証・判定不能なときは false** を返す（判定できないときに通すと、権限の穴が「動いているように見える」形で残る）。

### 2 つの形式

```ts
host.slot('profile:info', { component: MyCard });        // Vue コンポーネント
host.slot('profile:info', (el, ctx) => { /* ... */ });   // 素の DOM
```

Misskey のコンポーネント（`MkInput` など）を使うなら前者。ホストのアプリ内で描画されるので、テーマも provide/inject も本体と同じものが効く。

### 使えるもの

```ts
import { MkInput, MkButton, MkFolder, MkLoading } from '@/plugin-api.js';
```

**ここに出ているものだけが「壊さないと約束する範囲」。** プラグインは同じバンドルに入るので技術的には何でも import できるが、それ以外は upstream のリファクタで黙って壊れる。必要なものがあれば mk-go 側に要求すること。

### API 呼び出し

```ts
const res = await host.api<T>('plugin/myplugin/me', { ... });
```

**POST 固定。** 利用者のセッションがそのまま使われる。

## テスト

`plugin/plugintest` でルートとジョブを直接叩ける。

```go
h := plugintest.New(t).
	WithDB(db).
	WithConfig(map[string]any{"maxLength": 50}).
	WithAPI(&stubAPI{}).
	Routes(Plugin)

res, err := h.Call(t, "POST /me/set", plugintest.Request{UserID: "u1", Body: `{"text":"x"}`})
```

```go
jobs := plugintest.New(t).WithDB(db).Jobs(Plugin)
err := jobs.Run(t, "prune", "")
```

**DB はフェイクにしない。** SQL の挙動を模した偽物は本物とずれ、通ったのに本番で落ちるテストになる。`plugintest` も migration の適用は mk-go 本体と同じ実装を使っている。

## 公開面の一覧

以下が「壊さないと約束する範囲」のすべて。`internal/entitycompat/testdata/golden_plugin_surface.txt` から生成される（実装とずれない）。

### Go (`github.com/shiroha-a/mk/plugin`)

```
const APIVersion

type Definition struct
  Name       string
  Version    string
  APIVersion int
  Migrations []Migration
  Routes     func(Context, Router) error
  Jobs       func(Context, Jobs) error

type Context interface
  Name() string
  Logger() *slog.Logger
  API() API
  Storage() Storage
  Config() Config
  Go(func())

type Router interface
  GET(string, Handler)
  POST(string, Handler)

type Request interface
  Context() context.Context
  Bind(any) error
  Param(string) string
  Query(string) string
  UserID() string
  IsModerator() bool
  IsAdministrator() bool

type Storage interface
  DB() *sql.DB
  Migrate(context.Context, []Migration) error

type API interface
  Anonymous() Caller
  AsUser(string) Caller

type Caller interface
  Call(context.Context, string, any) (json.RawMessage, error)

type Jobs interface
  Handle(string, JobHandler)
  Schedule(string, string, any)

type Config interface
  Unmarshal(any) error

type Handler func(Request) (any, error)
type JobHandler func(context.Context, json.RawMessage) error
type Migration struct { Version int; SQL string }
type Blob struct { ContentType string; Body []byte; CacheControl string }
type StatusError struct { Status int; Message string }
type APIError struct { Endpoint string; Status int; Body json.RawMessage }

func Errorf(int, string, ...any) *StatusError
func ErrNotFound(string, ...any) *StatusError
func Register(Definition)
func Registered() []Definition
```

### TypeScript (`@/plugin-api.js`)

```
definePlugin(def)
host.name / host.me
definePlugin({ name, pages?, setup })
host.slot(name, renderer)
host.api<T>(endpoint, params)

PluginPage: { path, component, navTitle?, navIcon?, admin? }

型: SlotName / SlotUser / SlotContext / SlotMount / SlotComponent / SlotRenderer
    PluginPage / PageRegistration
再公開: MkInput / MkButton / MkFolder / MkLoading
```

## やってはいけないこと

| | 理由 |
|---|---|
| ActivityPub に関わるものを触る | 公開していない。不具合の症状が他人のサーバー側に出て、自分では気づけない |
| mk-go 本体のテーブルを読む | 可視性判定を迂回する。非公開ノートが混ざる |
| `plugin-api.ts` に無いコンポーネントを import する | upstream のリファクタで黙って壊れる |
| 取得元の `Content-Type` をそのまま `Blob` に流す | ブラウザの MIME 推測で意図しない解釈をされる |
| 素の `go` で goroutine を起動する | panic でプロセスごと落ちる。`ctx.Go()` を使う |
| 管理用の API を `IsModerator()` で守らない | 画面を隠しても API は誰でも叩ける |

## 公開と互換性

`plugin/` は semver に従う。破壊的変更では `APIVersion` が上がり、合わないプラグインは**ビルド時にエラーになる**（黙って動かない状態にはならない）。

詳細は[互換性ポリシー](compatibility.md)を参照。
