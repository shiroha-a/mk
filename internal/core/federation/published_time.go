package federation

import (
	"time"
)

// publishedFutureSkew は AP `published` 値がローカル時計より未来側にどこまで
// 許容されるかの閾値。NTP ずれ / VM clock drift で 1-2 分の前進はあり得るので
// 5 分まで許容、それ以上は spoof / バグとみなして fallback する。
const publishedFutureSkew = 5 * time.Minute

// publishedPastFloor は AP `published` 値の過去側の許容下限。10 年以上前の
// timestamp は実質ありえず (Misskey/Mastodon は 2017+)、tampering / parse バグの
// 兆候。fallback して受信時刻を採用する。
const publishedPastFloor = 10 * 365 * 24 * time.Hour

// parseAPPublishedTime は ActivityPub Object の `published` 文字列を time.Time
// に変換する。遅延配送された note を origin の publish 時刻として timeline 上に
// 正しい順序で配置するために使う (#940)。
//
// fallback は呼び出し側で `time.Now()` を渡す前提:
//   - raw が空 → fallback
//   - RFC3339 / RFC3339Nano で parse 不可 → fallback
//   - 未来側に publishedFutureSkew を超えて飛んでいる → fallback (spoof 防御)
//   - 過去側に publishedPastFloor を超えて遡る → fallback (parse バグ防御)
func parseAPPublishedTime(raw string, fallback time.Time) time.Time {
	if raw == "" {
		return fallback
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return fallback
		}
	}
	now := time.Now()
	if t.After(now.Add(publishedFutureSkew)) {
		return fallback
	}
	if t.Before(now.Add(-publishedPastFloor)) {
		return fallback
	}
	return t
}
