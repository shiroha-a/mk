package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

/*
 * 相手が同じプラグインを持っているかを nodeinfo から見る (#2537)。
 *
 * 送る前に確かめるのは、**持っていない相手に接続しないため**。送ってから
 * 404 で知る形にすると、無関係なインスタンスにこちらの都合でリクエストが飛ぶ。
 */

// peerLookupTTL is how long a nodeinfo answer is reused.
//
// プラグインの構成は滅多に変わらないので長めでよい。**プロセス内に持つ**
// (DB に列を足さない) ので、再起動すれば取り直す。
const peerLookupTTL = 6 * time.Hour

// peerLookupNegativeTTL is how long "does not have it" is remembered.
//
// 引けなかった相手に毎回問い合わせると、こちらの都合で相手に負荷をかける。
// ただし導入直後に長く覚えると入れたのに繋がらないので、肯定側より短くする。
const peerLookupNegativeTTL = 30 * time.Minute

// peerLookupTimeout bounds one nodeinfo fetch.
const peerLookupTimeout = 10 * time.Second

// peerLookupMaxBody bounds a nodeinfo document.
const peerLookupMaxBody = 256 << 10

// nodeInfoPeerLister resolves which plugins a remote host declares.
type nodeInfoPeerLister struct {
	client *http.Client
	// urlFor builds the nodeinfo URL for a host. テストから http の
	// httptest サーバーへ向けられるように関数にしてある。
	urlFor func(host string) string

	mu    sync.Mutex
	cache map[string]peerLookupEntry
}

type peerLookupEntry struct {
	plugins   []string
	expiresAt time.Time
}

func newNodeInfoPeerLister(client *http.Client) *nodeInfoPeerLister {
	return &nodeInfoPeerLister{
		client: client,
		// well-known を辿らず 2.1 を直接見る。mk-go 同士でしか使わない経路
		// なので相手の nodeinfo の場所は分かっている。**相手が mk-go でなければ
		// mkGoPlugins が無いだけ**で、誤って送ることはない。
		urlFor: func(host string) string { return "https://" + host + "/nodeinfo/2.1" },
		cache:  map[string]peerLookupEntry{},
	}
}

// Plugins returns the plugin names host declares in nodeinfo metadata.
func (l *nodeInfoPeerLister) Plugins(ctx context.Context, host string) ([]string, error) {
	l.mu.Lock()
	if e, ok := l.cache[host]; ok && time.Now().Before(e.expiresAt) {
		l.mu.Unlock()
		return e.plugins, nil
	}
	l.mu.Unlock()

	names, err := l.fetch(ctx, host)
	if err != nil {
		// **失敗は覚えない。** 一時的な不調で「持っていない」と決めつけると、
		// 相手が復帰しても TTL の間ずっと送れなくなる。
		return nil, err
	}

	ttl := peerLookupTTL
	if len(names) == 0 {
		ttl = peerLookupNegativeTTL
	}
	l.mu.Lock()
	l.cache[host] = peerLookupEntry{plugins: names, expiresAt: time.Now().Add(ttl)}
	l.mu.Unlock()
	return names, nil
}

func (l *nodeInfoPeerLister) fetch(ctx context.Context, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, peerLookupTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.urlFor(host), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	res, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close() //nolint:errcheck // 読み捨て
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nodeinfo: status %d", res.StatusCode)
	}

	var parsed struct {
		Metadata struct {
			MkGoPlugins []string `json:"mkGoPlugins"`
		} `json:"metadata"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, peerLookupMaxBody)).Decode(&parsed); err != nil {
		return nil, err
	}
	return parsed.Metadata.MkGoPlugins, nil
}
