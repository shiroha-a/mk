package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// RemoteUserStats represents counts fetched from a remote Misskey-compatible
// instance via /api/users/show.
type RemoteUserStats struct {
	NotesCount     int
	FollowersCount int
	FollowingCount int
}

// remoteStatsTTL は cache 有効期間。counter は数分単位で動くが、毎 request
// 取りに行くと remote 負荷が大きいので 1 時間で fold (#943)。
const remoteStatsTTL = 1 * time.Hour

// remoteStatsTimeout は remote /api/users/show の単一 fetch timeout。
const remoteStatsTimeout = 5 * time.Second

// RemoteStatsFetcher fetches notes/followers/following counts from a remote
// Misskey-compatible instance and caches the result.
//
// Misskey TS の users/show は自インスタンスで観測した範囲のみ集計するため、
// remote user の counts が実体より小さく出る (#943)。本 fetcher は user の
// origin instance の /api/users/show を叩いて公開 counts を取得し、上書き
// 表示することで「リモートサーバー上の実値」を反映する mk-go 独自拡張。
//
// 失敗時は静かに nil を返す (= caller は local 観測値を fallback として使う)。
// 同一 (host, username) への並行 fetch は singleflight で fold する。
type RemoteStatsFetcher struct {
	client *http.Client
	cache  sync.Map // key=host|username → cachedRemoteStats
	group  singleflight.Group
}

type cachedRemoteStats struct {
	stats   *RemoteUserStats
	fetched time.Time
}

// NewRemoteStatsFetcher constructs a fetcher with the given HTTP client.
// nil client は default (timeout 付き) にフォールバック。
func NewRemoteStatsFetcher(client *http.Client) *RemoteStatsFetcher {
	if client == nil {
		client = &http.Client{Timeout: remoteStatsTimeout}
	}
	return &RemoteStatsFetcher{client: client}
}

// Fetch returns cached stats if available and fresh, otherwise fetches from
// the remote /api/users/show endpoint. Returns nil if the host or username is
// empty, or if the remote call fails / returns malformed payload.
func (f *RemoteStatsFetcher) Fetch(ctx context.Context, host, username string) *RemoteUserStats {
	if f == nil || host == "" || username == "" {
		return nil
	}
	key := host + "|" + username
	if v, ok := f.cache.Load(key); ok {
		if entry, ok := v.(cachedRemoteStats); ok && time.Since(entry.fetched) < remoteStatsTTL {
			return entry.stats
		}
	}
	v, _, _ := f.group.Do(key, func() (any, error) {
		stats := f.fetchRemote(ctx, host, username)
		f.cache.Store(key, cachedRemoteStats{stats: stats, fetched: time.Now()})
		return stats, nil
	})
	if stats, ok := v.(*RemoteUserStats); ok {
		return stats
	}
	return nil
}

// fetchRemote performs the actual HTTP POST to https://<host>/api/users/show.
// Misskey 系 instance は username + host=null で local user lookup できる。
// Mastodon 等 Misskey 互換でない instance は 4xx/404 を返すので nil を出す。
func (f *RemoteStatsFetcher) fetchRemote(ctx context.Context, host, username string) *RemoteUserStats {
	endpoint := fmt.Sprintf("https://%s/api/users/show", host)
	body, _ := json.Marshal(map[string]any{
		"username": username,
		"host":     nil,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		slog.Debug("remoteStats: fetch failed", "host", host, "username", username, "err", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	// 過大 response は driver / reverse proxy 側で 4xx を返す前提なので、ここは
	// 念のため 1MB 程度で切る。Misskey UserDetailed の JSON は通常 5KB 未満。
	limited := io.LimitReader(resp.Body, 1<<20)
	var payload struct {
		NotesCount     *int `json:"notesCount"`
		FollowersCount *int `json:"followersCount"`
		FollowingCount *int `json:"followingCount"`
	}
	if err := json.NewDecoder(limited).Decode(&payload); err != nil {
		return nil
	}
	if payload.NotesCount == nil && payload.FollowersCount == nil && payload.FollowingCount == nil {
		return nil
	}
	stats := &RemoteUserStats{}
	if payload.NotesCount != nil {
		stats.NotesCount = *payload.NotesCount
	}
	if payload.FollowersCount != nil {
		stats.FollowersCount = *payload.FollowersCount
	}
	if payload.FollowingCount != nil {
		stats.FollowingCount = *payload.FollowingCount
	}
	return stats
}
