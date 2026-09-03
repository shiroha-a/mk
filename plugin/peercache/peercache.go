/*
 * SPDX-FileCopyrightText: syuilo and misskey-project
 * SPDX-License-Identifier: AGPL-3.0-only
 */

// Package peercache caches data fetched from other instances so a plugin can
// answer a view-time read without waiting for the network.
//
// **peer の応答は同期の描画には間に合わない (#2820)。** [plugin.Peer.Send] は
// 積んでから最大 4 回・数分かけて [plugin.Peer.OnReply] に返るので、プロフィール
// を開いた瞬間に答えは出ない。「初回は空で返し、届いた分を次から出す」という型が
// 要る。
//
// **空振りも覚える。** 覚えないと、そのプラグインを使っていない利用者の
// プロフィールを開くたびに相手へ問い合わせることになる。
//
// キャッシュはプラグイン自身の schema に置く ([plugin.Storage] の DB)。本体には
// 持たせない — プラグインを消したらデータも一緒に消えるべきなので。
package peercache

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shiroha-a/mk/plugin"
)

// Default TTLs used when Options leaves them zero.
//
// 肯定側を短くしすぎると相手に負荷をかけ、長くしすぎると古い情報が残る。
// 否定側はそれより短くする — 相手が後から使い始めたときに気付けなくなるため。
const (
	DefaultTTL         = 30 * time.Minute
	DefaultNegativeTTL = 10 * time.Minute
)

// askInterval bounds how often the same key is asked for while no reply has
// arrived yet.
//
// **同じ相手に群がらない。** 否定 TTL は応答が届いてから効くので、届く前に
// 大勢が同じプロフィールを開くと問い合わせが重なる。
const askInterval = time.Minute

// Options configures a Cache.
type Options struct {
	// Context is the plugin's own context. Peer / Logger / Go を使う。
	Context plugin.Context
	// DB is the plugin's schema.
	DB *sql.DB
	// Request builds the peer payload for one key. Required.
	Request func(key string) any
	// TTL is how long a positive answer is reused. 0 は DefaultTTL。
	TTL time.Duration
	// NegativeTTL is how long "the other side has nothing" is remembered.
	// 0 は DefaultNegativeTTL。
	NegativeTTL time.Duration
}

// Cache is a view-time cache of data fetched from other instances.
type Cache struct {
	ctx         plugin.Context
	db          *sql.DB
	request     func(key string) any
	ttl         time.Duration
	negativeTTL time.Duration
}

// New returns a Cache backed by the plugin's own schema.
func New(o Options) (*Cache, error) {
	if o.Context == nil {
		return nil, fmt.Errorf("peercache: Context が nil です")
	}
	if o.DB == nil {
		return nil, fmt.Errorf("peercache: DB が nil です")
	}
	if o.Request == nil {
		return nil, fmt.Errorf("peercache: Request が nil です")
	}
	c := &Cache{
		ctx:         o.Context,
		db:          o.DB,
		request:     o.Request,
		ttl:         o.TTL,
		negativeTTL: o.NegativeTTL,
	}
	if c.ttl <= 0 {
		c.ttl = DefaultTTL
	}
	if c.negativeTTL <= 0 {
		c.negativeTTL = DefaultNegativeTTL
	}
	return c, nil
}

// Migrations returns the schema this cache needs, numbered from `from`.
//
// **番号はプラグインが決める。** プラグイン自身の migration と衝突しないよう、
// 空いている番号を渡すこと。テーブル名 (`peer_cache` / `peer_cache_pending`) は
// このパッケージの予約。
func Migrations(from int) []plugin.Migration {
	return []plugin.Migration{{
		Version: from,
		SQL: `
			CREATE TABLE peer_cache (
				host       text NOT NULL,
				key        text NOT NULL,
				payload    jsonb NOT NULL,
				fetched_at timestamptz NOT NULL DEFAULT now(),
				expires_at timestamptz NOT NULL,
				PRIMARY KEY (host, key)
			);
			CREATE TABLE peer_cache_pending (
				id         text PRIMARY KEY,
				host       text NOT NULL,
				key        text NOT NULL,
				created_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX peer_cache_pending_target ON peer_cache_pending (host, key, created_at DESC);
		`,
	}}
}

// Lookup returns the cached payload for host/key.
//
// **無ければ nil を返し、取り寄せは裏で走らせる。** 描画を待たせないため。
// 期限切れでも**古いものは返す** — 空で出すより古い方が読み手には有用で、
// 取り直しは次の表示に間に合う。
//
// 相手がそのプラグインを持っていない場合は、静かに何もしない (普通のこと)。
func (c *Cache) Lookup(ctx context.Context, host, key string) (json.RawMessage, error) {
	payload, stale, err := c.read(ctx, host, key)
	if err != nil {
		return nil, err
	}
	if stale {
		c.askAsync(host, key)
	}
	return payload, nil
}

