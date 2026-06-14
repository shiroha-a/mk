// Package featured implements the engagement ranking used by notes/featured,
// users/featured-notes and channel featured. It is a faithful port of upstream
// Misskey's FeaturedService: per-window Redis sorted sets (ZSET) keyed by a
// time window, incremented on reaction / renote, read by merging the current
// and previous window.
package featured

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Ranking key prefixes (upstream FeaturedService の name 引数に対応)。
const (
	globalNotesRankingName    = "featuredGlobalNotesRanking"
	inChannelNotesRankingName = "featuredInChannelNotesRanking" // ":<channelId>" を付ける
	perUserNotesRankingName   = "featuredPerUserNotesRanking"   // ":<userId>" を付ける
	galleryPostsRankingName   = "featuredGalleryPostsRanking"   // gallery/featured ランキング (#1548)
)

// Ranking time windows (upstream の *_RANKING_WINDOW 定数に対応)。各 window ごと
// に別 ZSET を持ち、古い window は TTL で失効する。
const (
	globalNotesRankingWindow  = 3 * 24 * time.Hour // 3日ごと
	perUserNotesRankingWindow = 7 * 24 * time.Hour // 1週間ごと
	// galleryPostsRankingWindow は upstream GALLERY_POSTS_RANKING_WINDOW
	// (= 3日) に対応する (#1548)。
	galleryPostsRankingWindow = 3 * 24 * time.Hour
)

// featuredEpoch は window 番号計算の基準時刻 (upstream featuredEpoc =
// 2023-01-01T00:00:00Z)。window = floor((now - epoch) / windowRange)。
var featuredEpoch = time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)

// Service maintains the engagement ranking ZSETs in Redis.
type Service struct {
	redis *redis.Client
	// now is the clock used for window computation. テストで固定するため差し替え
	// 可能にしてある (本番は time.Now)。
	now func() time.Time
}

// NewService constructs a featured ranking Service backed by the given Redis
// client.
func NewService(r *redis.Client) *Service {
	return &Service{redis: r, now: time.Now}
}

// currentWindow returns the window index for the given range at the current
// time (upstream getCurrentWindow)。
func (s *Service) currentWindow(windowRange time.Duration) int64 {
	passed := s.now().Sub(featuredEpoch)
	return int64(passed / windowRange)
}

func windowKey(name string, window int64) string {
	return name + ":" + strconv.FormatInt(window, 10)
}

// updateRankingOf increments element's score in the current window ZSET and
// (re)sets the window TTL only when it has none (upstream updateRankingOf:
// ZINCRBY + EXPIRE NX)。TTL は windowRange*3 にして current/previous window が
// 読めるだけの寿命を持たせる。
func (s *Service) updateRankingOf(ctx context.Context, name string, windowRange time.Duration, element string, score float64) error {
	key := windowKey(name, s.currentWindow(windowRange))
	pipe := s.redis.TxPipeline()
	pipe.ZIncrBy(ctx, key, score, element)
	pipe.ExpireNX(ctx, key, windowRange*3)
	_, err := pipe.Exec(ctx)
	return err
}

// getRankingOf returns up to (threshold+1)*2 (deduped) element IDs from the
// current and previous window, merged by averaging the score of elements that
// appear in both windows (upstream getRankingOf)。返り値は current window 優先
// の挿入順 (= スコア DESC の current → previous-only を後置)。呼び出し側が ID で
// 並べ替えるため順序自体に強い意味は無いが upstream と揃える。
func (s *Service) getRankingOf(ctx context.Context, name string, windowRange time.Duration, threshold int) ([]string, error) {
	cur := s.currentWindow(windowRange)
	prev := cur - 1
	pipe := s.redis.Pipeline()
	curCmd := pipe.ZRevRangeWithScores(ctx, windowKey(name, cur), 0, int64(threshold))
	prevCmd := pipe.ZRevRangeWithScores(ctx, windowKey(name, prev), 0, int64(threshold))
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}

	ranking := make(map[string]float64)
	order := make([]string, 0, len(curCmd.Val())+len(prevCmd.Val()))
	for _, z := range curCmd.Val() {
		id, ok := z.Member.(string)
		if !ok {
			continue
		}
		ranking[id] = z.Score
		order = append(order, id)
	}
	for _, z := range prevCmd.Val() {
		id, ok := z.Member.(string)
		if !ok {
			continue
		}
		if existing, dup := ranking[id]; dup {
			ranking[id] = (existing + z.Score) / 2
		} else {
			ranking[id] = z.Score
			order = append(order, id)
		}
	}
	return order, nil
}

