/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

package plugin

import (
	"context"
	"encoding/json"
)

/*
 * 同じプラグインを入れている mk-go 同士だけで通信する経路。
 *
 * **ActivityPub には出さない。** AP に載せると、こちらの不具合の症状が
 * 他人のサーバー側に出るうえ、一度公開した形は後から塞げない (#2476 で
 * 「AP 関連は公開しない」と決めている)。ここは mk-go 専用の HTTP 経路に
 * 閉じるので、壊れても他実装には届かない。代わりに、相手も同じプラグインを
 * 持っていることが前提になる。
 *
 * 宛先・署名・ブロック・SSRF・大きさ・レート制限は mk-go 側で担保する。
 * プラグインが面倒を見るのは payload の中身だけ。
 */

// Peer is a private channel to the same plugin on other mk-go instances.
//
// 使うには [Definition.Peered] を立てる。立てると nodeinfo にプラグイン名が
// 出て、相手から「このインスタンスは同じプラグインを持っている」と分かる。
// 立てていないプラグインでこれを呼ぶとエラーになる。
type Peer interface {
	// Send queues a payload for the same plugin on host and returns the id
	// that identifies this exchange.
	//
	// **即座に返る。** 実際の送信は mk-go のキューが行うので、相手が遅くても
	// こちらのリクエストは詰まらない。応答は [Peer.OnReply] に届く。
	//
	// 相手が同じプラグインを持たない / ブロックしている / 宛先が不正な場合は
	// ここでエラーになる (キューに積まれない)。
	Send(ctx context.Context, host string, payload any) (id string, err error)

	// Handle registers the receiver for payloads from other instances.
	//
	// from は HTTP Signature で確定したホスト。**名乗りではない**ので、
	// 送信元としてそのまま信頼してよい。戻り値は相手の [Peer.OnReply] に
	// 渡る。nil を返せば応答なしとして扱われる。
	//
	// **中身は信用しない。** 相手は同じプラグインを持っているだけで、
	// 善良とは限らない。値域の検査はプラグインの責任。
	Handle(fn PeerHandler)

	// OnReply registers the receiver for replies to [Peer.Send].
	//
	// **届かないことがある。** 相手が落ちている / 応答しない / リトライの
	// 上限に達した場合は呼ばれない。mk-go は「いつか必ず届く」を約束しない
	// ので、取り直しはプラグイン側が期限を持って行うこと。
	OnReply(fn PeerReplyHandler)

	// Has reports whether host runs this plugin.
	//
	// nodeinfo 由来のキャッシュを見る。相手が宣言していなければ false。
	Has(ctx context.Context, host string) (bool, error)
}

// PeerHandler processes one payload from another instance and returns the
// reply. Returning an error makes mk-go respond with an error status; the
// sender's [Peer.OnReply] is not called.
type PeerHandler func(ctx context.Context, from string, payload json.RawMessage) (any, error)

// PeerReplyHandler processes the reply to a [Peer.Send]. id is what Send
// returned.
type PeerReplyHandler func(ctx context.Context, from, id string, reply json.RawMessage) error
