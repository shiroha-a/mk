package id

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	// time2000 is the epoch used by AID/AIDX (2000-01-01T00:00:00Z in milliseconds).
	// AID/AIDXはbase36 8桁、ObjectIDはunsigned 32-bit秒、ULIDは48-bit
	// millisecondのtimestamp fieldを持つため、各定数をwire上限として使う。
	time2000              int64 = 946684800000
	aidMaxTimeMillis      int64 = time2000 + 36*36*36*36*36*36*36*36 - 1
	meidMinTimeMillis     int64 = -(1 << 47)
	meidMaxTimeMillis     int64 = (1 << 47) - 1
	objectIDMaxTimeSecond int64 = (1 << 32) - 1
	ulidMaxTimeMillis     int64 = (1 << 48) - 1
)

// AidxCutoffPrefix builds the smallest aidx-style ID at the given time
// for use as a `WHERE id > ?` cutoff in note range scans (mk-go の note
// は created_at 列を持たず aidx ID 先頭 8 文字に ms timestamp を埋め込
// んでいる)。後続 8 文字 (nodeID + counter) は最小値 "00000000" で揃え、
// その時刻における最小 ID を作る。
//
// 呼び出し側は \`db.Where("id > ?", id.AidxCutoffPrefix(t))\` のように
// 使い、aidx 規約と齟齬が出ないよう time2000 / base36 padding は本
// パッケージの実装と必ず一致させる。
func AidxCutoffPrefix(t time.Time) string {
	ms := clampUnixMillis(t, time2000, aidMaxTimeMillis) - time2000
	timePart := fmt.Sprintf("%08s", strconv.FormatInt(ms, 36))
	return timePart + "00000000"
}

// Generator defines the interface for ID generation.
type Generator interface {
	// Generate creates a new ID for the given timestamp.
	Generate(t time.Time) string
	// ParseTime extracts the timestamp from an ID.
	ParseTime(id string) (time.Time, error)
}

// NewGenerator creates a Generator based on the method name.
// Supported: "aid", "aidx", "meid", "meidg", "ulid", "objectid".
func NewGenerator(method string) (Generator, error) {
	switch strings.ToLower(method) {
	case "aid":
		return newAID(), nil
	case "aidx":
		return newAIDX(), nil
	case "meid":
		return newMEID(), nil
	case "objectid":
		return newObjectID(), nil
	case "ulid":
		return newULID(), nil
	default:
		return nil, fmt.Errorf("unknown id method: %s", method)
	}
}

// --- AID ---

type aidGen struct {
	mu      sync.Mutex
	counter uint16
}

func newAID() *aidGen {
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return &aidGen{counter: uint16(b[0]) | uint16(b[1])<<8}
}

func (g *aidGen) Generate(t time.Time) string {
	ms := clampUnixMillis(t, time2000, aidMaxTimeMillis) - time2000
	timePart := padLeft(strconv.FormatInt(ms, 36), 8, '0')

	g.mu.Lock()
	g.counter++
	c := g.counter
	g.mu.Unlock()

	counterPart := padLeft(strconv.FormatUint(uint64(c), 36), 2, '0')
	counterPart = counterPart[len(counterPart)-2:]

	return timePart + counterPart
}

func (g *aidGen) ParseTime(id string) (time.Time, error) {
	if len(id) < 8 {
		return time.Time{}, fmt.Errorf("invalid aid: %s", id)
	}
	ms, err := strconv.ParseInt(id[:8], 36, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms + time2000), nil
}

// --- AIDX ---

type aidxGen struct {
	mu      sync.Mutex
	counter uint32
	nodeID  string
}

func newAIDX() *aidxGen {
	return &aidxGen{
		nodeID: randomBase36(4),
	}
}

func (g *aidxGen) Generate(t time.Time) string {
	ms := clampUnixMillis(t, time2000, aidMaxTimeMillis) - time2000
	timePart := padLeft(strconv.FormatInt(ms, 36), 8, '0')

	g.mu.Lock()
	g.counter++
	c := g.counter
	g.mu.Unlock()

	counterPart := padLeft(strconv.FormatUint(uint64(c), 36), 4, '0')
	counterPart = counterPart[len(counterPart)-4:]

	return timePart + g.nodeID + counterPart
}

func (g *aidxGen) ParseTime(id string) (time.Time, error) {
	if len(id) < 8 {
		return time.Time{}, fmt.Errorf("invalid aidx: %s", id)
	}
	ms, err := strconv.ParseInt(id[:8], 36, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms + time2000), nil
}

// --- MEID ---

type meidGen struct{}

