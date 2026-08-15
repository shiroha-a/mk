// Package plugin is the public API for mk-go server plugins.
//
// A plugin is an ordinary Go module that mk-go compiles into its binary. It
// declares a [Definition] and receives callbacks to register HTTP routes and
// background jobs.
//
// # 触れる範囲について
//
// **このパッケージはセキュリティ境界ではない。** プラグインは mk-go と同一
// プロセスで動く Go コードなので、`os` / `net/http` / `os/exec` を普通に使え、
// 設定ファイルを読むこともできる。ここで提供しているのは「mk-go 側が壊さない
// と約束する範囲」であって、プラグインができることの上限ではない。
//
// 運営者は信頼できる作者のプラグインのみを組み込むこと。
//
// # 公開範囲を最小に保つ方針
//
// 本パッケージは公開した時点から semver の義務を負う。実際のプラグインが要求
// するまで機能を追加しない。追加は tools/pluginspec の golden 差分としてレビュー
// に現れる。
package plugin

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// APIVersion is the plugin API contract version mk-go implements.
//
// プラグインは自身が想定する版を [Definition.APIVersion] に書く。一致しない
// プラグインは**読み込まずにエラーにする** (黙って動かして不定の壊れ方を
// させない)。破壊的変更のときだけ上げる。
const APIVersion = 1

// Definition describes a plugin. A plugin module exposes it as a package-level
// variable named Plugin, which the build-time generator references.
type Definition struct {
	// Name identifies the plugin. It also namespaces the plugin's HTTP routes
	// (/api/plugin/<name>/...), jobs, and storage.
	Name string

	// Version is the plugin's own version, surfaced in diagnostics.
	Version string

	// APIVersion is the plugin API version this plugin was written against.
	// Must equal [APIVersion].
	APIVersion int

	// Migrations are applied to the plugin's schema before Routes and Jobs
	// run. May be nil.
	//
	// **ここで宣言する。** Routes / Jobs の中で [Storage.Migrate] を呼ぶと、
	// ロール分割 (#2459) したときに片方でしか走らない。Routes 側だけに書くと
	// queue 専用プロセスがテーブルの無い schema でジョブを回すことになる。
	// 宣言にしておけば mk-go がどちらのロールでも必ず適用する。
	Migrations []Migration

	// Routes registers HTTP endpoints. Called only in processes that serve
	// HTTP. May be nil.
	//
	// ロールでプロセスを分割できる (#2459) ため、ルートとジョブは別々の
	// callback に分ける。1 つにまとめると queue 専用プロセスがルートを登録
	// しようとする (あるいはその逆) ことになる。
	Routes func(Context, Router) error

	// Jobs registers background work. Called only in processes that run the
	// job queue. May be nil.
	Jobs func(Context, Jobs) error

	// Peered opts this plugin into [Peer] — the private channel to the same
	// plugin on other mk-go instances.
	//
	// **宣言したものだけが nodeinfo に出る。** 入れているプラグインを全部
	// 晒すと、運営者がどんな拡張を使っているかが攻撃面の情報になる。
	// 連合しないプラグインは名前も出さない。
	Peered bool
}

// Validate reports whether the definition is usable.
func (d Definition) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("plugin: Name が空です")
	}
	if !validName(d.Name) {
		return fmt.Errorf("plugin: Name %q が不正です (使えるのは小文字英数字とハイフンのみ)", d.Name)
	}
	if d.APIVersion != APIVersion {
		return fmt.Errorf("plugin %q: APIVersion %d はこの mk-go (APIVersion %d) と互換がありません",
			d.Name, d.APIVersion, APIVersion)
	}
	if d.Routes == nil && d.Jobs == nil {
		return fmt.Errorf("plugin %q: Routes も Jobs も設定されていません", d.Name)
	}
	return nil
}

// validName restricts names to what is safe in a URL path segment, a queue task
// type, and a PostgreSQL schema name at once. 3 箇所で使う識別子なので、
// どれか 1 つでも通らない文字を最初から許さない。
func validName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(s)-1:
		default:
			return false
		}
	}
	return true
}

var (
	registryMu sync.Mutex
	registry   = map[string]Definition{}
)

// Register adds a plugin to the global registry.
//
// ビルド時に生成されるコードから呼ばれる。同名の重複は**エラーにせず panic**
// する: 生成コードの不具合であって運用時の状態ではないため、起動を許すと
// どちらが有効か分からないまま動いてしまう。
func Register(d Definition) {
	if err := d.Validate(); err != nil {
		panic(err)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[d.Name]; dup {
		panic(fmt.Errorf("plugin: %q が二重に登録されました", d.Name))
	}
	registry[d.Name] = d
}

// Registered returns the registered plugins sorted by name.
//
// 順序を固定するのは、ルート登録順やログの並びがビルドごとに変わらないように
// するため。
func Registered() []Definition {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]Definition, 0, len(registry))
	for _, d := range registry {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Context gives a plugin access to mk-go facilities during registration and at
// request time.
type Context interface {
	// Name returns the plugin's name.
	Name() string

	// Logger returns a logger tagged with the plugin name, so plugin output is
	// attributable in the instance's logs.
	Logger() *slog.Logger

	// API calls mk-go's own REST endpoints in-process.
	API() API

	// Storage returns this plugin's own PostgreSQL schema.
	Storage() Storage

	// Config returns this plugin's settings from the instance configuration.
	Config() Config

	// Peer returns the channel to the same plugin on other mk-go instances.
	//
	// [Definition.Peered] を立てていない場合、呼び出しはエラーを返す
	// (nil は返さないので、nil チェックは要らない)。
	Peer() Peer

	// Go runs fn in a new goroutine, recovering panics.
	//
	// **プラグインが自分で `go` を書くとプロセスごと落ちる。** Go は他の
	// goroutine の panic を回収できないため、mk-go 側の recover
	// (echo の Recover middleware 等) では止められない。
	Go(fn func())
}
