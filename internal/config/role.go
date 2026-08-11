package config

import (
	"fmt"
	"os"
	"strings"
)

// ProcessRole selects which halves of the process are started.
//
// upstream Misskey は `MK_ONLY_SERVER` / `MK_ONLY_QUEUE` (環境変数) で、その
// プロセスが Web を担うかジョブキューを担うかを切り替える
// (`packages/backend/src/env.ts` / `boot/master.ts`)。同じ config・同じ DB /
// Redis を向けたまま環境変数だけ変えて起動し、配送専用ノードを別サーバーに
// 立てる運用を可能にする。mk-go も同じ変数名で受ける (#2459)。
//
// **`redisForJobQueue` 等とは別物。** あちらは用途ごとの Redis 接続先で、
// 役割の指定ではない。Web ノードも `redisForJobQueue` を読んで enqueue する。
type ProcessRole string

const (
	// RoleBoth runs the HTTP server and the job queue worker in one process.
	// 既定。env を設定しない従来の運用はここに落ちる。
	RoleBoth ProcessRole = "both"
	// RoleServer runs only the HTTP server (ジョブを積むが処理しない)。
	RoleServer ProcessRole = "server"
	// RoleQueue runs only the job queue worker (API を生やさない)。
	RoleQueue ProcessRole = "queue"
)

// Environment variables selecting the role. upstream は camelCase の option 名を
// SNAKE_CASE へ機械変換して読むので (`'MK_' + key.replace(/[A-Z]/g, ...)`)、
// 名前だけが契約になる。ここでは literal で持つ。
const (
	EnvOnlyServer = "MK_ONLY_SERVER"
	EnvOnlyQueue  = "MK_ONLY_QUEUE"
)

// RunsServer reports whether this role serves HTTP API / frontend traffic.
func (r ProcessRole) RunsServer() bool { return r != RoleQueue }

// RunsQueue reports whether this role consumes jobs from the queue.
func (r ProcessRole) RunsQueue() bool { return r != RoleServer }

// ResolveProcessRole reads the role from the environment.
//
// # upstream との意図的な差分
//
// upstream は `if (process.env['MK_ONLY_QUEUE'])` の truthy 判定なので、
// **`MK_ONLY_QUEUE=false` と書いても有効になる**。無効化するには変数ごと
// 消すしかない。mk-go は値を解釈して `0` / `false` / `no` / `off` / 空を偽と
// して扱う。`=1` を使う既存構成は影響を受けず、`=false` と書いた運用者だけが
// 意図どおりに動く。
//
// 両方を真にした場合、upstream は `onlyServer` を優先して黙って続行するが、
// mk-go はエラーにする。矛盾した設定は運用ミスであって、起動してから
// 「配送が動かない」と気付く方が高くつく。
//
// どちらも docs/divergence.md に記載。
func ResolveProcessRole() (ProcessRole, error) {
	onlyServer, err := envFlag(EnvOnlyServer)
	if err != nil {
		return "", err
	}
	onlyQueue, err := envFlag(EnvOnlyQueue)
	if err != nil {
		return "", err
	}
	switch {
	case onlyServer && onlyQueue:
		return "", fmt.Errorf("%s and %s are mutually exclusive; set at most one", EnvOnlyServer, EnvOnlyQueue)
	case onlyServer:
		return RoleServer, nil
	case onlyQueue:
		return RoleQueue, nil
	default:
		return RoleBoth, nil
	}
}

// envFlag parses a boolean environment variable, treating unset and empty as
// false. 未知の値は**黙って偽にせずエラー**にする。`MK_ONLY_QUEUE=ture` の
// ようなタイポで配送ノードが Web ノードとして起動すると、気付くのは
// 「ジョブが溜まり続ける」ところになる。
func envFlag(name string) (bool, error) {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	default:
		return false, fmt.Errorf("%s: invalid boolean value %q", name, raw)
	}
}
