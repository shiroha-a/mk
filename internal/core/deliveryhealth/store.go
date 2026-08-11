package deliveryhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis key layout.
//
// 1 分バケットの hash に足し込み、読む側が直近 N バケットを合算する。窓が
// 自然に転がるので、掃除のための別ジョブが要らない (TTL に任せる)。
//
// **Redis に置くのは queue ノードが複数ありうるため** (#2459 で role 分離を
// 入れた)。プロセスローカルに持つと、ノードごとに別々の部分的な数字が見える。
const (
	// DirectionOutbound / DirectionInbound are the Redis key namespaces.
	//
	// **送受でキー空間を分ける。** 混ざると「配送できない host」と「受信を
	// 拒否した host」が同じ行に足し込まれ、どちらの話か分からなくなる。
	DirectionOutbound = "apDeliveryHealth"
	DirectionInbound  = "apInboxHealth"

	// fieldSep separates the parts of a hash field. host には現れない文字を
	// 使う (host は DNS 名なので `|` を含まない)。
	fieldSep = "|"
	// latencyFieldMarker distinguishes latency buckets from class counters.
	latencyFieldMarker = "lat"
)

// bucketTTL keeps a minute bucket alive slightly longer than the longest
// window a caller can ask for.
const bucketTTL = 2 * time.Hour

// lastErrorTTL bounds how long a stale host keeps its error visible.
const lastErrorTTL = 7 * 24 * time.Hour

// MaxWindow is the longest window Query accepts. bucketTTL より短くしないと
// 「期限切れで消えた分が 0 として合算される」ことになる。
const MaxWindow = time.Hour

// Store persists deltas to Redis and reads them back.
type Store struct {
	rdb    redis.UniversalClient
	clock  func() time.Time
	prefix string
	// succeeded は集計時に success / failure を振り分ける判定。Aggregator と
	// **同じものを使うこと** (ずれると記録と表示で数字が食い違う)。
	succeeded func(OutcomeClass) bool
}

// NewStore constructs an outbound-delivery Store. rdb が nil なら呼び出し側が
// Store を使わない前提 (telemetry 無効)。
func NewStore(rdb redis.UniversalClient) *Store {
	return NewStoreForDirection(rdb, DirectionOutbound)
}

// NewStoreForDirection constructs a Store writing under the given namespace.
//
// 送信 (#2461) と受信 (#2471) で集計の構造は同じなので、キー空間だけを変えて
// 同じ実装を使う。複製すると片方だけ直る形の乖離が生まれる。
func NewStoreForDirection(rdb redis.UniversalClient, direction string) *Store {
	if direction == "" {
		direction = DirectionOutbound
	}
	st := &Store{rdb: rdb, clock: time.Now, prefix: direction}
	if direction == DirectionInbound {
		st.succeeded = func(c OutcomeClass) bool { return c.SucceededInbound() }
	} else {
		st.succeeded = func(c OutcomeClass) bool { return c.Succeeded() }
	}
	return st
}

// bucketKey returns the key for the minute containing t.
func (s *Store) bucketKey(t time.Time) string {
	return s.prefix + ":bucket:" + strconv.FormatInt(t.UTC().Unix()/60, 10)
}

// lastErrorKey returns the hash holding each host's most recent failure.
func (s *Store) lastErrorKey() string { return s.prefix + ":lastError" }

// SetClockForTest overrides the time source.
func (s *Store) SetClockForTest(fn func() time.Time) { s.clock = fn }

// Flush writes deltas into the current minute bucket.
//
// 1 回の pipeline にまとめる。ホストごとに往復すると flush が Redis の
// レイテンシに比例して伸び、その間 worker が止まる。
func (s *Store) Flush(ctx context.Context, deltas []Delta) error {
	if s.rdb == nil || len(deltas) == 0 {
		return nil
	}
	key := s.bucketKey(s.clock())
	pipe := s.rdb.Pipeline()
	for _, d := range deltas {
		for class, n := range d.ByClass {
			if n == 0 {
				continue
			}
			pipe.HIncrBy(ctx, key, d.Host+fieldSep+string(class), n)
		}
		for i, n := range d.Latency {
			if n == 0 {
				continue
			}
			pipe.HIncrBy(ctx, key, d.Host+fieldSep+latencyFieldMarker+fieldSep+strconv.Itoa(i), n)
		}
		if d.LastError != nil {
			if encoded, err := json.Marshal(d.LastError); err == nil {
				pipe.HSet(ctx, s.lastErrorKey(), d.Host, encoded)
			}
		}
	}
	// TTL は毎回付け直す。バケットは 1 分ごとに新しくなるので、期限が伸び続ける
	// ことはない。
	pipe.Expire(ctx, key, bucketTTL)
	pipe.Expire(ctx, s.lastErrorKey(), lastErrorTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("deliveryhealth: flush: %w", err)
	}
	return nil
}