// Store records the reply for one exchange.
//
// sendID は [plugin.Peer.OnReply] が受け取る id。found が false なら「相手は
// 持っていない」として否定 TTL で覚える。
//
// **知らない id は静かに捨てる。** 期限切れの pending を掃除した後に応答が
// 届くことがあり、それはエラーではない。
func (c *Cache) Store(ctx context.Context, sendID string, payload any, found bool) error {
	host, key, err := c.takePending(ctx, sendID)
	if err != nil {
		return err
	}
	if host == "" {
		return nil
	}

	ttl := c.ttl
	body := json.RawMessage(`null`)
	if found {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("peercache: payload を JSON 化できません: %w", err)
		}
		body = encoded
	}
	if !found || len(body) == 0 || string(body) == "null" {
		ttl = c.negativeTTL
		body = json.RawMessage(`null`)
	}

	_, err = c.db.ExecContext(ctx, `
		INSERT INTO peer_cache (host, key, payload, fetched_at, expires_at)
		VALUES ($1, $2, $3, now(), now() + make_interval(secs => $4))
		ON CONFLICT (host, key) DO UPDATE SET
			payload = EXCLUDED.payload,
			fetched_at = EXCLUDED.fetched_at,
			expires_at = EXCLUDED.expires_at
	`, host, key, []byte(body), int(ttl.Seconds()))
	if err != nil {
		return fmt.Errorf("peercache: 保存できません: %w", err)
	}
	return nil
}

// Sweep drops rows nobody will read again. Call it from a cron job.
func (c *Cache) Sweep(ctx context.Context) error {
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM peer_cache WHERE fetched_at < now() - interval '7 days'`); err != nil {
		return fmt.Errorf("peercache: 掃除できません: %w", err)
	}
	// 応答が来なかった pending。**残すと問い合わせの抑止が効き続ける**ので、
	// askInterval よりずっと長い所で切る。
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM peer_cache_pending WHERE created_at < now() - interval '1 day'`); err != nil {
		return fmt.Errorf("peercache: 掃除できません: %w", err)
	}
	return nil
}

// read returns the cached payload and whether it should be refetched.
func (c *Cache) read(ctx context.Context, host, key string) (json.RawMessage, bool, error) {
	var payload []byte
	var expired bool
	err := c.db.QueryRowContext(ctx, `
		SELECT payload, expires_at <= now() FROM peer_cache WHERE host = $1 AND key = $2
	`, host, key).Scan(&payload, &expired)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("peercache: 読めません: %w", err)
	}
	if string(payload) == "null" {
		// 否定を覚えている。期限内なら問い合わせ直さない。
		return nil, expired, nil
	}
	return payload, expired, nil
}

// askAsync sends the request without blocking the caller.
//
// **描画の途中で待たない。** Peer.Has は nodeinfo をキャッシュしていないと
// 最大 10 秒待つので、view-time の読み取りから同期で呼ぶと画面が固まる。
func (c *Cache) askAsync(host, key string) {
	c.ctx.Go(func() {
		// 呼び出し元の ctx はハンドラが返ると切れるので引き継がない。
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c.ask(ctx, host, key)
	})
}

func (c *Cache) ask(ctx context.Context, host, key string) {
	fresh, err := c.hasFreshPending(ctx, host, key)
	if err != nil {
		c.ctx.Logger().Warn("peercache: 問い合わせ中の記録を読めません", "host", host, "err", err)
		return
	}
	if fresh {
		// 誰かが既に問い合わせている。応答は 1 つで足りる。
		return
	}

	peer := c.ctx.Peer()
	ok, err := peer.Has(ctx, host)
	if err != nil || !ok {
		// 相手が同じプラグインを持っていない。**普通のこと**なので黙って諦める。
		return
	}
	id, err := peer.Send(ctx, host, c.request(key))
	if err != nil {
		c.ctx.Logger().Debug("peercache: 問い合わせを出せませんでした", "host", host, "err", err)
		return
	}
	if _, err := c.db.ExecContext(ctx, `
		INSERT INTO peer_cache_pending (id, host, key, created_at) VALUES ($1, $2, $3, now())
		ON CONFLICT (id) DO NOTHING
	`, id, host, key); err != nil {
		c.ctx.Logger().Warn("peercache: 問い合わせの記録に失敗しました", "id", id, "err", err)
	}
}

func (c *Cache) hasFreshPending(ctx context.Context, host, key string) (bool, error) {
	var exists bool
	err := c.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM peer_cache_pending
			WHERE host = $1 AND key = $2 AND created_at > now() - make_interval(secs => $3)
		)
	`, host, key, int(askInterval.Seconds())).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// takePending consumes the pending row for sendID.
func (c *Cache) takePending(ctx context.Context, sendID string) (host, key string, err error) {
	// **一度で消す。** 同じ交換の応答が複数回届くことがある (送信はキューに
	// 載るので、worker が途中で落ちれば積み直される) ので、2 回目以降は
	// 知らない id として静かに捨てる。
	err = c.db.QueryRowContext(ctx,
		`DELETE FROM peer_cache_pending WHERE id = $1 RETURNING host, key`, sendID).Scan(&host, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("peercache: 問い合わせの記録を読めません: %w", err)
	}
	return host, key, nil
}