// removeFromRanking drops element from both the current and previous window
// (upstream removeFromRanking)。note 削除時に呼ぶ。
func (s *Service) removeFromRanking(ctx context.Context, name string, windowRange time.Duration, element string) error {
	cur := s.currentWindow(windowRange)
	pipe := s.redis.Pipeline()
	pipe.ZRem(ctx, windowKey(name, cur), element)
	pipe.ZRem(ctx, windowKey(name, cur-1), element)
	_, err := pipe.Exec(ctx)
	return err
}

// UpdateGlobalNotesRanking adds score to noteID in the global notes ranking.
func (s *Service) UpdateGlobalNotesRanking(ctx context.Context, noteID string, score float64) error {
	return s.updateRankingOf(ctx, globalNotesRankingName, globalNotesRankingWindow, noteID, score)
}

// UpdateInChannelNotesRanking adds score to noteID in channelID's ranking.
func (s *Service) UpdateInChannelNotesRanking(ctx context.Context, channelID, noteID string, score float64) error {
	return s.updateRankingOf(ctx, inChannelNotesRankingName+":"+channelID, globalNotesRankingWindow, noteID, score)
}

// UpdatePerUserNotesRanking adds score to noteID in userID's per-user ranking.
func (s *Service) UpdatePerUserNotesRanking(ctx context.Context, userID, noteID string, score float64) error {
	return s.updateRankingOf(ctx, perUserNotesRankingName+":"+userID, perUserNotesRankingWindow, noteID, score)
}

// GetGlobalNotesRanking returns the top-threshold global ranked note IDs.
func (s *Service) GetGlobalNotesRanking(ctx context.Context, threshold int) ([]string, error) {
	return s.getRankingOf(ctx, globalNotesRankingName, globalNotesRankingWindow, threshold)
}

// GetInChannelNotesRanking returns the top-threshold ranked note IDs in channelID.
func (s *Service) GetInChannelNotesRanking(ctx context.Context, channelID string, threshold int) ([]string, error) {
	return s.getRankingOf(ctx, inChannelNotesRankingName+":"+channelID, globalNotesRankingWindow, threshold)
}

// GetPerUserNotesRanking returns the top-threshold ranked note IDs by userID.
func (s *Service) GetPerUserNotesRanking(ctx context.Context, userID string, threshold int) ([]string, error) {
	return s.getRankingOf(ctx, perUserNotesRankingName+":"+userID, perUserNotesRankingWindow, threshold)
}

// RemoveGlobalNotesRanking removes noteID from the global ranking windows.
func (s *Service) RemoveGlobalNotesRanking(ctx context.Context, noteID string) error {
	return s.removeFromRanking(ctx, globalNotesRankingName, globalNotesRankingWindow, noteID)
}

// RemoveInChannelNotesRanking removes noteID from channelID's ranking windows.
func (s *Service) RemoveInChannelNotesRanking(ctx context.Context, channelID, noteID string) error {
	return s.removeFromRanking(ctx, inChannelNotesRankingName+":"+channelID, globalNotesRankingWindow, noteID)
}

// RemovePerUserNotesRanking removes noteID from userID's per-user ranking windows.
func (s *Service) RemovePerUserNotesRanking(ctx context.Context, userID, noteID string) error {
	return s.removeFromRanking(ctx, perUserNotesRankingName+":"+userID, perUserNotesRankingWindow, noteID)
}

// UpdateGalleryPostsRanking adds score to postID in the gallery posts ranking
// (upstream FeaturedService.updateGalleryPostsRanking, #1548). gallery/posts/like
// で +1、unlike で -1 を加算する。
func (s *Service) UpdateGalleryPostsRanking(ctx context.Context, postID string, score float64) error {
	return s.updateRankingOf(ctx, galleryPostsRankingName, galleryPostsRankingWindow, postID, score)
}

// GetGalleryPostsRanking returns the top-threshold ranked gallery post IDs
// (upstream FeaturedService.getGalleryPostsRanking, #1548). gallery/featured で使う。
func (s *Service) GetGalleryPostsRanking(ctx context.Context, threshold int) ([]string, error) {
	return s.getRankingOf(ctx, galleryPostsRankingName, galleryPostsRankingWindow, threshold)
}