// HostHealth is one host's aggregated view over the queried window.
type HostHealth struct {
	Host    string                 `json:"host"`
	Success int64                  `json:"success"`
	Failure int64                  `json:"failure"`
	ByClass map[OutcomeClass]int64 `json:"byClass"`
	// LatencyP50Ms / LatencyP95Ms are histogram approximations. 正確な分位数
	// ではなく、該当バケットの上限を返す (+Inf バケットは -1)。
	LatencyP50Ms int64      `json:"latencyP50Ms"`
	LatencyP95Ms int64      `json:"latencyP95Ms"`
	LastError    *LastError `json:"lastError,omitempty"`
}

// Query aggregates the last `window` worth of minute buckets.
func (s *Store) Query(ctx context.Context, window time.Duration) ([]HostHealth, error) {
	if s.rdb == nil {
		return nil, nil
	}
	if window <= 0 || window > MaxWindow {
		window = MaxWindow
	}
	minutes := int(window / time.Minute)
	if minutes < 1 {
		minutes = 1
	}

	now := s.clock().UTC()
	byHost := map[string]*HostHealth{}
	latency := map[string]*[latencyBucketCount]int64{}

	for i := 0; i < minutes; i++ {
		key := s.bucketKey(now.Add(-time.Duration(i) * time.Minute))
		fields, err := s.rdb.HGetAll(ctx, key).Result()
		if err != nil {
			return nil, fmt.Errorf("deliveryhealth: query bucket: %w", err)
		}
		for field, raw := range fields {
			n, convErr := strconv.ParseInt(raw, 10, 64)
			if convErr != nil {
				continue
			}
			host, class, latIdx, ok := parseField(field)
			if !ok {
				continue
			}
			h, exists := byHost[host]
			if !exists {
				h = &HostHealth{Host: host, ByClass: map[OutcomeClass]int64{}}
				byHost[host] = h
				latency[host] = &[latencyBucketCount]int64{}
			}
			if latIdx >= 0 {
				latency[host][latIdx] += n
				continue
			}
			h.ByClass[class] += n
			if s.succeeded(class) {
				h.Success += n
			} else {
				h.Failure += n
			}
		}
	}
	if len(byHost) == 0 {
		return nil, nil
	}

	lastErrors, err := s.rdb.HGetAll(ctx, s.lastErrorKey()).Result()
	if err != nil {
		// 直近エラーが読めなくても件数は返せる。観測を全部落とさない。
		lastErrors = nil
	}

	out := make([]HostHealth, 0, len(byHost))
	for host, h := range byHost {
		h.LatencyP50Ms = approxQuantileMs(latency[host], 0.50)
		h.LatencyP95Ms = approxQuantileMs(latency[host], 0.95)
		if raw, ok := lastErrors[host]; ok {
			var le LastError
			if json.Unmarshal([]byte(raw), &le) == nil {
				h.LastError = &le
			}
		}
		out = append(out, *h)
	}
	// 失敗が多い順。運用者が最初に見たいのは壊れているホスト。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Failure != out[j].Failure {
			return out[i].Failure > out[j].Failure
		}
		return out[i].Host < out[j].Host
	})
	return out, nil
}

// parseField splits a hash field back into its parts. latIdx >= 0 means the
// field is a latency bucket rather than a class counter.
func parseField(field string) (host string, class OutcomeClass, latIdx int, ok bool) {
	parts := strings.Split(field, fieldSep)
	switch len(parts) {
	case 2:
		return parts[0], OutcomeClass(parts[1]), -1, parts[0] != ""
	case 3:
		if parts[1] != latencyFieldMarker {
			return "", "", -1, false
		}
		idx, err := strconv.Atoi(parts[2])
		if err != nil || idx < 0 || idx >= latencyBucketCount {
			return "", "", -1, false
		}
		return parts[0], "", idx, parts[0] != ""
	default:
		return "", "", -1, false
	}
}

// approxQuantileMs returns the upper bound of the bucket containing the
// requested quantile. +Inf バケットに落ちた場合は -1 (「計測上限を超えた」)。
//
// 正確な分位数は返さない。バケット境界の粗さがそのまま誤差になるが、
// 「遅い」を見分ける用途には足りる。
func approxQuantileMs(buckets *[latencyBucketCount]int64, q float64) int64 {
	if buckets == nil {
		return 0
	}
	var total int64
	for _, n := range buckets {
		total += n
	}
	if total == 0 {
		return 0
	}
	// 「q 以上を満たす最小のバケット」を探す。ceil 相当にするため、
	// running >= q*total で判定する。
	threshold := float64(total) * q
	var running float64
	for i, n := range buckets {
		running += float64(n)
		if running >= threshold {
			if i >= len(latencyBucketsMs) {
				return -1
			}
			return latencyBucketsMs[i]
		}
	}
	return -1
}