func newMEID() *meidGen { return &meidGen{} }

// MEIDはsigned millisecondをoffset付き12桁hexとしてencodeする。upstreamは負の
// 時刻を0へclampするが、mk-goは既存のsigned round tripを維持し、encode可能な
// 上下限でのみclampする。
const meidOffset int64 = 0x800000000000

func (g *meidGen) Generate(t time.Time) string {
	ms := clampUnixMillis(t, meidMinTimeMillis, meidMaxTimeMillis)
	timePart := padLeft(strconv.FormatInt(ms+meidOffset, 16), 12, '0')
	return timePart + randomHex(12)
}

func (g *meidGen) ParseTime(id string) (time.Time, error) {
	if len(id) < 12 {
		return time.Time{}, fmt.Errorf("invalid meid: %s", id)
	}
	ms, err := strconv.ParseInt(id[:12], 16, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms - meidOffset), nil
}

// --- ObjectID ---

type objectIDGen struct{}

func newObjectID() *objectIDGen { return &objectIDGen{} }

func (g *objectIDGen) Generate(t time.Time) string {
	sec := clampUnixSeconds(t, 0, objectIDMaxTimeSecond)
	timePart := padLeft(strconv.FormatInt(sec, 16), 8, '0')
	return timePart + randomHex(16)
}

func (g *objectIDGen) ParseTime(id string) (time.Time, error) {
	if len(id) < 8 {
		return time.Time{}, fmt.Errorf("invalid objectid: %s", id)
	}
	sec, err := strconv.ParseInt(id[:8], 16, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(sec, 0), nil
}

// --- ULID ---

// ulidGen wraps oklog/ulid/v2 which implements the ULID spec including
// monotonic entropy. 自前の5bit truncation実装だと下位5bitがオーバー
// フローした瞬間に単調性が破れる (issue #388) ため、仕様準拠の80bit
// monotonic entropyを使う実装に置き換えている。
type ulidGen struct {
	entropy io.Reader
}

func newULID() *ulidGen {
	// ulid.Monotonic は同一 ms 内で返す entropy を単調増加させる。
	// 内部で mutex を持つので外側での排他制御は不要。inc=0 を渡すと
	// default (uint32範囲でのrandom increment) を使う。
	return &ulidGen{
		entropy: ulid.Monotonic(rand.Reader, 0),
	}
}

func (g *ulidGen) Generate(t time.Time) string {
	// MustNewはentropyオーバーフロー時にpanicするが、80bit entropyが
	// 同一ms内で使い切られるのは実用上起こらないので許容する。
	ms := clampUnixMillis(t, 0, ulidMaxTimeMillis)
	return ulid.MustNew(uint64(ms), g.entropy).String()
}

func (g *ulidGen) ParseTime(id string) (time.Time, error) {
	// ParseStrictはCrockford base32の厳格validationを行う。Parseだと
	// 不正な文字が silentにmapされてしまい TestParseTime_InvalidChars の
	// 意図 (不正ID は error を返す) に反するため使わない。
	u, err := ulid.ParseStrict(id)
	if err != nil {
		return time.Time{}, err
	}
	return ulid.Time(u.Time()), nil
}

// --- Helpers ---

// 極端なtime.TimeでUnixMilli/Unixを先に呼ぶとint64 overflowでclamp方向を
// 判定できないため、変換前のtime.Time同士で表現可能範囲を比較する。
func clampUnixMillis(t time.Time, min, max int64) int64 {
	if t.Before(time.UnixMilli(min)) {
		return min
	}
	if t.After(time.UnixMilli(max)) {
		return max
	}
	return t.UnixMilli()
}

func clampUnixSeconds(t time.Time, min, max int64) int64 {
	if t.Before(time.Unix(min, 0)) {
		return min
	}
	if t.After(time.Unix(max, 0)) {
		return max
	}
	return t.Unix()
}

func padLeft(s string, length int, pad byte) string {
	for len(s) < length {
		s = string(pad) + s
	}
	return s
}

func randomBase36(n int) string {
	const chars = "0123456789abcdefghijklmnopqrstuvwxyz"
	result := make([]byte, n)
	for i := range result {
		idx, _ := rand.Int(rand.Reader, big.NewInt(36))
		result[i] = chars[idx.Int64()]
	}
	return string(result)
}

func randomHex(n int) string {
	const chars = "0123456789abcdef"
	result := make([]byte, n)
	for i := range result {
		idx, _ := rand.Int(rand.Reader, big.NewInt(16))
		result[i] = chars[idx.Int64()]
	}
	return string(result)
}
