package event

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"
)

// PubSubService provides Redis-backed pub/sub messaging.
//
// **トピックごとに Redis 購読は 1 本だけ持ち、購読者へはメモリ上で配る。**
// かつては購読ハンドルを「トピック名だけ」をキーにしたマップへ入れており、
// 2 本目の購読が 1 本目のハンドルを黙って上書きしていた。解除は「いま
// マップに入っているもの」を閉じるので、**先に購読した接続が解除すると、
// 後から購読した別の接続の配信が止まる**。閉じ損ねた側は Redis 接続と
// goroutine がプロセス終了まで残る。
//
// 同じトピック (ローカル/グローバルタイムライン、ハッシュタグ、チャンネル、
// ノートのリアクション購読) は複数の接続が同時に購読する普通の状態なので、
// 攻撃者が居なくても 2 人目がタブを開いて 1 人目が閉じた瞬間に起きた。
//
// upstream も Redis を 1 本だけ subscribe し、接続ごとの EventEmitter へ
// 配る形になっている (StreamingApiServerService)。
type PubSubService struct {
	client *redis.Client
	prefix string

	mu   sync.Mutex
	subs map[string]*topicSubscription
	// nextID はサービス全体で単調。**トピックごとに採番しない** — 購読の
	// 張り替えをまたいで同じ id が再利用されると、未呼び出しの解除関数が
	// 別人のハンドラを消す。
	nextID uint64
}

// topicSubscription holds the single Redis subscription for one topic and the
// set of handlers fed from it.
type topicSubscription struct {
	// sub は Redis への購読。**dial が終わるまで nil。**
	sub *redis.PubSub
	// closed は「dial の完了前に最後のハンドラが外れた」印。dial した側が
	// これを見て、埋める代わりに即座に閉じる。
	closed   bool
	handlers map[uint64]func([]byte)
}

// NewPubSubService creates a new PubSubService.
func NewPubSubService(client *redis.Client, prefix string) *PubSubService {
	return &PubSubService{
		client: client,
		prefix: prefix,
		subs:   make(map[string]*topicSubscription),
	}
}

func (p *PubSubService) channel(ch string) string {
	return p.prefix + ch
}

// Publish sends a message to a channel. The payload is JSON-encoded.
func (p *PubSubService) Publish(ctx context.Context, channel string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("pubsub marshal: %w", err)
	}
	return p.client.Publish(ctx, p.channel(channel), data).Err()
}

// Subscribe registers handler for the channel and returns the function that
// removes it again.
//
// **解除は返り値でしか行えない。** トピック名を渡して解除する API は、どの
// 購読者を外すのかを表現できず、他人の購読を閉じてしまう。戻り値は何度呼んで
// も安全で、最後の購読者が外れたときだけ Redis 購読を閉じる。
func (p *PubSubService) Subscribe(ctx context.Context, channel string, handler func([]byte)) func() {
	prefixed := p.channel(channel)

	p.mu.Lock()
	ts, ok := p.subs[prefixed]
	if !ok {
		ts = &topicSubscription{handlers: make(map[uint64]func([]byte))}
		p.subs[prefixed] = ts
	}
	id := p.nextID
	p.nextID++
	ts.handlers[id] = handler
	p.mu.Unlock()

	if !ok {
		// **dial はロックの外で行う。** `client.Subscribe` は新しい TCP 接続を
		// 張って AUTH / SUBSCRIBE を往復する同期処理で、`pump` は配信 1 件ごとに
		// 同じ `p.mu` を取る。ロック内で dial すると**新しいトピックの購読が
		// 1 つ発生するたび、その往復が終わるまでプロセス全体の配信が止まる**
		// (Redis が劣化すると dial timeout ぶん丸ごと止まる)。
		sub := p.client.Subscribe(ctx, prefixed)
		p.mu.Lock()
		if ts.closed {
			// dial 中に最後のハンドラが外れた。枠はもう map から消えている
			// ので、ここで閉じないと接続が残る。
			p.mu.Unlock()
			if err := sub.Close(); err != nil {
				slog.Warn("failed to close pubsub", "channel", prefixed, "error", err)
			}
		} else {
			ts.sub = sub
			p.mu.Unlock()
			go p.pump(channel, sub)
		}
	}

	var once sync.Once
	return func() {
		once.Do(func() { p.removeHandler(prefixed, id) })
	}
}

// pump fans one Redis subscription out to every handler registered for it.
//
// **`sub` を引数で受ける。** `ts.sub` を読むと、購読が張り替わったあとの
// 別の接続を読みうる。
func (p *PubSubService) pump(channel string, sub *redis.PubSub) {
	prefixed := p.channel(channel)
	for msg := range sub.Channel() {
		payload := []byte(msg.Payload)

		// **ハンドラの呼び出しはロックの外で行う。** ハンドラが Subscribe /
		// 解除を呼ぶ形があり、ロックを持ったまま呼ぶと自己デッドロックする。
		p.mu.Lock()
		var handlers []func([]byte)
		if cur, ok := p.subs[prefixed]; ok && cur.sub == sub {
			handlers = make([]func([]byte), 0, len(cur.handlers))
			for _, h := range cur.handlers {
				handlers = append(handlers, h)
			}
		}
		p.mu.Unlock()

		for _, h := range handlers {
			h(payload)
		}
	}
	slog.Debug("pubsub subscription closed", "channel", channel)
}

// removeHandler drops one handler and closes the Redis subscription once the
// last handler for that topic is gone.
func (p *PubSubService) removeHandler(prefixed string, id uint64) {
	p.mu.Lock()
	ts, ok := p.subs[prefixed]
	if !ok {
		p.mu.Unlock()
		return
	}
	delete(ts.handlers, id)
	if len(ts.handlers) > 0 {
		p.mu.Unlock()
		return
	}
	delete(p.subs, prefixed)
	// **印を残す。** dial がまだ終わっていなければ `sub` は nil で、ここでは
	// 閉じられない。印を見た dial 側が閉じる。
	ts.closed = true
	sub := ts.sub
	p.mu.Unlock()

	if sub == nil {
		return
	}
	if err := sub.Close(); err != nil {
		slog.Warn("failed to close pubsub", "channel", prefixed, "error", err)
	}
}

// SubscriberCount reports how many handlers are attached to a channel.
// Exposed for tests and diagnostics.
func (p *PubSubService) SubscriberCount(channel string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	ts, ok := p.subs[p.channel(channel)]
	if !ok {
		return 0
	}
	return len(ts.handlers)
}

// Close closes all active subscriptions.
func (p *PubSubService) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for key, ts := range p.subs {
		// dial がまだ終わっていない枠にも印を残す (`sub` は nil)。dial 側が
		// それを見て閉じる。
		ts.closed = true
		if ts.sub != nil {
			if err := ts.sub.Close(); err != nil {
				slog.Warn("failed to close pubsub", "channel", key, "error", err)
			}
		}
		delete(p.subs, key)
	}
	return nil
}
