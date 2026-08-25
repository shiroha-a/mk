// Package federation provides services for processing inbound and outbound
// ActivityPub traffic.
package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/activitypub/mfm"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/misc/hashtag"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/misc/idnhost"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// HTTPFetcher abstracts the HTTP client used for fetching remote AP objects.
// 実装は activitypub.Client か互換のテストダブル。署名なし fetch でも互換性は
// 保たれるため、ここでは accept ヘッダ指定だけ受け取る。
type HTTPFetcher interface {
	FetchObject(uri string) ([]byte, error)
}

// InstanceTracker is an interface used by Resolver to register hosts as soon
// as a remote user is fetched. パッケージ間の循環依存を避けるため interface で
// 受け取る (実装は core/instance.Service)。
type InstanceTracker interface {
	RegisterFromHost(host string) (*model.Instance, error)
}

// ChartHook is invoked after a remote user has been freshly created so
// the chart subsystem can record the new-user event into UsersChart and
// InstanceChart. パッケージ間の循環依存を避けるため interface で受け取る
// (実装は core/chart/charthook)。
type ChartHook interface {
	OnRemoteUserCreated(user *model.User)
}

// HashtagHook is invoked after a remote note has been freshly persisted
// (or updated) so the hashtag subsystem can record per-tag mentioned
// counts. パッケージ間の循環依存を避けるため interface で受け取る (実装は
// core/hashtag.Service)。#680。
//
// 実装契約 (#719): OnNoteCreated は **non-blocking** でなければならず、
// repo 書き込みが必要な場合は実装側で goroutine を起こすこと。IngestNote /
// UpdateRemoteNote は本 hook 結果を待たずに即時 return することで inbox
// processor の drain time が tag 数に比例しないようにする。panic recovery
// も実装側の責務。
type HashtagHook interface {
	OnNoteCreated(note *model.Note, author *model.User)
	// UpdateUsertags reconciles the hashtag attach aggregate when a remote
	// user's person.tag derived tags change (#1362)。OnNoteCreated と同じく
	// non-blocking であること。
	UpdateUsertags(userID string, isLocal bool, oldTags, newTags []string)
}

// Errors returned by Resolver.
var (
	// ErrInvalidActor is returned when the fetched JSON cannot be parsed.
	ErrInvalidActor = errors.New("invalid actor document")
	// ErrInvalidNote is returned when a fetched Note cannot be parsed.
	ErrInvalidNote = errors.New("invalid note document")
	// ErrHostNotAllowed is returned when the resolver is asked to fetch /
	// ingest content from a host that the local instance's federation policy
	// (federation: none / specified, blockedHosts) does not allow.
	ErrHostNotAllowed = errors.New("host not allowed by federation policy")
	// ErrObjectHostMismatch is returned when a fetched AP object's id host does
	// not match the host that actually served it (after redirects). Mirrors
	// upstream assertActivityMatchesUrl の hard requirement で object-spoofing を
	// 防ぐ (#1820)。
	ErrObjectHostMismatch = errors.New("fetched object id host does not match response host")
	// ErrNoteAttributionMismatch is returned when an inbound Note's id host does
	// not match its attributedTo host, or its attributedTo does not match the
	// delivering actor. Mirrors upstream ApNoteService.validateNote で note 偽装
	// (なりすまし) を防ぐ (#1839)。malformed note (ErrInvalidNote) と違い、これは
	// retry しても解消しないため caller は ack して drop する。
	ErrNoteAttributionMismatch = errors.New("note attribution does not match delivering actor or id host")
	// ErrResolveFragment is returned when a resolve target URL carries a `#`
	// fragment. Fragments are not transmitted over HTTP(S) so such URLs cannot
	// be dereferenced; mirrors upstream Resolver の b94fd5b1 guard (#1828)。
	ErrResolveFragment = errors.New("cannot resolve URL with fragment")
	// ErrRecursionLimit is returned when a single resolve operation's note/quote
	// chain exceeds resolveRecursionLimit, mirroring upstream Resolver の
	// d592da9f guard。悪意ある quote チェーンによる無限再帰・amplification を防ぐ。
	ErrRecursionLimit = errors.New("hit recursion limit")
	// ErrResolveWouldDeadlock is returned when joining an in-flight resolve
	// would close a wait cycle across goroutines. 呼び出し側は既存行に落とすか、
	// その回の解決を諦める (#2685 review HIGH-1)。
	ErrResolveWouldDeadlock = errors.New("resolving would deadlock: wait cycle")
	// ErrResolveJoinTimeout is returned when this resolve tree used up its
	// total wait budget (resolveJoinTimeout) waiting on other chains.
	//
	// 循環検出の**保険**。検出はモデル化した待ちしか見ないので、載っていない
	// 待ちが増えれば見逃す。見逃しても永久には止まらないようにする。
	ErrResolveJoinTimeout = errors.New("resolving timed out waiting for an in-flight resolve")
	// ErrNoteAncestorIngesting is what a best-effort resolve returns when the
	// document it fetched is already being ingested by an ancestor of the same
	// resolve chain, and no row exists yet to fall back to (#2695).
	//
	// **相乗りした別チェーンにも届きうる** (group は先頭の返り値を配る)。それは
	// 受け取った側の話ではないので、下記の印を見て 1 度だけやり直させる。
	//
	// **取り込みを横取りしない**ためのもの。横取りすると外側の `Create` が
	// UNIQUE に当たって `created=false` になり、呼び出し側が通知とチャートの
	// フックを飛ばす (#2686 と同じ形)。
	//
	// **`errChainLocalVerdict` を包んである。** これは先頭のチェーンの状態から
	// 出した答えで、相乗りしただけの別チェーンには当てはまらない。素で返すと
	// group がそれを追従側へ配り、**引用の renoteId を恒久的に落とす**。
	ErrNoteAncestorIngesting = fmt.Errorf("note is already being ingested by an ancestor of this resolve chain: %w", errChainLocalVerdict)
	// ErrResolveWouldBlock is returned to a best-effort caller that would have
	// had to wait for another chain's in-flight resolve. 待たずに既存行へ落とす。
	ErrResolveWouldBlock = errors.New("resolving would block on another in-flight resolve")
	// errResolveAborted wakes followers when the leader unwound without a
	// result (panic 等)。起こさないと鍵が残り、以降の追従側が上限まで待たされる。
	errResolveAborted = errors.New("in-flight resolve aborted")
)

// resolveJoinTimeout bounds how long **one resolve tree** may spend waiting for
// other chains' in-flight resolves, in total. 先頭側には掛けない (自分の HTTP
// timeout で終わる)。
//
// **join ごとではなく木ごと。** 1 回の解決は著者・返信・引用と何度も待ちうるし、
// 引用チェーンは resolveRecursionLimit まで入れ子になれるので、join ごとに
// 掛けると回数分積み上がる (#2685 review HIGH-2)。予算は木の根で作って枝が
// 共有する (resolveChain.budget)。**時刻の期限ではなく、待ちに費やした時間で
// 減らす** — 理由は resolveChain.budget のコメント。
//
// AP fetch は 1 本 30s で、先頭は著者・返信・引用を順に引くので分単位まで
// 伸びうる。短くすると #2685 が直した「別 worker が取り込み中の引用先を待てずに
// renoteId を落とす」が戻るため、実際の解決より十分長い側に倒してある。ここに
// 掛かるのは循環検出の見落としか、相手方が極端に遅い場合。
//
// var なのはテストから縮めるため。実行時に書き換える経路は無い。
var resolveJoinTimeout = 5 * time.Minute

// resolveRecursionLimit bounds how deep a single resolve operation may recurse
// through note quote chains, matching upstream Resolver.recursionLimit (256)。
const resolveRecursionLimit = 256

// EphemeralSink receives relay-delivered notes and their authors instead of
// the database (#2332)。実装は core/ephemeral.Store。
//
// リレー経由でしか観測しない投稿は、ローカルの誰とも関係が無いまま流れていく
// だけなので DB に入れる価値が無い。それでも保存すると流量に比例して INSERT と
// DELETE が両方発生し、autovacuum まで含めた I/O が二重に掛かる。
type EphemeralSink interface {
	// Enabled は取り込みを ephemeral 経路に倒すかどうか。false なら従来どおり
	// DB へ保存する。sink が配線されているだけで機能が入ってしまわないよう、
	// 既定無効の担保をここで行う。
	Enabled() bool
	PutNote(ctx context.Context, n *model.Note, author *model.User) error
	UserIDByURI(ctx context.Context, uri string) (string, error)
	GetUser(ctx context.Context, id string) (*model.User, error)
	// NoteIDByURI / DropNote は重複解消に使う。同じ投稿が後から直接配送で
	// DB に入ったとき、ephemeral 側を落とさないとタイムラインに 2 回出る。
	NoteIDByURI(ctx context.Context, uri string) (string, error)
	DropNote(ctx context.Context, id, uri string) error
	// GetNote は取り込み時の dedup で既存の ephemeral ノートを返すために使う
	// (#2397)。ID だけでは呼び出し側が note を返せない。
	GetNote(ctx context.Context, id string) (*model.Note, error)
}

// EphemeralTimelineRemover drops a superseded ephemeral note ID from the
// Redis timeline lists (#2332)。実装は core/timeline.FanoutHook。
type EphemeralTimelineRemover interface {
	RemoveNoteID(noteID string, author *model.User, visibility string, host *string)
}

// MaterializeActor creates (or returns) the database row for a remote actor,
// reusing preassignedID when the row does not exist yet (#2332).
//
// ephemeral store から著者を起こすときの入口。ID を据え置くのは、変えると
// Redis に残っている既存ノートが古い ID を指したままになり、ミュートが効かなく
// なるため。鍵ごと正規に作るため actor は fetch し直す。
func (r *Resolver) MaterializeActor(uri, preassignedID string) (*model.User, error) {
	return r.resolveActorWithID(uri, preassignedID)
}

// RelayObservedMarker records that a remote user was first seen via a relay
// (#2340)。実装は repository.RelayObservedUserRepository。
type RelayObservedMarker interface {
	MarkObserved(userID string) error
}

// SetRelayObservedMarker wires the relay-observed recorder. Optional.
func (r *Resolver) SetRelayObservedMarker(m RelayObservedMarker) { r.relayObserved = m }

// SetEphemeralSink wires the ephemeral store. Optional — nil keeps the
// database-only behaviour.
func (r *Resolver) SetEphemeralSink(s EphemeralSink) { r.ephemeralSink = s }

// SetEphemeralTimelineRemover wires the timeline list cleaner used when a
// database row supersedes an ephemeral entry. Optional.
func (r *Resolver) SetEphemeralTimelineRemover(t EphemeralTimelineRemover) { r.ephemeralTimeline = t }

// ephemeralNoteByURI returns the ephemeral note already stored for uri, or nil.
//
// ephemeral 経路は DB に行を作らないので、`noteRepo.FindByURI` の miss は
// 「まだ取り込んでいない」を意味しない。URI 逆引きを併せて引かないと、2 つ目の
// リレーから同じ投稿が届くたびに別 ID を採番してタイムラインに重複する (#2397)。
// 著者側は resolveNoteAuthor が同じ逆引きを既に行っている。
func (r *Resolver) ephemeralNoteByURI(uri string) *model.Note {
	if r.ephemeralSink == nil || uri == "" {
		return nil
	}
	ctx := context.Background()
	id, err := r.ephemeralSink.NoteIDByURI(ctx, uri)
	if err != nil || id == "" {
		return nil
	}
	n, err := r.ephemeralSink.GetNote(ctx, id)
	if err != nil || n == nil {
		return nil
	}
	return n
}

// dropSupersededEphemeral removes an ephemeral entry that the freshly created
// database row supersedes, along with its now-stale ID in the timeline lists.
//
// ephemeral 側の ID と DB 行の ID は別物 (先に ephemeral として採番したものは
// materialize 時にのみ引き継がれる) なので、FTT に残った旧 ID も除かないと
// hydrate で ephemeral 側が拾われ続ける。
func (r *Resolver) dropSupersededEphemeral(uri, visibility string, author *model.User) {
	if r.ephemeralSink == nil || uri == "" {
		return
	}
	ctx := context.Background()
	oldID, err := r.ephemeralSink.NoteIDByURI(ctx, uri)
	if err != nil || oldID == "" {
		return
	}
	if derr := r.ephemeralSink.DropNote(ctx, oldID, uri); derr != nil {
		slog.Warn("federation: failed to drop superseded ephemeral note", "uri", uri, "err", derr)
	}
	if r.ephemeralTimeline != nil && author != nil {
		r.ephemeralTimeline.RemoveNoteID(oldID, author, visibility, author.Host)
	}
}

// resolveNoteAuthor resolves a note's author, keeping relay-only authors out
// of the database (#2332).
//
// 解決順が重要:
//
//  1. DB を先に引く。ミュート済み / フォロー済み / 過去に materialize 済みの
//     著者は実 ID を使う。ここを飛ばすと、ミュートしたのに ephemeral 側の
//     別 ID を持つ投稿がタイムラインに残り続ける
//  2. ephemeral store の URI 逆引き。同じ著者の 2 件目以降で ID を再利用する。
//     これが無いと投稿ごとに別 ID を採番して同一人物が別人として並ぶ
//  3. どちらにも無ければ通常の ResolveActor で解決する。DB 行は作られるが、
//     ephemeral な著者を作る経路は Phase 2 (materialize) で扱う
func (r *Resolver) resolveNoteAuthor(uri string, ephemeral bool, depth int, chain *resolveChain) (*model.User, error) {
	if !ephemeral || r.ephemeralSink == nil {
		// depth > 0 は「既にノート解決の内側に居る」ことを意味する。ここで
		// featured を引くと、ピン留め → 引用先 → その著者 → その featured …
		// と入れ子になり、1 段ごとに 5 分岐する取得の連鎖になる (#2552)。
		// 入口が depth 0 なので、通常の配送で著者を初めて観測する経路は
		// これまでどおり featured を引く。
		return r.resolveActor(uri, false, depth > 0, chain)
	}
	if existing, err := r.userRepo.FindByURI(uri); err == nil && existing != nil {
		return existing, nil
	}
	ctx := context.Background()
	if id, err := r.ephemeralSink.UserIDByURI(ctx, uri); err == nil && id != "" {
		if u, uerr := r.ephemeralSink.GetUser(ctx, id); uerr == nil && u != nil {
			return u, nil
		}
	}
	// 3. どこにも居なければ fetch して **DB に入れずに** 組み立てる。
	//
	// ここを ResolveActor に任せると、ノートを Redis に逃がしても著者だけが
	// DB に積み上がる。実測でリレー購読後は note と user がほぼ 1:1 で増える
	// ため、著者を止めないと肥大化を抑える目的が半分しか達成できない。
	return r.resolveActorEphemeral(uri, chain)
}

// resolveActorEphemeral fetches an actor and builds the row **without
// persisting it** (#2332)。
//
// singleflight key を通常経路と分けるのは、同一 URI に対する DB 経路の解決と
// 混ざると片方が意図しない層へ書かれるため (note 側と同じ理由)。
func (r *Resolver) resolveActorEphemeral(uri string, chain *resolveChain) (*model.User, error) {
	key := "eph\x00" + crossHostKey(uri, false)
	chain = chain.ensureTree()
	v, err := r.joinResolve(&r.resolveActorGroup, chain, actorWaitKey(key), key, func() (any, error) {
		// ephemeral な行は DB に載らないので featured の取り込み対象外。
		return r.resolveActorOnceWithID(uri, false, "", true, false, true, chain)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, ErrInvalidActor
	}
	return v.(*model.User), nil
}

// DefaultActorTTL is the default duration after which a cached actor (and its
// public key) is considered stale and refetched on next access.
const DefaultActorTTL = 24 * time.Hour

// publicKeyEntry stores a cached PEM together with the time it was fetched so
// the resolver can detect TTL expiry.
type publicKeyEntry struct {
	pem       string
	fetchedAt time.Time
}

// PublickeyStore abstracts persistence of remote actor public keys. Resolver
// uses this to fall back to the database when the in-memory cache misses.
type PublickeyStore interface {
	Upsert(pk *model.UserPublickey) error
	FindByUserID(userID string) (*model.UserPublickey, error)
}

// PublickeyExtraStore abstracts persistence of *additional* remote actor
// public keys (Ed25519 / Multikey) for FEP-521a compliant peers. Resolver
// upserts here when an inbound actor JSON includes `assertionMethod[]`
// (#1067 / #1070). `user_publickey_extra` table backs this in production;
// tests use an in-memory mock.
//
// DeleteByKeyID は resolver が cacheAssertionMethods で actor JSON から消えた
// 旧 keyId を purge するために使う。これが無いと remote が Ed25519 鍵を
// rotate した後も古い鍵が user_publickey_extra に残り、compromised key で
// の rogue verify を許してしまう (#1070 follow-up)。
type PublickeyExtraStore interface {
	Upsert(pk *model.UserPublickeyExtra) error
	// FindByUserAndKeyID は (userID, keyID) で actor-scope に鍵を引く。keyID 単独の
	// global lookup (FindByKeyID) は、攻撃者 actor が assertionMethod に victim の
	// keyId を載せて鍵を植え込み victim を verify する key confusion を許すため、
	// 意図的に interface へ公開しない (#security: HTTP-sig key confusion)。
	FindByUserAndKeyID(userID, keyID string) (*model.UserPublickeyExtra, error)
	ListByUserID(userID string) ([]model.UserPublickeyExtra, error)
	DeleteByKeyID(userID, keyID string) error
}

// Resolver fetches remote actors / notes and persists them in the local
// user / note tables.
//
// 公開鍵は in-memory + DB (user_publickey テーブル) の二段キャッシュで管理
// する。エントリは actorTTL を超えると miss として扱い、次回 ResolveActor 時
// にリフレッシュされる。
type Resolver struct {
	userRepo repository.UserRepository
	noteRepo repository.NoteRepository
	urls     *activitypub.URLBuilder
	fetcher  HTTPFetcher
	idGen    id.Generator
	// keysMu は keys map (userID → publicKey + fetchedAt) の concurrent
	// access を保護する。queue worker や inbox handler は別 actor を並行
	// 処理するため、ロック無しの map read/write は runtime panic を起こす
	// (Devin review #555 FLAG-1)。
	keysMu sync.RWMutex
	keys   map[string]publicKeyEntry // userID → publicKey + fetchedAt
	// keyFetchFailures は refreshPublicKey が失敗した時刻 (userID → 時刻)。
	// keysMu で保護する。keyFetchBackoff の説明を参照。
	keyFetchFailures map[string]time.Time
	clock            func() time.Time // テストで差し替える時計
	actorTTL         time.Duration    // アクター情報の最大寿命
	// moveProcessor はリモートアカウント移行の検知時に呼ぶ引き継ぎ処理
	// (#2414)。実体は core/move.Service。nil なら移行を検知しても何もしない。
	moveProcessor      RemoteMoveProcessor
	instanceTracker    InstanceTracker             // optional: ホスト発見を通知
	chartHook          ChartHook                   // optional: 新規 remote user の集計
	hashtagHook        HashtagHook                 // optional: per-tag mentionedUsersCount 集計 (#680)
	publickeyRepo      PublickeyStore              // optional: 公開鍵の永続化 (RSA)
	publickeyExtraRepo PublickeyExtraStore         // optional: 追加公開鍵 (Ed25519 / Multikey) の永続化
	capabilities       SignatureCapabilityDeclarer // optional: Ed25519 対応宣言の記録 (#2393)
	pollRepo           repository.PollRepository   // optional: Question(投票)のPoll作成
	// pinningRepo / pinningIDGen はリモート actor の featured コレクション
	// (ピン留め投稿) の取り込み先 (#2552)。未配線なら取り込まない。
	pinningRepo   repository.UserNotePiningRepository
	pinningIDGen  id.Generator
	pollVoter     PollVoter                      // optional: AP vote (Note.name) の投票記録
	emojiRepo     repository.EmojiRepository     // optional: リモート絵文字の永続化
	driveFileRepo repository.DriveFileRepository // optional: リモート添付の link 化
	// imageProbeClient は image attachment の dimension probe (#461) で
	// 使う outbound HTTP client。SSRF-safe transport (router.go で
	// safehttp.NewSSRFSafeTransport を適用したもの) を渡す前提で、
	// 未設定なら probe 自体をスキップする (安全側に倒す: SSRF リスクを
	// 起こすくらいなら properties 空のまま運用)。
	// ephemeralSink はリレー経由でしか観測しない投稿の置き場 (#2332)。
	// 設定が有効かつ配線されているときだけ、DB ではなくこちらへ書く。
	ephemeralSink EphemeralSink
	// relayObserved はリレー経由で初めて観測した remote user の記録先 (#2340)。
	relayObserved RelayObservedMarker
	// ephemeralTimeline は DB 行が ephemeral を上書きしたときに FTT から旧 ID
	// を除くためのもの。残すと hydrate で ephemeral 側が拾われ二重表示になる。
	ephemeralTimeline EphemeralTimelineRemover
	imageProbeClient  *http.Client
	// hostBlocker は federation 設定 (none / specified / blockedHosts) を
	// 評価する gate。fetchActor / resolveNoteOnce / IngestNoteWithCreated
	// の入口で URI の host / attributedTo の host を判定して、ホワイト
	// リスト外 host からの取り込みを拒否する。未配線時は legacy 挙動
	// (全 host 許可) にフォールバック。
	hostBlocker HostBlockChecker

	// silencedChecker は remote note ingest 時に meta.silencedHosts 該当 host の
	// public note を home に降格する判定に使う (#2106 N14)。未配線時は降格しない。
	silencedChecker SilencedHostChecker

	// resolveActorGroup / resolveNoteGroup は同一 URI への並行 ResolveActor /
	// ResolveNote 呼び出しを 1 度の DB lookup + HTTP fetch に collapse する
	// (#300 3-7)。inbox 受信時に同じ remote actor / note を参照する activity
	// が連続して届く現実的なケースで thundering herd を抑える。
	resolveActorGroup resolveGroup
	resolveNoteGroup  resolveGroup
	// waits は cross-goroutine の待ち循環を検出する (#2685 review HIGH-1)。
	// チェーンローカルの判定は自分の祖先しか見ないので、これが無いと相互引用で
	// worker が永久に止まる。
	waits resolveWaits
	// 解決の in-flight は resolveChain が持つ (#2685)。以前は Resolver に
	// sync.Map を 2 つ (ingesting / resolvingNotes) 置いていたが、プロセス全体で
	// 共有されるため「自分の祖先が握っている」と「無関係な goroutine が握って
	// いる」を区別できなかった。
}

// NewResolver constructs a Resolver.
// noteRepo / urls はリモート Note の解決と取り込みに使用する。リモート Note
// 機能を使わない呼び出し側 (テスト等) では nil を渡してよい。
func NewResolver(
	userRepo repository.UserRepository,
	noteRepo repository.NoteRepository,
	urls *activitypub.URLBuilder,
	fetcher HTTPFetcher,
	idGen id.Generator,
) *Resolver {
	return &Resolver{
		userRepo: userRepo,
		noteRepo: noteRepo,
		urls:     urls,
		fetcher:  fetcher,
		idGen:    idGen,
		keys:     map[string]publicKeyEntry{},
		clock:    time.Now,
		actorTTL: DefaultActorTTL,
	}
}

// SetClock replaces the clock used for TTL checks. Intended for tests.
func (r *Resolver) SetClock(now func() time.Time) {
	if now != nil {
		r.clock = now
	}
}

// SetActorTTL overrides the actor TTL used for cache freshness checks.
// 0 以下を渡した場合は変更しない。
func (r *Resolver) SetActorTTL(d time.Duration) {
	if d > 0 {
		r.actorTTL = d
	}
}

// SetInstanceTracker attaches an InstanceTracker that will be notified each
// time a remote actor is fetched (created or refreshed). nil 渡しは無効化と
// 同義。
func (r *Resolver) SetInstanceTracker(t InstanceTracker) {
	r.instanceTracker = t
}

// SetChartHook attaches a ChartHook invoked after a remote user has
// been freshly created. nil 渡しは無効化と同義。
func (r *Resolver) SetChartHook(h ChartHook) {
	r.chartHook = h
}

// SetHashtagHook attaches a HashtagHook invoked after a remote note has
// been ingested or updated, so the hashtag table mentionedUsersCount /
// mentionedUserIds arrays stay in sync with /api/hashtags/list ranking
// (#680)。nil 渡しは無効化と同義。
func (r *Resolver) SetHashtagHook(h HashtagHook) {
	r.hashtagHook = h
}

// SetPublickeyRepo attaches a PublickeyStore for persistent public key
// storage. nil 渡しは無効化と同義 (in-memory only に戻る)。
func (r *Resolver) SetPublickeyRepo(repo PublickeyStore) {
	r.publickeyRepo = repo
}

// SignatureCapabilityDeclarer records that a remote host declared Ed25519
// support by publishing an assertionMethod Multikey (#2393). 実装は
// repository.InstanceSignatureCapabilityRepository。
type SignatureCapabilityDeclarer interface {
	RecordEd25519Declared(host string, at time.Time) error
}

// SetSignatureCapabilityDeclarer attaches the store that records per-host
// Ed25519 capability declarations (#2393). 未配線なら宣言は記録されない。
func (r *Resolver) SetSignatureCapabilityDeclarer(d SignatureCapabilityDeclarer) {
	r.capabilities = d
}

// SetPublickeyExtraRepo attaches a PublickeyExtraStore for persistent
// additional public key storage (FEP-521a Multikey / Ed25519). nil 渡しは
// 無効化と同義: ResolveActor は actor の assertionMethod[] を parse せず、
// PublicKeyForKeyID は user_publickey (RSA) のみを返す (#1067 / #1070)。
func (r *Resolver) SetPublickeyExtraRepo(repo PublickeyExtraStore) {
	r.publickeyExtraRepo = repo
}

// SetPollRepo attaches a PollRepository for ingesting Question (poll) objects.
func (r *Resolver) SetPollRepo(repo repository.PollRepository) {
	r.pollRepo = repo
}

// SetPinningRepo wires the pinned-notes store used to import a remote actor's
// featured collection (#2552). 未配線なら featured の取り込み自体を行わない。
func (r *Resolver) SetPinningRepo(repo repository.UserNotePiningRepository, idGen id.Generator) {
	r.pinningRepo = repo
	r.pinningIDGen = idGen
}

// PollVoter records a poll vote on behalf of a remote actor when an
// inbound AP Note carries `name` + `inReplyTo` to a poll-bearing note
// (Misskey TS の vote AP wire format)。実装は core/poll.Service。循環依存
// 回避のため interface で受け取る。nil 配線時は federation 経由 vote が
// 無視される (legacy / 元バグ挙動)。
type PollVoter interface {
	Vote(user *model.User, noteID string, choice int) error
}

// SetPollVoter attaches a PollVoter so AP votes are routed to the local
// poll service instead of being mistakenly persisted as reply notes (#690)。
func (r *Resolver) SetPollVoter(v PollVoter) {
	r.pollVoter = v
}

// SetEmojiRepo attaches an EmojiRepository for extracting and upserting
// remote custom emoji from AP Person/Note Tag arrays (#330).
func (r *Resolver) SetEmojiRepo(repo repository.EmojiRepository) {
	r.emojiRepo = repo
}

// SetImageProbeClient attaches an SSRF-safe *http.Client used by the
// attachment dimension probe (#461). The supplied client must wrap a
// transport with safehttp.NewSSRFSafeTransport(...) — otherwise a
// malicious remote can point a Document URL at internal addresses
// (cloud metadata, localhost services). nil 渡しは無効化と同義で、
// その場合 dimension probe はスキップされ properties は空のまま。
func (r *Resolver) SetImageProbeClient(client *http.Client) {
	r.imageProbeClient = client
}

// SetDriveFileRepo attaches a DriveFileRepository for ingesting AP
// `attachment` arrays into drive_file rows (#378). 未設定なら attachment
// は無視される (旧挙動)。
func (r *Resolver) SetDriveFileRepo(repo repository.DriveFileRepository) {
	r.driveFileRepo = repo
}

// SetHostBlockChecker attaches a federation-policy checker used to reject
// new fetches / ingests from hosts that the running instance does not
// federate with (federation: none / specified、blockedHosts)。未配線時は
// gate が無効化されて legacy 挙動 (全 host 許可) に倒れる。
func (r *Resolver) SetHostBlockChecker(c HostBlockChecker) {
	r.hostBlocker = c
}

// SilencedHostChecker reports whether a remote host is in meta.silencedHosts.
// *instance.Service implements it (IsSilenced).
type SilencedHostChecker interface {
	IsSilenced(host string) bool
}

// SetSilencedHostChecker attaches the meta.silencedHosts checker used to demote
// a silenced remote instance's public notes to home on ingest (#2106 N14).
// 未配線時は降格しない。
func (r *Resolver) SetSilencedHostChecker(c SilencedHostChecker) {
	r.silencedChecker = c
}

// hostAllowed reports whether the resolver may talk to / persist content
// authored on host. host == "" (= local) は常に true。未配線時も true。
func (r *Resolver) hostAllowed(host string) bool {
	if r.hostBlocker == nil || host == "" {
		return true
	}
	if r.hostBlocker.IsBlocked(host) {
		return false
	}
	return r.hostBlocker.IsAllowed(host)
}

// hostAllowedForURI parses uri and applies hostAllowed. URI 解析不可は別
// 経路で reject される前提で許容 (= true) する。
func (r *Resolver) hostAllowedForURI(uri string) bool {
	host, err := hostFromURI(uri)
	if err != nil {
		return true
	}
	return r.hostAllowed(host)
}

// PublicKeyForActor returns the cached public key PEM for an actor ID.
// in-memory → DB → miss の順で探索する。TTL超過は miss として扱い、呼び出し
// 側が ResolveActor を再実行することで refresh をトリガできる。
func (r *Resolver) PublicKeyForActor(actorID string) (string, error) {
	// 1. in-memory cache (TTL内)
	r.keysMu.RLock()
	entry, ok := r.keys[actorID]
	r.keysMu.RUnlock()
	if ok {
		if r.clock().Sub(entry.fetchedAt) <= r.actorTTL {
			return entry.pem, nil
		}
		r.keysMu.Lock()
		delete(r.keys, actorID)
		r.keysMu.Unlock()
	}
	// 2. DB fallback
	if r.publickeyRepo != nil {
		if pk, err := r.publickeyRepo.FindByUserID(actorID); err == nil {
			r.keysMu.Lock()
			r.keys[actorID] = publicKeyEntry{pem: pk.KeyPEM, fetchedAt: r.clock()}
			r.keysMu.Unlock()
			return pk.KeyPEM, nil
		}
	}
	return "", fmt.Errorf("public key for actor %q not cached", actorID)
}

// ResolveActor returns the local model.User row for a remote actor URI,
// fetching and creating it if necessary. 既存ユーザーであっても LastFetchedAt
// が actorTTL を超えていたら fetch しなおして name / inbox / sharedInbox /
// publicKey を更新する。fetch 失敗時はベストエフォートで既存値を返す。
func (r *Resolver) ResolveActor(uri string) (*model.User, error) {
	return r.resolveActor(uri, false, false, nil)
}

// ResolveActorAllowCrossHost is ResolveActor for user-initiated lookups
// (/api/ap/show) where upstream relaxes the request-url ↔ id binding to
// CrossOrigin softfail (ap/show.ts:153、#1828)。entry URI が admin の手入力で
// attacker 制御でないため cross-host redirect を許容する。finalURL ↔ id binding は
// 引き続き適用される。
func (r *Resolver) ResolveActorAllowCrossHost(uri string) (*model.User, error) {
	return r.resolveActor(uri, true, false, nil)
}

// resolveActor is ResolveActor carrying the cross-host-allowed flag (#1828)。
// resolveActorWithID resolves an actor, reusing a pre-assigned local ID when
// the row does not exist yet (#2332).
//
// ephemeral として採番した ID を materialize 後もそのまま使うために要る。
// ここを新規採番に任せると、ミュートで著者を materialize したときに ID が
// 変わり、Redis に残っている既存ノートが古い ID を指したままになる。結果と
// して **ミュートしたのにタイムラインから消えない** 状態が TTL 切れまで続く。
func (r *Resolver) resolveActorWithID(uri, preassignedID string) (*model.User, error) {
	key := crossHostKey(uri, false)
	chain := (*resolveChain)(nil).ensureTree()
	v, err := r.joinResolve(&r.resolveActorGroup, chain, actorWaitKey(key), key, func() (any, error) {
		return r.resolveActorOnceWithID(uri, false, preassignedID, false, false, false, chain)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, ErrInvalidActor
	}
	return v.(*model.User), nil
}

func (r *Resolver) resolveActor(uri string, allowCrossHost, skipFeatured bool, chain *resolveChain) (*model.User, error) {
	// 同一 URI への並行呼び出しは singleflight で 1 つに collapse する
	// (#300 3-7)。cache hit 経路は微秒なので serialize の影響は無視でき、
	// cold な fetch + Create で発生する HTTP fan-out + UNIQUE 衝突を抑える
	// 効果が大きい。
	key := actorGroupKey(uri, allowCrossHost, skipFeatured)
	chain = chain.ensureTree()
	v, err := r.joinResolve(&r.resolveActorGroup, chain, actorWaitKey(key), key, func() (any, error) {
		return r.resolveActorOnceWithID(uri, allowCrossHost, "", false, false, skipFeatured, chain)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, ErrInvalidActor
	}
	return v.(*model.User), nil
}

// resolveActorOnce is the body of resolveActor, invoked once per URI by
// resolveGroup.
func (r *Resolver) resolveActorOnceWithID(uri string, allowCrossHost bool, preassignedID string, ephemeral, viaRelay, skipFeatured bool, chain *resolveChain) (*model.User, error) {
	// fragment 付き URL は HTTP(S) で fragment が送られず正しく解決できないため
	// 拒否する (本家 Resolver の b94fd5b1 guard、#1828)。keyId fragment は
	// ResolveActorByKeyID -> ResolveKeyURL で除去済みなのでここには来ない。
	if strings.Contains(uri, "#") {
		return nil, ErrResolveFragment
	}
	if existing, err := r.userRepo.FindByURI(uri); err == nil {
		if r.shouldRefreshActor(existing) {
			r.refreshActor(existing, uri, skipFeatured, chain)
		} else {
			r.keysMu.RLock()
			_, cached := r.keys[existing.ID]
			r.keysMu.RUnlock()
			if !cached {
				// TTL 内であっても publicKey キャッシュが空 (再起動直後など)
				// なら取り直す。
				r.refreshPublicKey(existing.ID, uri)
			}
		}
		return existing, nil
	}

	actor, err := r.fetchActor(uri, allowCrossHost)
	if err != nil {
		return nil, err
	}

	host, err := hostFromURI(actor.ID)
	if err != nil {
		return nil, ErrInvalidActor
	}

	now := r.clock()
	user := &model.User{
		ID: pickID(preassignedID, r.idGen, now),
		// リレー経由で初めて観測した行に印を付ける (#2340)。孤児掃除の対象を
		// リレー由来に限定するために使う。
		Username:      actor.PreferredUsername,
		UsernameLower: strings.ToLower(actor.PreferredUsername),
		Host:          &host,
		URI:           &actor.ID,
		Inbox:         &actor.Inbox,
		IsBot:         activitypub.IsBotActorType(actor.Type.String()),
		// AP actorの manuallyApprovesFollowers を IsLocked (承認制) として
		// 取り込む。これが false だとリモートの承認制ユーザに対するフォロー
		// が非 locked として処理されて即 Following が成立し、ボタン挙動と
		// AP仕様が崩れる。
		IsLocked:      actor.ManuallyApproves.Bool(),
		LastFetchedAt: &now,
	}
	if name := remoteDisplayName(actor.Name.String()); name != "" {
		user.Name = &name
	}
	// remote actor の `_misskey_canChat` を chatScope に翻訳する (#692)。
	// CherryPick / 連合先は granular な scope (followers/following/mutual)
	// を AP に expose しないため、boolean (everyone/none) しか取れない。
	// chatScope が "none" / "everyone" のどちらかになることを保証することで、
	// chat service の canChat() が remote 相手でも正しく判定できる。
	//
	// 新規 fetch 時 (ここ) は flag 欠落 = everyone 扱い。`_misskey_canChat`
	// を export しない実装 (Mastodon / 古い Misskey / 一般的な AP 実装) は
	// そもそも chat 連合できないので、誤って "none" に倒すと local 側で
	// 送信前 reject されて UX が悪化するだけ。
	// refresh 経路 (refreshActor) は flag 欠落 = 既存値保持 (連合先 server が
	// フィールドを一時的に消した場合に scope を上書きしない安全策) で挙動が
	// 異なる点に注意。
	if actor.MisskeyCanChat != nil && !*actor.MisskeyCanChat {
		user.ChatScope = "none"
	} else {
		user.ChatScope = "everyone"
	}
	if shared := remoteSharedInbox(actor); shared != "" {
		user.SharedInbox = &shared
	}
	if actor.Featured != "" {
		featured := actor.Featured.String()
		user.Featured = &featured
	}
	if actor.MovedTo != "" {
		movedTo := actor.MovedTo.String()
		user.MovedToURI = &movedTo
		user.MovedAt = &now
	}
	if len(actor.AlsoKnownAs) > 0 {
		aka := strings.Join(actor.AlsoKnownAs, ",")
		user.AlsoKnownAs = &aka
	}
	// actor.icon / actor.image はそれぞれアバター / バナー画像。空 URL や
	// icon自体が欠落している actor (Service 系など) もあるので nil チェック。
	if avatarURL := remoteMediaURL(actor.ID, "user.avatarUrl", actor.Icon.URLOrEmpty(), maxRemoteAvatarURLLen); avatarURL != "" {
		user.AvatarURL = &avatarURL
	}
	if bannerURL := remoteMediaURL(actor.ID, "user.bannerUrl", actor.Image.URLOrEmpty(), maxRemoteBannerURLLen); bannerURL != "" {
		user.BannerURL = &bannerURL
	}
	// AP Person Tag配列からカスタム絵文字を抽出してDBにupsert
	user.Emojis = r.upsertEmojis(extractEmojiTags(actor.Tag), host)
	// upstream ApPersonService と同じく person.tag の Hashtag entry を user.tags に
	// 取り込む (hashtags/users の containment query 用)。summary text からは抽出しない。
	user.Tags = model.StringArray(hashtag.ExtractUserTags(extractHashtagTagNames(actor.Tag)...))
	if ephemeral {
		// リレー経由でしか観測しない著者は DB に入れない (#2332)。呼び出し元
		// (ingestNoteWithCreated) が PutNote でノートごと Redis に置く。
		//
		// 公開鍵は保存しない。リレー配送では Announce に署名するのがリレー
		// actor で、ノート本体は origin から HTTPS で取得するため著者の鍵を
		// 使う場面が無い。後で必要になれば (直接配送を受けた等) 通常経路の
		// ResolveActor が引き直して DB 行ごと作る。
		//
		// hashtag 集計 / user_profile / chart も DB 書き込みなので行わない。
		return user, nil
	}
	if err := r.userRepo.Create(user); err != nil {
		return nil, err
	}
	// リレー経由で初めて観測した行を記録する (#2340)。孤児掃除の対象をリレー
	// 由来に限定するために使う。**新規作成時のみ**。既に DB に在る行 (上の
	// FindByURI で返る) には付けないので、リレー購読前から居る行や
	// プロフィール閲覧で解決された行を巻き込まない。
	if viaRelay && r.relayObserved != nil {
		if oerr := r.relayObserved.MarkObserved(user.ID); oerr != nil {
			slog.Warn("federation: failed to record relay-observed user", "userId", user.ID, "err", oerr)
		}
	}
	// hashtag 集計 (attachedUsersCount) を新規 remote user の tags で更新する
	// (old=nil)。remote なので isLocal=false。non-blocking hook (#1362)。
	if r.hashtagHook != nil && len(user.Tags) > 0 {
		r.hashtagHook.UpdateUsertags(user.ID, false, nil, []string(user.Tags))
	}
	// remote actor の bio (= AP `summary` または `_misskey_summary`) を
	// `user_profile.description` に取り込む (#1022)。profile 行を作成しないと
	// 既存の Description 取得経路が常に NULL を返してしまい、frontend で
	// 自己紹介欄が空のまま表示される regression につながる。
	//
	// あわせて location / birthday / fields も取り込む (#2661)。送信側
	// (renderer) は vcard:Address / vcard:bday / PropertyValue を出していたが、
	// 受信側が description しか読んでおらず、リモートユーザーのプロフィール
	// 追加項目が常に空になっていた。
	hostCopy := host
	extras := extractRemoteProfileExtras(actor)
	profile := &model.UserProfile{
		UserID:      user.ID,
		Description: extractRemoteDescription(actor),
		Location:    extras.location,
		Birthday:    extras.birthday,
		Fields:      extras.fields,
		UserHost:    &hostCopy,
	}
	if err := r.userRepo.CreateProfile(profile); err != nil {
		// profile 作成失敗時は user 取り込みは成立させる (= 次回 refreshActor
		// で back-fill する経路を維持)。production race / fixture 衝突など
		// transient な失敗時に actor resolve 自体まで道連れにしない。
		slog.Warn("create remote user profile failed", "userId", user.ID, "err", err)
	}
	r.cachePublicKey(user.ID, actor.PublicKey.ID, actor.PublicKey.PublicKeyPEM.String())
	r.cacheAssertionMethods(user.ID, actor.ID, actor.AssertionMethod)
	r.notifyInstance(host)
	if r.chartHook != nil {
		r.chartHook.OnRemoteUserCreated(user)
	}
	// 観測を始めた時点で既にピン留めされていた投稿を取り込む (#2552)。inbound の
	// Add はピン留めされた瞬間にしか飛んで来ないので、これが無いと観測より前の
	// ピン留めは永久に拾えない。
	if !skipFeatured && !ephemeral {
		r.updateFeatured(user, chain)
	}
	return user, nil
}

// Column limits for the remote media URLs we copy onto the user row.
//
// `user.avatarUrl` は varchar(1024)、`user.bannerUrl` は varchar(512)。
// 超過値を渡すと `userRepo.Create` が SQLSTATE 22001 で落ち、**その actor が
// 1 行も作られない** (#2662)。URL は truncate すると壊れるだけなので落とす。
// upstream は Drive に取り込んで file id を持つので、長い URL でも user は
// 作られる。
const (
	maxRemoteAvatarURLLen = 1024
	maxRemoteBannerURLLen = 512
)

// remoteMediaURL returns the URL when it fits the target column, or "" when it
// does not (the actor is still imported, just without that image).
func remoteMediaURL(actorURI, column, raw string, max int) string {
	if raw == "" {
		return ""
	}
	// **NUL も落とす。** PostgreSQL の text は NUL を受け付けず (SQLSTATE 22021)、
	// 長さ超過と同じく **INSERT / UPDATE ごと**落ちる。create 経路では actor が
	// 1 行も作られず、refresh 経路では atomic UPDATE ごと失敗して
	// `lastFetchedAt` が進まないため、inbound activity 1 件につき outbound
	// fetch が 1 回走り続ける (#2662)。
	//
	// 除去ではなく破棄する。NUL を抜いた URL は別物で、取りに行っても無駄。
	if strings.ContainsRune(raw, 0) {
		slog.Warn("federation: dropping media url containing NUL",
			"uri", actorURI, "column", column)
		return ""
	}
	if len([]rune(raw)) > max {
		slog.Warn("federation: dropping oversized media url",
			"uri", actorURI, "column", column, "len", len([]rune(raw)), "max", max)
		return ""
	}
	return raw
}

// remoteSharedInbox picks the actor's shared inbox, preferring the top-level
// field like upstream's `x.sharedInbox ?? x.endpoints?.sharedInbox`.
//
// mk-go は endpoints しか見ていなかったので、top-level のみ publish する実装
// (#1560 で mk-go 自身が両方出しているとおり、実在の慣習) では shared inbox を
// 取りこぼしていた。取りこぼすと individual inbox へ落ちるだけなので配送は
// 成立するが、束ね配送が効かない (#2662)。
//
// host 検証は fetchActor で済ませてあるので、ここに来る値は actor と同一
// host か空。
func remoteSharedInbox(actor *activitypub.Person) string {
	if actor.SharedInbox != "" {
		return actor.SharedInbox.String()
	}
	return actor.Endpoints.SharedInbox.String()
}

// maxRemoteNameLen bounds the imported display name (`user.name`).
//
// upstream validateActor は `name` が truthy な string のとき
// `truncate(person.name, 128)` を通す (非 string なら throw、空文字なら
// undefined 化)。mk-go は素通ししていたので、128 文字を超える `name` を持つ
// actor は `user.name` (varchar(128)) の insert が SQLSTATE 22001 で落ち、
// **その actor がまったく作られなかった** (#2662)。NUL も 22021 で同じく落とす。
//
// upstream の `truncate` は `stringz.substring` = 書記素クラスタ単位だが、
// PostgreSQL の varchar はコードポイントで数えるので rune 単位で切る。
const maxRemoteNameLen = 128

// maxRemoteUsernameLen bounds `preferredUsername` (`user.username` /
// `usernameLower`)。upstream validateActor の `length <= 128` に対応する。
// 列長がたまたま同じだけで maxRemoteNameLen とは別の制約なので定数を分ける。
const maxRemoteUsernameLen = 128

// remoteDisplayName normalises a remote actor's display name for `user.name`.
func remoteDisplayName(name string) string {
	name = sanitizeRemoteText(name)
	if runes := []rune(name); len(runes) > maxRemoteNameLen {
		name = string(runes[:maxRemoteNameLen])
	}
	return name
}

// usernamePattern mirrors upstream validateActor's preferredUsername check
// (`/^\w([\w-.]*\w)?$/`). 1 文字の場合は先頭の `\w` だけで成立する。
var usernamePattern = regexp.MustCompile(`^\w([\w-.]*\w)?$`)

// validRemoteUsername reports whether preferredUsername can be stored and is
// shaped like upstream requires.
//
// `\w` は Go / JS とも ASCII `[0-9A-Za-z_]` なので、この pattern を通る値は
// 必ず ASCII。したがって長さを byte で数えても upstream の UTF-16 `.length`
// と一致する。
//
// `user.username` / `usernameLower` は varchar(128) NOT NULL。長すぎる値や NUL
// 入りの値は `userRepo.Create` 自体を落とし、**その actor がまったく作られない**
// (#2662)。upstream も同じ条件で `invalid Actor: wrong username` を投げるので、
// DB エラーではなく検証で弾く。
func validRemoteUsername(name string) bool {
	if name == "" || len(name) > maxRemoteUsernameLen {
		return false
	}
	return usernamePattern.MatchString(name)
}

// Limits applied when importing a remote actor's profile extras.
//
// **upstream は location を truncate していない**が、mk-go の
// user_profile.location は varchar(128) (migration/000001_initial.up.sql)。
// 超過値をそのまま渡すと insert / update ごと落ちて、同じ書き込みに乗っている
// description まで巻き添えになる。description が 2048 文字で切っているのと
// 同じ形で切る。
//
// fields の件数も upstream の analyzeAttachments には上限が無い。ローカルの
// i/update は maxItems: 16 なので、リモートも同じ上限に揃える。上限が無いと
// 任意件数を送り込める。
const (
	maxRemoteLocationLen = 128
	maxRemoteFields      = 16
)

// sanitizeRemoteText strips NUL bytes from a remote-supplied string.
//
// **NUL は書き込みごと落とす。** JSON の NUL エスケープは正当な入力で、Go の
// decoder は実 NUL バイトを作る。PostgreSQL の text 系列はこれを受け付けず
// (実測 SQLSTATE 22021 `invalid byte sequence for encoding "UTF8": 0x00`)、
// jsonb も拒否する (22P05)。
//
// **SQLSTATE は protocol mode で変わる。** 本番の `internal/db` は pgx の
// extended protocol なので 22021 だが、`internal/testutil` は
// `PreferSimpleProtocol: true` なので同じ入力が 08P01 (`invalid message
// format`) になる。運用ログを grep するときは 22021 の方。
//
// 同じ書き込みに乗っている description まで巻き添えになり、しかも create 経路
// では **user_profile 行が 1 行も作られない** (以後の refresh も同じ create を
// 繰り返して失敗し続ける)。長さの truncate と同じ理由で、ここで落とす。
//
// 適用先は「列へ生で行く可能性のある文字列」すべて。summary 経由の
// description は mfm.FromHTML が NUL を落とすが、**_misskey_summary は
// FromHTML を通らない** ので description も対象に含める。
func sanitizeRemoteText(s string) string {
	if !strings.ContainsRune(s, 0) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == 0 {
			return -1
		}
		return r
	}, s)
}

// apTypeOf returns an AP object's `type`, tolerating the array form.
//
// upstream の getApType は `type` が配列なら先頭要素を見る
// (`activitypub/type.ts` の getApType / isPropertyValue)。string 決め打ちに
// すると `"type": ["PropertyValue"]` を送る実装を取りこぼす。
func apTypeOf(m map[string]any) string {
	switch t := m["type"].(type) {
	case string:
		return t
	case []any:
		if len(t) == 0 {
			return ""
		}
		first, _ := t[0].(string)
		return first
	}
	return ""
}

// birthdayPattern matches the leading YYYY-MM-DD of a vcard:bday value.
// upstream ApPersonService は `person['vcard:bday']?.match(/^\d{4}-\d{2}-\d{2}/)`
// で先頭だけを取る (= 時刻付きでも日付部分を使う)。
var birthdayPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

// remoteProfileExtras holds the profile columns imported from a remote actor
// besides the description.
type remoteProfileExtras struct {
	location *string
	birthday *string
	fields   datatypes.JSON
}

// extractRemoteProfileExtras maps the actor's vcard / attachment properties to
// user_profile columns, mirroring upstream ApPersonService:
//
// **upstream との乖離**: 空文字 / 空白のみの vcard:Address は NULL にする
// (upstream はそのまま保存する)。ローカルの i/update と同じ正規化。
//
//	fields:   analyzeAttachments(person.attachment ?? [])
//	birthday: person['vcard:bday']?.match(/^\d{4}-\d{2}-\d{2}/)?.[0] ?? null
//	location: person['vcard:Address'] ?? null
func extractRemoteProfileExtras(actor *activitypub.Person) remoteProfileExtras {
	var out remoteProfileExtras
	if loc := strings.TrimSpace(sanitizeRemoteText(actor.VcardAddress.String())); loc != "" {
		if runes := []rune(loc); len(runes) > maxRemoteLocationLen {
			loc = string(runes[:maxRemoteLocationLen])
		}
		out.location = &loc
	}
	if bday := birthdayPattern.FindString(actor.VcardBday.String()); bday != "" {
		out.birthday = &bday
	}
	out.fields = extractRemoteFields(actor.Attachment)
	return out
}

// extractRemoteFields converts the actor's `attachment` array into the
// [{name, value}] shape stored in user_profile.fields.
//
// upstream analyzeAttachments は isPropertyValue (getApType が "PropertyValue"
// かつ name が string かつ value を持ち string) で絞り、value を fromHtml で
// MFM に変換する。value は Mastodon 等が HTML (`<a href=...>`) で送るため、
// 素通しすると frontend でタグがリテラル表示される (description と同じ問題)。
//
// **upstream との乖離**: trim して name / value のどちらかが空になる entry は
// 落とす。upstream の analyzeAttachments は trim も空排除もしないが、ローカルの
// i/update (core/user) が同じ正規化をしているので揃える。
//
// 返り値は常に non-nil。取り込むものが無ければ "[]" を返して、jsonb 列を
// null にしない (golden の fields は配列必須)。
func extractRemoteFields(attachments []any) datatypes.JSON {
	empty := datatypes.JSON([]byte("[]"))
	if len(attachments) == 0 {
		return empty
	}
	type field struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	out := make([]field, 0, min(len(attachments), maxRemoteFields))
	for _, raw := range attachments {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if apTypeOf(m) != "PropertyValue" {
			continue
		}
		name, nameOK := m["name"].(string)
		value, valueOK := m["value"].(string)
		if !nameOK || !valueOK {
			continue
		}
		name = strings.TrimSpace(sanitizeRemoteText(name))
		// FromHTML は末尾で TrimSpace 済み。NUL も x/net/html のトークナイザが
		// 落とすが、経路の前提に依存しないよう明示的に落とす。
		value = mfm.FromHTML(sanitizeRemoteText(value))
		if name == "" || value == "" {
			continue
		}
		out = append(out, field{Name: name, Value: value})
		if len(out) >= maxRemoteFields {
			break
		}
	}
	if len(out) == 0 {
		return empty
	}
	data, err := json.Marshal(out)
	if err != nil {
		return empty
	}
	return datatypes.JSON(data)
}

// extractRemoteDescription returns the actor's bio mapped to
// UserProfile.Description. upstream `ApPersonService` の logic と同じく
// `_misskey_summary` (MFM そのまま) を優先し、なければ AP `summary`
// (HTML、Mastodon 系は `<p>...</p>` で wrap して送ってくる) を `mfm.FromHTML`
// で MFM 変換してから保存する。生 HTML を保存すると frontend MFM render が
// `<p>` を escape してリテラル表示する drop-in regression を起こすため
// (#1140 で発覚)。varchar(2048) 制約を rune 単位で respect し、空は nil で表す。
//
// 注: truncation 順序は upstream と意図的に差をつけている。upstream は
// HTML 段階で truncate (mid-tag 切断の risk) してから htmlToMfm、本実装は
// FromHTML で MFM に縮約してから truncate する。後者の方が content 効率
// が良く、mid-tag 切断による壊れ HTML を parser に渡す経路が無い。
func extractRemoteDescription(actor *activitypub.Person) *string {
	var raw string
	if actor.MisskeySummary != nil && *actor.MisskeySummary != "" {
		raw = actor.MisskeySummary.String()
	} else if actor.Summary != "" {
		// AP summary は Mastodon / Pleroma 等が HTML で送ってくる仕様
		// (`<p>...</p>` ラップが典型)。MFM render を期待する frontend に
		// そのまま渡すと escape されてタグがリテラル表示される。
		raw = mfm.FromHTML(actor.Summary.String())
	}
	raw = sanitizeRemoteText(raw)
	if raw == "" {
		return nil
	}
	const maxLen = 2048
	if runes := []rune(raw); len(runes) > maxLen {
		raw = string(runes[:maxLen])
	}
	return &raw
}

// ForceResolveActor resolves an actor and always re-fetches the profile,
// bypassing the TTL cache. Move activityなどプロフィール更新が確実に必要な場合に使う。
func (r *Resolver) ForceResolveActor(uri string) (*model.User, error) {
	if existing, err := r.userRepo.FindByURI(uri); err == nil {
		r.refreshActor(existing, uri, false, nil)
		return existing, nil
	}
	return r.ResolveActor(uri)
}

// notifyInstance is a best-effort hook into the instance tracker. ベスト
// エフォートのため、失敗してもエラーは伝搬しない。
func (r *Resolver) notifyInstance(host string) {
	if r.instanceTracker == nil || host == "" {
		return
	}
	_, _ = r.instanceTracker.RegisterFromHost(host)
}

// shouldRefreshActor reports whether a cached user row is past its TTL and
// should be refetched.
func (r *Resolver) shouldRefreshActor(u *model.User) bool {
	if u == nil || u.LastFetchedAt == nil {
		return true
	}
	return r.clock().Sub(*u.LastFetchedAt) > r.actorTTL
}

// refreshActor refetches the remote actor document and updates mutable fields
// on the local user row. 失敗してもエラーは返さず (呼び出し側はベストエフォート
// で既存値を使う)、ログは呼び出し元側で残す。
func (r *Resolver) refreshActor(existing *model.User, uri string, skipFeatured bool, chain *resolveChain) {
	// background refresh は federation-loop 扱いで Strict (request host binding 有効)。
	actor, err := r.fetchActor(uri, false)
	if err != nil {
		// **document が恒久的に不正なら lastFetchedAt だけ進める。**
		// 進めないと shouldRefreshActor が永久に true のままで、その actor から
		// inbound activity が 1 件来るたびに outbound fetch が 1 回走り続ける。
		// 相手は同じ document を返すので自然回復しない (#2662 で
		// preferredUsername の検証を足したことで、検証前に取り込まれた既存行が
		// この状態になりうる)。
		//
		// 対象は **document の内容起因**のエラーだけ。ネットワーク断や 5xx で
		// 進めると、一時的な障害の間に TTL を空振りさせて更新が遅れる。
		// `ErrObjectHostMismatch` (inbox / publicKey.id の host 不一致など) も
		// 相手が document を直さない限り変わらないので同じ扱いにする。
		//
		// **副作用**: lastFetchedAt を進めるので、`DeleteOrphanRemoteUsers` の
		// 「graceDays 以上 fetch されていない」条件から外れ続ける。掃除対象は
		// relay 由来の孤児行だけなので影響は小さいが、恒久的に壊れている
		// actor はまさに掃除したい対象ではある。
		if errors.Is(err, ErrInvalidActor) || errors.Is(err, ErrObjectHostMismatch) {
			now := r.clock()
			if uerr := r.userRepo.UpdateUser(existing.ID, map[string]any{"lastFetchedAt": &now}); uerr != nil {
				slog.Warn("federation: lastFetchedAt backoff update failed",
					"userID", existing.ID, "error", uerr)
			} else {
				existing.LastFetchedAt = &now
			}
		}
		return
	}
	now := r.clock()
	fields := map[string]any{
		"lastFetchedAt": &now,
	}
	// actor type が変わるケース (Person ↔ Service など) を反映する。常に上書きする
	// (TS Misskey も同様)。
	isBot := activitypub.IsBotActorType(actor.Type.String())
	fields["isBot"] = isBot
	existing.IsBot = isBot
	// manuallyApprovesFollowers の切り替えも追従する (リモート側でlock/unlock
	// された場合にローカルの判定もずれないように)。
	fields["isLocked"] = actor.ManuallyApproves.Bool()
	existing.IsLocked = actor.ManuallyApproves.Bool()
	if name := remoteDisplayName(actor.Name.String()); name != "" {
		fields["name"] = &name
		existing.Name = &name
	}
	if actor.Inbox != "" {
		inbox := actor.Inbox
		fields["inbox"] = &inbox
		existing.Inbox = &inbox
	}
	if shared := remoteSharedInbox(actor); shared != "" {
		fields["sharedInbox"] = &shared
		existing.SharedInbox = &shared
	}
	if actor.Featured != "" {
		featured := actor.Featured.String()
		fields["featured"] = &featured
		existing.Featured = &featured
	}
	// movedAt は移行が「起きた」瞬間だけ打刻する。upstream
	// (ApPersonService.updatePerson の `moving` フラグ) と同じ判定で、
	// 無→有 と 有→別の値 のときだけ更新し、同じ移行先を宣言し続けている
	// actor の再取得では触らない。
	//
	// 以前は actor が movedTo を持っていれば毎回 now で打ち直していた。
	// movedAt はまだどこからも読まれていないので実害は出ていなかったが、
	// upstream が movedAt を時間窓の基準に使う以上 (移行直後 2h の import
	// 上限緩和 / 14 日の移行クールダウン)、打ち直すと窓が開き続けて機能
	// しなくなる。時間窓を実装する前にここを直しておく (#2412)。
	// 移行の検知に使う「更新前」の状態。existing はこの後 in-place で
	// 書き換わるので、判定に必要な値をここで退避する。
	prevMovedAt := existing.MovedAt
	movedThisRefresh := false
	if actor.MovedTo != "" {
		movedTo := actor.MovedTo.String()
		moving := existing.MovedToURI == nil || *existing.MovedToURI != movedTo
		fields["movedToUri"] = &movedTo
		existing.MovedToURI = &movedTo
		if moving {
			fields["movedAt"] = &now
			existing.MovedAt = &now
			movedThisRefresh = true
		}
	}
	// 移行先が消えた (actor.MovedTo == "") ケースは追わない。他フィールドと
	// 同じ「空値・欠落では既存値を温存する」規約に従う。
	//
	// upstream は `movedToUri: person.movedTo ?? null` で null に戻すが、
	// mk-go はここで意図的に divergence する。リモートが一時的に movedTo を
	// 欠いた actor JSON を返しただけでクリアすると、次の取得が「無→有」の
	// 遷移に見えて movedAt が打ち直され、上の修正が骨抜きになるため。
	// 移行の取り消しに追従できない代わりに、時間窓の基準を安定させる。
	if len(actor.AlsoKnownAs) > 0 {
		akaStr := strings.Join(actor.AlsoKnownAs, ",")
		fields["alsoKnownAs"] = &akaStr
		existing.AlsoKnownAs = &akaStr
	}
	// アバター / バナー画像のURLリモート側で変更された場合に追従する。
	// 他フィールドと同様、空値や欠落時は既存値を温存する (削除は追わない)。
	if avatarURL := remoteMediaURL(actor.ID, "user.avatarUrl", actor.Icon.URLOrEmpty(), maxRemoteAvatarURLLen); avatarURL != "" {
		fields["avatarUrl"] = &avatarURL
		existing.AvatarURL = &avatarURL
	}
	if bannerURL := remoteMediaURL(actor.ID, "user.bannerUrl", actor.Image.URLOrEmpty(), maxRemoteBannerURLLen); bannerURL != "" {
		fields["bannerUrl"] = &bannerURL
		existing.BannerURL = &bannerURL
	}
	// `_misskey_canChat` の更新を chatScope に反映 (#692)。flag 欠落時は
	// 既存値を保持する (連合先がフィールドを export していない場合に上書き
	// しないため)。
	if actor.MisskeyCanChat != nil {
		newScope := "everyone"
		if !*actor.MisskeyCanChat {
			newScope = "none"
		}
		fields["chatScope"] = newScope
		existing.ChatScope = newScope
	}
	// AP Person Tag配列からカスタム絵文字を抽出してDBにupsert
	if existing.Host != nil {
		emojis := r.upsertEmojis(extractEmojiTags(actor.Tag), *existing.Host)
		fields["emojis"] = emojis
		existing.Emojis = emojis
	}
	// person.tag の Hashtag entry を user.tags に追従させる (actor 更新時に
	// 自己紹介の hashtag が変わったら反映する。新規取り込みと同じ正規化)。
	oldTags := []string(existing.Tags)
	// ExtractUserTags は hashtag が無いと nil を返すが、nil の model.StringArray は
	// Updates() map 経由で SQL NULL になり user.tags (NOT NULL) 制約に違反する。
	// その場合 actor 更新 (emojis / name / lastFetchedAt 等を含む atomic UPDATE)
	// 全体が失敗するため空配列に倒して '{}' を書く。
	tags := model.StringArray(hashtag.ExtractUserTags(extractHashtagTagNames(actor.Tag)...))
	if tags == nil {
		tags = model.StringArray{}
	}
	fields["tags"] = tags
	existing.Tags = tags
	existing.LastFetchedAt = &now
	// UpdateUserエラーはベストエフォートで無視 (次回再試行される)
	_ = r.userRepo.UpdateUser(existing.ID, fields)
	// hashtag 集計を tags 差分で追従させる。remote なので isLocal=false。
	// non-blocking hook (#1362)。
	if r.hashtagHook != nil {
		r.hashtagHook.UpdateUsertags(existing.ID, false, oldTags, []string(tags))
	}
	// UserProfile の description (#1022) と location / birthday / fields (#2661)。
	// actor 側が変わった場合に追従する。profile 行が無いケース (= それぞれの
	// fix 以前に取り込まれた remote user) は back-fill する。Update / Create の
	// いずれも best-effort で fail しても user 更新は維持する。
	desc := extractRemoteDescription(actor)
	extras := extractRemoteProfileExtras(actor)
	if _, err := r.userRepo.FindProfileByUserID(existing.ID); err != nil {
		_ = r.userRepo.CreateProfile(&model.UserProfile{
			UserID:      existing.ID,
			Description: desc,
			Location:    extras.location,
			Birthday:    extras.birthday,
			Fields:      extras.fields,
			UserHost:    existing.Host,
		})
	} else {
		// fields は jsonb 列に string で渡す。**素の []byte を渡してはいけない**
		// (実測 SQLSTATE 22P02 で UPDATE ごと落ち、同じ書き込みの description も
		// 巻き添えになる)。datatypes.JSON は driver.Valuer を実装しているので
		// そのままでも書けるが、値の形を呼び出し側で確定させておく。
		_ = r.userRepo.UpdateProfile(existing.ID, map[string]any{
			"description": desc,
			"location":    extras.location,
			"birthday":    extras.birthday,
			"fields":      string(extras.fields),
		})
	}
	r.cachePublicKey(existing.ID, actor.PublicKey.ID, actor.PublicKey.PublicKeyPEM.String())
	r.cacheAssertionMethods(existing.ID, actor.ID, actor.AssertionMethod)
	if existing.Host != nil {
		r.notifyInstance(*existing.Host)
	}
	// 移行を検知したらフォロワー等の引き継ぎを起動する。DB 更新の後に置くのは、
	// 引き継ぎ処理が movedToUri の永続化済みを前提にするため。
	if movedThisRefresh {
		r.processRemoteMove(existing, prevMovedAt, nil, chain)
	}
	// 既に観測済みのユーザーはここでピン留めが埋まる (#2552)。actor の TTL が
	// 切れるたびに引き直すので、featured を後から公開した相手にも追従する。
	if !skipFeatured {
		r.updateFeatured(existing, chain)
	}
}

// remoteMoveCooldown is how long a remote account must wait between moves
// before mk-go will process another one. upstream ApPersonService は 14 日
// (「Mastodon のクールダウン期間は 30 日だが若干緩めに設定しておく」)。
const remoteMoveCooldown = 14 * 24 * time.Hour

// maxRemoteMoveChain bounds how many hops a move chain may traverse before we
// give up. upstream の `movePreventUris.length > 10` と同じ上限。
const maxRemoteMoveChain = 10

// processRemoteMove carries local followers (and blocks / mutes / roles / lists
// / antennas) of a moved remote account over to its destination.
//
// upstream ApPersonService.processRemoteMove 相当 (#2414)。src は movedToUri を
// 書き終えた直後の行で、prevMovedAt は今回の更新より前の movedAt。
//
// visited は移行チェーンを辿った URI の集合で、A→B→A のような循環と、無限に
// 続く連鎖を止める。最初の呼び出しでは nil を渡す。
//
// **best-effort。** 移行の検知は actor 更新の副作用なので、ここでの失敗が
// プロフィール更新そのものを巻き戻すことは無い。
func (r *Resolver) processRemoteMove(src *model.User, prevMovedAt *time.Time, visited map[string]bool, chain *resolveChain) {
	if r.moveProcessor == nil || src == nil {
		return
	}
	if src.MovedToURI == nil || *src.MovedToURI == "" {
		return
	}
	dstURI := *src.MovedToURI
	srcURI := ""
	if src.URI != nil {
		srcURI = *src.URI
	}
	// 自分自身への移行は無意味。upstream も同じ位置で弾く。
	if srcURI != "" && srcURI == dstURI {
		return
	}
	// クールダウン。前回の移行から 14 日経っていなければ処理しない。
	// **#2412 で movedAt を遷移時だけ打刻するようにしたのが前提。** 再取得の
	// たびに打ち直していた頃はこの判定が常に成立してしまう。
	if prevMovedAt != nil && src.MovedAt != nil &&
		src.MovedAt.Sub(*prevMovedAt) < remoteMoveCooldown {
		slog.Info("federation: skip remote move (cooldown)",
			"srcURI", srcURI, "dstURI", dstURI)
		return
	}
	if visited == nil {
		visited = map[string]bool{}
	}
	if len(visited) > maxRemoteMoveChain {
		slog.Warn("federation: skip remote move (chain too long)",
			"srcURI", srcURI, "dstURI", dstURI, "hops", len(visited))
		return
	}
	if visited[dstURI] {
		slog.Warn("federation: skip remote move (circular)",
			"srcURI", srcURI, "dstURI", dstURI)
		return
	}
	visited[srcURI] = true

	dst, err := r.userRepo.FindByURI(dstURI)
	if err != nil || dst == nil {
		// ローカルユーザーは user.uri を持たない (NULL) ので URI では引けない。
		// upstream fetchPerson と同じく、自インスタンスを指す URI は id に
		// 分解して DB を引く。**移行先が自インスタンス (= move-in 本来の形)
		// はこの経路でしか解決できない。**
		if r.urls != nil {
			if localID := r.urls.LocalUserIDFromURI(dstURI); localID != "" {
				if u, ferr := r.userRepo.FindByID(localID); ferr == nil && u != nil {
					dst = u
				}
			}
		}
	}
	if dst == nil {
		// 未知の移行先は取りに行く。ローカルを名乗る URI が DB に無い場合は
		// movedToUri が間違っているので、取りに行かず打ち切る (upstream の
		// 'failed: movedTo is local but not found')。
		if r.urls != nil && r.urls.IsLocalURI(dstURI) {
			slog.Warn("federation: remote move points at an unknown local URI",
				"srcURI", srcURI, "dstURI", dstURI)
			return
		}
		// **チェーンを引き継ぐ。** 落とすと移行先の featured 解決が、この
		// goroutine が既に握っている鍵を「他人のもの」と見なす。いまは待たずに
		// 諦めるのでピンを 1 件落とすだけで済むが、以前は待って止まっていた
		// (#2684 と同じ形)。
		//
		// **この待ちがあるので actor 側もグラフに載せている。** 互いを movedTo に
		// 指す 2 つの actor を 2 worker が同時に取得すると、ここで actor どうしの
		// 循環になる。
		dst, err = r.resolveActor(dstURI, false, false, chain)
		if err != nil || dst == nil {
			slog.Warn("federation: resolve remote move destination failed",
				"srcURI", srcURI, "dstURI", dstURI, "err", err)
			return
		}
	}

	if !r.moveDestinationAccepts(src, dst, srcURI, dstURI) {
		return
	}
	r.moveProcessor.PostMoveProcess(src, dst)
}

// moveDestinationAccepts reports whether dst really claims src as a previous
// identity, i.e. whether the migration is bidirectionally confirmed.
//
// **ここは security boundary。** `alsoKnownAs` の相互確認を省くと、任意の actor
// が movedTo で他人を指すだけでそのフォロワーを奪える。確認が取れない限り
// 引き継ぎに入らない。
func (r *Resolver) moveDestinationAccepts(src, dst *model.User, srcURI, dstURI string) bool {
	dstCanonical := ""
	if dst.URI != nil {
		dstCanonical = *dst.URI
	}
	// ローカルユーザーが移行先の場合、user.uri は空なので正規 URI を組み立てる。
	if dstCanonical == "" && dst.IsLocal() && r.urls != nil {
		dstCanonical = r.urls.UserURI(dst.ID)
	}
	if dstCanonical == "" || dstCanonical != dstURI {
		slog.Warn("federation: remote move destination URI mismatch",
			"srcURI", srcURI, "declared", dstURI, "resolved", dstCanonical)
		return false
	}
	// 移行先がさらに移行済みで、それが自分自身や移行元を指す場合は打ち切る。
	if dst.MovedToURI != nil && *dst.MovedToURI != "" {
		if *dst.MovedToURI == dstCanonical || (srcURI != "" && *dst.MovedToURI == srcURI) {
			slog.Warn("federation: remote move destination points back",
				"srcURI", srcURI, "dstURI", dstURI, "dstMovedTo", *dst.MovedToURI)
			return false
		}
	}
	if srcURI == "" {
		return false
	}
	if !alsoKnownAsContains(dst.AlsoKnownAs, srcURI) {
		slog.Warn("federation: remote move rejected (alsoKnownAs does not claim the source)",
			"srcURI", srcURI, "dstURI", dstURI)
		return false
	}
	return true
}

// alsoKnownAsContains reports whether the csv alsoKnownAs column lists uri.
// mk-go は upstream の配列を text csv として保持している (core/move と同じ表現)。
func alsoKnownAsContains(csvPtr *string, uri string) bool {
	if csvPtr == nil || *csvPtr == "" || uri == "" {
		return false
	}
	for _, part := range strings.Split(*csvPtr, ",") {
		if strings.TrimSpace(part) == uri {
			return true
		}
	}
	return false
}

// fetchActor fetches and decodes a remote actor document. Reject any
// document whose `type` is not in activitypub.ValidActorTypes — this guards
// against a non-Actor object (e.g. a Note) being interpreted as a Person.
func (r *Resolver) fetchActor(uri string, allowCrossHost bool) (*activitypub.Person, error) {
	// federation policy gate: ホワイトリスト連合 (federation: specified) や
	// blockedHosts 設定下では、対象 URI の host が許可されていなければ HTTP
	// fetch 自体を抑止する。refreshActor / refreshPublicKey / resolveActorOnce
	// すべてここを経由するため、actor 側はこの 1 箇所で塞げる。
	if !r.hostAllowedForURI(uri) {
		return nil, ErrHostNotAllowed
	}
	body, finalURL, err := r.fetchObjectWithFinalURL(uri)
	if err != nil {
		return nil, err
	}
	var actor activitypub.Person
	if err := json.Unmarshal(body, &actor); err != nil {
		return nil, ErrInvalidActor
	}
	// fetch した document が AS @context を持つことを要求する (本家 resolve の
	// invalid-response guard、#1828)。誤設定 endpoint が返す non-AP JSON を
	// actor として取り込むのを防ぐ。
	if !hasActivityStreamsContext(actor.Context) {
		slog.Warn("federation: actor missing ActivityStreams @context", "uri", uri)
		return nil, ErrInvalidActor
	}
	// **`id` も正規化する。** upstream は `assertActivityMatchesUrl` が
	// `new URL(activity.id)` を通すので、末尾改行のような形でも通る。ここで
	// 落とすと **その actor がまったく取り込めない** (#2662)。`id` は全 field の
	// 中で最も必ず存在する URL なので、inbox に改行を付ける実装は id にも付ける。
	actor.ID = trimWHATWGURL(actor.ID)
	if actor.ID == "" {
		return nil, ErrInvalidActor
	}
	// actor.ID host が body を返した host と一致するか検証 (object-spoofing 防止、
	// #1820)。以降 resolveActorOnce は actor.ID から host/URI を導出するため、
	// ここで bind しておかないと別 host になりすました actor が作られる。
	if err := assertResponseHostMatches(finalURL, actor.ID); err != nil {
		slog.Warn("federation: actor id host mismatch", "uri", uri, "finalURL", finalURL, "id", actor.ID)
		return nil, err
	}
	// request URI host == id host も要求する (Strict、#1828)。attacker.example の
	// entry URI から 302 で victim.example の actor 解決を誘発するのを防ぐ。
	// user-initiated ap/show (allowCrossHost) は upstream 同様 cross-host を許容。
	if !allowCrossHost {
		if err := assertRequestHostMatches(uri, actor.ID); err != nil {
			slog.Warn("federation: actor request host mismatch", "uri", uri, "id", actor.ID)
			return nil, err
		}
	}
	// **型の検査を username より先に置く。** upstream validateActor も
	// `isActor(x)` を最初に見る。逆順だと Note を actor として resolve した
	// ときに「preferredUsername が不正」と報告され、型ミスマッチを username
	// 問題と読み違える (#2662)。host 検証よりは後ろに置く。そちらは spoofing
	// 対策で、専用のエラーを返せなくなると調査に効く情報が減る。
	if !activitypub.IsValidActorType(actor.Type.String()) {
		// Note/Activity 等 Actor でない object が actor として参照された場合の
		// 防衛策。デバッグのため URI と type を残してから拒否する。
		slog.Warn("federation: rejecting non-actor type", "uri", uri, "type", actor.Type.String())
		return nil, ErrInvalidActor
	}
	// **配送に使う URL は正規化してから保存する。** 検査だけ `trimWHATWGURL` で
	// 緩めて生値を保存すると、actor は取り込めるのに `http.NewRequest` が毎回
	// 落ちて**配送が永久に成立しない** (末尾空白なら `%20` 付きの URL を叩いて
	// 相手が 404)。upstream は `new URL()` を通した値を使うので、そこに揃える。
	// deliver 側は SkipRetry を付けないので、1 人いるだけで全 activity が
	// MaxAttempts 回空振りする (#2662)。
	actor.Inbox = trimWHATWGURL(actor.Inbox)
	actor.SharedInbox = activitypub.APLenientID(trimWHATWGURL(actor.SharedInbox.String()))
	actor.Endpoints.SharedInbox = activitypub.APLenientID(trimWHATWGURL(actor.Endpoints.SharedInbox.String()))
	actor.PublicKey.ID = trimWHATWGURL(actor.PublicKey.ID)
	// inbox / sharedInbox の host を actor に縛る。upstream validateActor と同型
	// (前者は Error、後者は破棄)。ここを見ないと、任意のリモート actor が
	// 第三者ホストを配送先として宣言できてしまう (deliver は blocklist しか
	// 見ない)。
	if !sameDeliveryHost(actor.Inbox, actor.ID) {
		slog.Warn("federation: actor inbox host mismatch",
			"uri", uri, "inbox", actor.Inbox)
		return nil, ErrInvalidActor
	}
	if actor.SharedInbox != "" && !sameDeliveryHost(actor.SharedInbox.String(), actor.ID) {
		slog.Warn("federation: dropping shared inbox with different host",
			"uri", uri, "sharedInbox", actor.SharedInbox.String())
		actor.SharedInbox = ""
	}
	if actor.Endpoints.SharedInbox != "" && !sameDeliveryHost(actor.Endpoints.SharedInbox.String(), actor.ID) {
		slog.Warn("federation: dropping endpoints.sharedInbox with different host",
			"uri", uri, "sharedInbox", actor.Endpoints.SharedInbox.String())
		actor.Endpoints.SharedInbox = ""
	}
	// preferredUsername は user.username / usernameLower (varchar(128) NOT NULL)
	// にそのまま入る。upstream validateActor と同じ条件で弾く (#2662)。素通しすると
	// userRepo.Create が落ちて actor がまったく作られない。
	if !validRemoteUsername(actor.PreferredUsername) {
		slog.Warn("federation: actor has unusable preferredUsername", "uri", uri)
		return nil, ErrInvalidActor
	}
	// publicKey.id (= HTTP Signature keyId) host を actor.ID host に縛る (Fix A、
	// upstream validateActor の publicKey.id host check と同型)。**判定は
	// `sameDeliveryHost`**: upstream は `punyHost` で比較しており `www.` を
	// 同一視しない。`normalizeMatchHost` (object-host binding 用) を使うと `www.`
	// サブドメインを名乗る actor が親ドメインの keyId を宣言できてしまう
	// (#2662)。これが無いと攻撃者 actor が publicKey.id に victim
	// ドメインの keyId を載せて自分の RSA 鍵を植え込み、LD-Signature 経路の
	// global FindByKeyID 解決で victim を名乗る活動を verify 通過させられる
	// (key confusion / 連合認証バイパス)。allowCrossHost (ap/show) でも keyId は
	// actor 自身の host に縛る (request host とは独立、upstream expectHost と同じ)。
	if actor.PublicKey.ID != "" && !sameDeliveryHost(actor.PublicKey.ID, actor.ID) {
		slog.Warn("federation: actor publicKey.id host mismatch",
			"uri", uri, "id", actor.ID, "publicKeyID", actor.PublicKey.ID)
		return nil, ErrObjectHostMismatch
	}
	return &actor, nil
}

// keyFetchBackoff bounds how often we re-fetch an actor document solely to
// refill the in-memory public key cache after a failure.
//
// この経路は TTL 内でもキャッシュが空なら毎回走る。鍵が入らない actor
// (document が恒久的に不正 / `publicKey` を持たない / 相手が落ちている) では
// 抑制しないと inbound activity 1 件につき outbound fetch が 1 回走り続ける。
//
// **エラーの種類で絞らない。** `refreshActor` 側は `ErrInvalidActor` に絞って
// いるが、あちらは「lastFetchedAt を進める = TTL を空振りさせる」ので一時的な
// 障害で進めると更新が遅れる。こちらは単なるレート制限で、5 分後には必ず
// 再挑戦する。一時障害でも 5 分待つが、その間は `PublicKeyForActor` の
// DB fallback が効く。
const keyFetchBackoff = 5 * time.Minute

// refreshPublicKey fetches the actor again and caches its public key.
// エラーは上に伝搬せず、keyFetchBackoff に載せて次の機会に回す。
func (r *Resolver) refreshPublicKey(userID, uri string) {
	r.keysMu.RLock()
	failedAt, failed := r.keyFetchFailures[userID]
	r.keysMu.RUnlock()
	if failed && r.clock().Sub(failedAt) < keyFetchBackoff {
		return
	}
	// background refresh は federation-loop 扱いで Strict。
	actor, err := r.fetchActor(uri, false)
	if err != nil {
		r.markKeyFetchFailed(userID)
		return
	}
	// **fetch は成功したが鍵が読めない場合も backoff に載せる。** この経路は
	// 「TTL 内かつ in-memory 鍵キャッシュが空」なら毎回走るので、キャッシュに
	// 何も入らないまま成功扱いにすると inbound activity 1 件につき outbound
	// fetch が 1 回走り続ける (#2662)。空 PEM をキャッシュして黙らせるのは
	// 論外 (既存の鍵を壊す)。
	pem := actor.PublicKey.PublicKeyPEM.String()
	if pem == "" {
		r.markKeyFetchFailed(userID)
		r.cacheAssertionMethods(userID, actor.ID, actor.AssertionMethod)
		return
	}
	r.keysMu.Lock()
	delete(r.keyFetchFailures, userID)
	r.keysMu.Unlock()
	r.cachePublicKey(userID, actor.PublicKey.ID, pem)
	r.cacheAssertionMethods(userID, actor.ID, actor.AssertionMethod)
}

// markKeyFetchFailed records a failed key refresh so refreshPublicKey backs off.
func (r *Resolver) markKeyFetchFailed(userID string) {
	r.keysMu.Lock()
	defer r.keysMu.Unlock()
	if r.keyFetchFailures == nil {
		r.keyFetchFailures = make(map[string]time.Time)
	}
	// backoff を過ぎた entry は用済みなので落とす。上限はリモート user 数
	// なので暴走はしないが、消えないと単調増加する。
	now := r.clock()
	for id, at := range r.keyFetchFailures {
		if now.Sub(at) >= keyFetchBackoff {
			delete(r.keyFetchFailures, id)
		}
	}
	r.keyFetchFailures[userID] = now
}

// cachePublicKey stores a PEM in the in-memory cache and optionally persists
// it to the user_publickey table.
func (r *Resolver) cachePublicKey(userID, keyID, pem string) {
	// **空の PEM で既存の鍵を上書きしない。** 空を書くと in-memory も DB も
	// 空になり、`PublicKeyForActor` が空文字を返して **その actor からの
	// inbound HTTP Signature がすべて verify 失敗する**。refresh のたびに
	// 同じ空が書き直されるので自然回復しない。
	//
	// `publicKeyPem` を寛容に読むようにした (#2662) ことで、`{"@value": ...}`
	// のような形の document が「通るが値は空」で到達するようになった。それ以前
	// から `publicKey` 欠落でも同じ経路には入る。鍵が読めないなら**何もしない**
	// のが正しい (既存の鍵で verify を続けられる)。
	if pem == "" {
		// upstream の validateActor は `if (x.publicKey)` で publicKey を任意
		// 扱いにするので、鍵を持たない actor は不正ではない (Ed25519 のみを
		// publish する実装など)。Warn だと出続けるので Debug にする。
		slog.Debug("federation: refusing to cache empty public key",
			"userID", userID, "keyID", keyID)
		return
	}
	r.keysMu.Lock()
	r.keys[userID] = publicKeyEntry{pem: pem, fetchedAt: r.clock()}
	r.keysMu.Unlock()
	if r.publickeyRepo != nil && keyID != "" {
		if err := r.publickeyRepo.Upsert(&model.UserPublickey{
			UserID: userID,
			KeyID:  keyID,
			KeyPEM: pem,
		}); err != nil {
			slog.Warn("failed to persist public key", "userId", userID, "err", err)
		}
	}
}

// declareEd25519Capability records that actorURI's host publishes an Ed25519
// key. purge (key rotation で assertionMethod が消えた場合) では対になるクリアを
// 行わない: 値は host 単位で、同一 host の別 actor がまだ鍵を持っている可能性が
// あるため。「最後に確認した時刻」として保持し、古さの表現は UI 側に委ねる。
func (r *Resolver) declareEd25519Capability(actorURI string) {
	if r.capabilities == nil {
		return
	}
	// host は user.Host / instance.host と同じ導出 (hostFromURI) を使う。ここが
	// ずれると instance 一覧との突合が空振りしてラベルが出ない。
	host, err := hostFromURI(actorURI)
	if err != nil || host == "" {
		return
	}
	if err := r.capabilities.RecordEd25519Declared(host, time.Now()); err != nil {
		slog.Warn("ed25519 capability declaration persist failed",
			"host", host, "actorURI", actorURI, "error", err)
	}
}

// cacheAssertionMethods reconciles a remote actor's FEP-521a `assertionMethod[]`
// with our user_publickey_extra rows (#1067 / #1070):
//
//  1. 各 Multikey entry を decode して PEM 化 → user_publickey_extra に upsert
//  2. 既存 row のうち、今回の actor JSON に存在しない keyId は purge (delete)
//
// 2 が無いと remote が Ed25519 鍵を rotate した後も古い鍵が残り、攻撃者が
// compromised key で署名した activity が verify 通ってしまうので security
// 上必須。publickeyExtraRepo 未配線 (= 旧 deployment) のときは何もしない。
// 不正な entry (非 Multikey type / decode 失敗 / persist 失敗) は warn log
// + skip して RSA only にフォールバックする (fail-soft)。
func (r *Resolver) cacheAssertionMethods(userID, actorURI string, ams activitypub.MultikeyList) {
	if r.publickeyExtraRepo == nil {
		return
	}
	// **読めなかったときは purge しない。** 下の purge は「actor が申告しなかった
	// keyId を消す」= key rotation 追従だが、それが成立するのは actor の申告を
	// 正しく読めた場合だけ。読めない形 (string や壊れた entry) で空リストを
	// 渡されて purge すると、キャッシュ済みの Ed25519 鍵を全消しし、Ed25519 のみを
	// publish する相手では **inbound の署名検証が恒久的に失敗する** (相手は同じ
	// 形を返し続けるので自然回復しない、#2662)。
	if ams.Unreadable {
		slog.Warn("assertionMethod partially unreadable; skipping stale-key purge",
			"userID", userID, "actorURI", actorURI, "readable", len(ams.Keys))
	}
	// 1. 新 entries の upsert + 受領 keyId set を構築
	receivedKeyIDs := make(map[string]bool, len(ams.Keys)+len(ams.Refs))
	// 参照形式 (bare IRI) は鍵素材を持たないので upsert はできないが、
	// 「actor がその keyId を申告している」ことは分かるので purge から守る。
	for _, ref := range ams.Refs {
		ref = trimWHATWGURL(ref)
		if sameDeliveryHost(ref, actorURI) {
			receivedKeyIDs[ref] = true
		}
	}
	upserted := 0
	for _, am := range ams.Keys {
		// **検査だけでなく保存値も正規化する。** 生値のまま入れると HTTP
		// ヘッダ由来の keyId と一致しないゴミ行になり、しかも自分自身を
		// purge から守ってしまう (#2662)。
		am.ID = trimWHATWGURL(am.ID)
		if am.Type != activitypub.MultikeyType || am.ID == "" || am.PublicKeyMultibase == "" {
			continue
		}
		// keyId (am.ID) の host を actor の host に縛る (Fix B, upstream validateActor の
		// publicKey.id host check と同型)。これが無いと攻撃者 actor が assertionMethod に
		// victim ドメインの keyId を載せ、自分の Ed25519 鍵を victim の keyId で植え込んで
		// victim を名乗る署名を verify 通過させられる (key confusion / 連合認証バイパス)。
		// 検証鍵の lookup key は am.ID 自身 (PublicKeyForKeyID) なので、am.ID の host を
		// actor に縛れば植え込み経路は塞がる (controller は lookup に使われないため追加検証
		// は不要)。不正 entry は既存の decode 失敗時と同じく warn + skip (fail-soft、
		// actor 自体は取り込む)。
		if !sameDeliveryHost(am.ID, actorURI) {
			slog.Warn("assertionMethod keyId host mismatch",
				"userID", userID, "actorURI", actorURI, "keyID", am.ID)
			continue
		}
		pub, err := activitypub.DecodeEd25519Multikey(am.PublicKeyMultibase)
		if err != nil {
			slog.Warn("assertionMethod decode failed",
				"userID", userID, "keyID", am.ID, "error", err)
			continue
		}
		pemStr, err := activitypub.MarshalEd25519PublicKeyPEM(pub)
		if err != nil {
			slog.Warn("assertionMethod PEM marshal failed",
				"userID", userID, "keyID", am.ID, "error", err)
			continue
		}
		if err := r.publickeyExtraRepo.Upsert(&model.UserPublickeyExtra{
			UserID: userID,
			KeyID:  am.ID,
			KeyPEM: pemStr,
			Alg:    model.AlgEd25519,
		}); err != nil {
			slog.Warn("assertionMethod persist failed",
				"userID", userID, "keyID", am.ID, "error", err)
			continue
		}
		receivedKeyIDs[am.ID] = true
		upserted++
	}
	// この host が Ed25519 を expose していることを instance 単位で記録する
	// (#2393)。鍵が複数あっても host 単位の事実は 1 つなので、ループ内ではなく
	// ここで 1 回だけ書く。actor resolve は inbound ほど高頻度ではないので buffer は
	// 挟まない。best-effort なので失敗しても actor の取り込みは続ける。
	// **参照形式 (bare IRI) では宣言しない。** ref は purge 保護のために
	// receivedKeyIDs へ入れるが、鍵素材が無いので Ed25519 対応の根拠にならない
	// (FEP-521a の bare IRI は alg も分からない)。ref だけで宣言すると
	// `SupportsEd25519()` が「対応」を返すのに鍵が 1 本も無い状態になる (#2662)。
	if upserted > 0 {
		r.declareEd25519Capability(actorURI)
	}
	// 2. actor JSON に無い既存 keyId を purge (key rotation 対応)
	if ams.Unreadable {
		return
	}
	existing, err := r.publickeyExtraRepo.ListByUserID(userID)
	if err != nil {
		slog.Warn("assertionMethod list failed", "userID", userID, "error", err)
		return
	}
	for _, row := range existing {
		if receivedKeyIDs[row.KeyID] {
			continue
		}
		if err := r.publickeyExtraRepo.DeleteByKeyID(userID, row.KeyID); err != nil {
			slog.Warn("stale assertionMethod delete failed",
				"userID", userID, "keyID", row.KeyID, "error", err)
		}
	}
}

// PublicKeyForKeyID returns the PEM-encoded public key whose keyId matches the
// given fragment URI. user_publickey_extra (Ed25519 / Multikey) を最初に探し、
// miss なら user_publickey の primary key (RSA) を返す fallback semantics。
// 受信した HTTP Signature の keyId fragment (e.g. `#main-key` / `#ed25519-key`)
// から正しい公開鍵を選ぶための path で、in-memory cache は介さない (= 永続層
// の最新値で verify する)。
//
// 鍵は必ず actorID-scope で引く (FindByUserAndKeyID)。keyID 単独の global lookup に
// すると、攻撃者 actor が assertionMethod に victim の keyId を載せて自分の鍵を
// 植え込み、victim を名乗る署名を verify 通過させる key confusion (連合認証
// バイパス) が成立する。actor (= keyId base から解決した signer) に紐づく鍵だけを
// 許すことで、植え込まれた cross-actor 鍵を読取段でも排除する (Fix C, 二重防御)。
//
// `gorm.ErrRecordNotFound` (= keyId 一致なし = 通常状態) は silent fallback、
// それ以外の DB error は診断のため slog.Warn を出す (= silent degradation を
// 回避)。stale assertion key の削除は cacheAssertionMethods 側で actor fetch
// 時に diff & delete するため、ここでは古い行が引っかかる可能性は最小化される。
func (r *Resolver) PublicKeyForKeyID(actorID, keyID string) (string, error) {
	if r.publickeyExtraRepo != nil && keyID != "" {
		row, err := r.publickeyExtraRepo.FindByUserAndKeyID(actorID, keyID)
		if err == nil {
			return row.KeyPEM, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			slog.Warn("publickeyExtra lookup failed",
				"actorID", actorID, "keyID", keyID, "error", err)
		}
	}
	return r.PublicKeyForActor(actorID)
}

// ResolveActorByKeyID resolves an actor based on the keyId fragment URI.
func (r *Resolver) ResolveActorByKeyID(keyID string) (*model.User, error) {
	return r.ResolveActor(activitypub.ResolveKeyURL(keyID))
}

// ResolveNote returns the local model.Note row for a remote (or local) note
// URI, fetching and creating it if necessary.
//
//   - ローカル URI (urls.NoteURI のプレフィックスに一致) の場合は ID を抽出して
//     noteRepo.FindByID を試みる
//   - リモート URI で既に取り込み済みなら noteRepo.FindByURI で返す
//   - 未知のリモート URI なら fetcher で取得して IngestNote で永続化
func (r *Resolver) ResolveNote(uri string) (*model.Note, error) {
	return r.resolveNoteDepth(uri, 0, false, false, nil)
}

// ResolveNoteEphemeral is ResolveNote for relay-delivered notes: the note and
// its author are written to the ephemeral store instead of the database
// (#2332)。既に DB に在る URI はそのまま DB 行を返す (直接配送で来ていた等)。
//
// **検証は ResolveNote と完全に同一の経路を通る。** host binding / attribution
// forge / 可視性導出 / mention 上限はいずれも分岐しない。ephemeral 用に別の
// 構築経路を書くと検証が二重管理になり、リレー経由が検証を迂回する穴になる。
func (r *Resolver) ResolveNoteEphemeral(uri string) (*model.Note, error) {
	if r.ephemeralSink == nil || !r.ephemeralSink.Enabled() {
		return r.resolveNoteDepth(uri, 0, false, false, nil)
	}
	return r.resolveNoteDepth(uri, 0, false, true, nil)
}

// ResolveActorViaRelay resolves an actor and marks it as relay-derived when the
// row is created (#2340).
//
// 転送活動の LD-Signature 検証は creator の公開鍵を DB に載せる必要があるため
// (転送活動の唯一の認証手段)、この経路の著者は DB に残る。後追いの孤児掃除で
// 回収できるよう、リレー由来であることを記録しておく。
//
// 既に DB に在る行には印を付けない。リレー購読前から居る行や、プロフィール閲覧・
// スレッド遡りで解決された行を巻き込まないため。
func (r *Resolver) ResolveActorViaRelay(uri string) (*model.User, error) {
	key := crossHostKey(uri, false)
	chain := (*resolveChain)(nil).ensureTree()
	v, err := r.joinResolve(&r.resolveActorGroup, chain, actorWaitKey(key), key, func() (any, error) {
		return r.resolveActorOnceWithID(uri, false, "", false, true, false, chain)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, ErrInvalidActor
	}
	return v.(*model.User), nil
}

// ResolveActorEphemeral resolves an actor without persisting it when the
// ephemeral store is active (#2338)。Create 転送経由のリレー投稿の著者に使う。
func (r *Resolver) ResolveActorEphemeral(uri string) (*model.User, error) {
	if r.ephemeralSink == nil || !r.ephemeralSink.Enabled() {
		return r.ResolveActor(uri)
	}
	return r.resolveNoteAuthor(uri, true, 0, nil)
}

// IngestNoteEphemeral is IngestNoteWithCreated for relay-forwarded Create
// activities: the note and its author go to the ephemeral store instead of the
// database (#2338)。検証は通常経路と完全に同一。
func (r *Resolver) IngestNoteEphemeral(body []byte, deliveringActorURI string) (*model.Note, bool, error) {
	if r.ephemeralSink == nil || !r.ephemeralSink.Enabled() {
		return r.IngestNoteWithCreated(body, deliveringActorURI)
	}
	return r.ingestNoteWithCreated(body, deliveringActorURI, 0, true, nil)
}

// ResolveNoteAllowCrossHost is ResolveNote for user-initiated lookups
// (/api/ap/show) where upstream relaxes the request-url ↔ id binding to
// CrossOrigin softfail (#1828)。entry URI が attacker 制御でないため cross-host
// redirect を許容する。finalURL ↔ id binding は引き続き適用される。
func (r *Resolver) ResolveNoteAllowCrossHost(uri string) (*model.Note, error) {
	return r.resolveNoteDepth(uri, 0, true, false, nil)
}

// resolveNoteDepth is ResolveNote carrying the current recursion depth so the
// note/quote chain can be bounded (#1828)。quote 解決経路 (resolveQuoteURI) が
// depth+1 で再入する。allowCrossHost は user-initiated ap/show 経路でのみ true。
func (r *Resolver) resolveNoteDepth(uri string, depth int, allowCrossHost, ephemeral bool, chain *resolveChain) (*model.Note, error) {
	return r.resolveNoteDepthOpt(uri, depth, allowCrossHost, ephemeral, true, chain)
}

// resolveNoteBestEffort is resolveNoteDepth for callers that must not block on
// another chain's in-flight resolve of the same note.
//
// **best-effort の経路が待ちの辺を張ると、犠牲者の選ばれ方が壊れる。**
// featured の取り込みは失敗しても投稿本体には影響しないが、待つと (1) その間
// actor の鍵を握り続け、(2) その待ちが循環に見えたときに**本命の note の解決**が
// 代わりに弾かれる (#2685 review HIGH-2 / MEDIUM-2)。待たずに既存行へ落とせば、
// プロセス全体台帳だった頃と同じ挙動になる。
func (r *Resolver) resolveNoteBestEffort(uri string, depth int, chain *resolveChain) (*model.Note, error) {
	// **印は枝ごと引き継ぐ。** 相乗りする瞬間だけ待たない形にすると、自分が
	// 先頭になったときに内側 (著者 actor の解決) で待ってしまい、その間 actor の
	// 鍵を握り続ける (#2685 review round 4 で実測 1.2s)。
	return r.resolveNoteDepthOpt(uri, depth, false, false, false, chain.asBestEffort())
}

// resolveNoteDepthOpt is the body of both. mayWait=false makes the caller give
// up with ErrResolveWouldBlock instead of joining another chain's call.
//
// **mayWait=false を渡してよいのは featured の取り込みだけ。** この値は
// 「待たない」と「入口が featured」を兼ねており、resolveNoteOnce の取得後判定
// (#2695) をどこに効かせるかもこれで決まる。別の best-effort な呼び出し元を
// 足すなら、判定の入口を bool から分けること (#2710 review LOW-3)。
func (r *Resolver) resolveNoteDepthOpt(uri string, depth int, allowCrossHost, ephemeral, mayWait bool, chain *resolveChain) (*model.Note, error) {
	// 同一 URI への並行呼び出しは singleflight で collapse (#300 3-7)。
	// ResolveActor と同じく cold path の HTTP fetch + IngestNote を重複
	// 実行しないことが目的。
	//
	// ephemeral かどうかで書き込み先が変わるため singleflight key も分ける。
	// 混ざると片方の呼び出しが意図しない層へ書かれる。
	// **入った鍵をチェーンへ足す。** 入れ子の解決が「待つと自分を待つことに
	// なる」を判定できるようにする (#2684)。チェーンに閉じているので、
	// 無関係な goroutine が同じ鍵を握っていても巻き込まない (#2685)。
	key := noteGroupKey(uri, allowCrossHost, ephemeral)
	inner := chain.with(key, "")
	// **待つと循環する場合は諦める。** チェーンローカルの判定は自分の祖先しか
	// 見ないので、cross-goroutine の待ちの循環 (相互に引用し合う 2 投稿を
	// 2 worker が同時に解決する等) を防げない。待ちに上限が無いと循環した
	// 両方が永久に止まる (#2685 review HIGH-1)。
	//
	// uri が空だと key も空になり with が受け取ったものをそのまま返すので、
	// inner が nil のことがある。木の識別子はここで確定させる。
	inner = inner.ensureTree()
	v, err := r.joinResolveOpt(&r.resolveNoteGroup, inner, noteWaitKey(key), key, mayWait, func() (any, error) {
		return r.resolveNoteOnce(uri, depth, allowCrossHost, ephemeral, !mayWait, inner)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, ErrInvalidNote
	}
	return v.(*model.Note), nil
}

// chainAfterProbe records the resolved document identity on the chain, under
// both the key the caller entered with (the fetch URI) and the document id
// itself, so a nested resolve can find the row by either.
//
// **値は取得 URI ではなく document id。** 呼び出し側は取得 URI しか知らないが、
// note 行は document id で保存されるので、別名 URL では取得 URI で引くと必ず
// 空振りする (#2684 review MED-1)。
func chainAfterProbe(chain *resolveChain, uri string, allowCrossHost, ephemeral bool, docID string) *resolveChain {
	return chain.with(noteGroupKey(uri, allowCrossHost, ephemeral), docID).with(docID, docID)
}

// noteInFlightInChain reports whether the current resolve chain is already
// resolving or ingesting this note, and returns the document id it is being
// stored under when known.
//
// **待ってはいけないのは「自分の祖先が握っている」ときだけ。** チェーンに
// 閉じているので、無関係な goroutine が同じ note を扱っていても巻き込まない
// (#2685)。プロセス全体の台帳だった頃は後者も諦めていたため、別の worker が
// 同じ引用先を取り込んでいる最中に引用元が来ると renoteId を恒久的に
// 落としていた。
//
// 鍵は 2 種類ある。呼び出し側は取得 URI しか知らないことも、document id しか
// 知らないこともあるので両方を見る:
//
//   - singleflight の鍵 (`noteGroupKey` = 取得 URI)。resolveNoteDepth が入場時に足す
//   - 正規化後の document id (`apNote.ID`)。resolveNoteOnce と
//     ingestNoteWithCreated が足す。**inbox 直送でも立つ** (#2686)
//
// 別名 URL では取得 URI と document id が食い違うので、片方だけでは取りこぼす
// (#2684 review HIGH-1)。戻り値の docID は既存行を引き当てるのに使う。
func (r *Resolver) noteInFlightInChain(chain *resolveChain, uri string, allowCrossHost, ephemeral bool) (string, bool) {
	if docID, ok := chain.lookup(noteGroupKey(uri, allowCrossHost, ephemeral)); ok {
		if docID == "" {
			// fetch 前なので id が未確定。取得 URI で引くしかない。
			docID = uri
		}
		return docID, true
	}
	if docID, ok := chain.lookup(uri); ok {
		// document id 側で当たった。載せた側は id→id で入れるので値は uri と
		// 同じはずだが、写像の値をそのまま返すほうが取り違えない。
		//
		// **届かない防御。** ここに空値が入るには「祖先が非 ephemeral で
		// `uri → ""` を載せ、子孫が同じ uri を ephemeral で問い合わせる」が要るが、
		// ephemeral は親から引き継がれるだけで false から true へ転じず、
		// allowCrossHost は根でしか true にならない (#2710 review round 4 LOW-7)。
		if docID == "" {
			docID = uri
		}
		return docID, true
	}
	return "", false
}

// noteByAnyURI returns the stored row for uri, falling back to docID when the
// fetch URI and the document id differ (alias URLs).
func (r *Resolver) noteByAnyURI(uri, docID string) *model.Note {
	if n, err := r.noteRepo.FindByURI(uri); err == nil && n != nil {
		return n
	}
	if docID != "" && docID != uri {
		if n, err := r.noteRepo.FindByURI(docID); err == nil && n != nil {
			return n
		}
	}
	return nil
}

// resolveNoteOnce is the body of resolveNoteDepth, invoked once per URI by
// resolveGroup. depth は quote chain の現在の深さ。allowCrossHost は
// user-initiated ap/show 経路でのみ true。
func (r *Resolver) resolveNoteOnce(uri string, depth int, allowCrossHost, ephemeral, bestEffortEntry bool, chain *resolveChain) (*model.Note, error) {
	if r.noteRepo == nil {
		return nil, ErrInvalidNote
	}
	// fragment 付き URL は解決不能なため拒否 (本家 b94fd5b1 guard、#1828)。
	if strings.Contains(uri, "#") {
		return nil, ErrResolveFragment
	}
	// quote chain の再帰上限 (本家 d592da9f guard、#1828)。悪意ある深い
	// quote チェーンによる無限再帰・amplification を防ぐ。
	if depth > resolveRecursionLimit {
		return nil, ErrRecursionLimit
	}
	if id := r.extractLocalNoteID(uri); id != "" {
		if existing, err := r.noteRepo.FindByID(id); err == nil {
			return existing, nil
		}
		// ローカル URI なのにDBに無ければ、これ以上 fetch しても意味がない
		return nil, ErrInvalidNote
	}
	if existing, err := r.noteRepo.FindByURI(uri); err == nil {
		return existing, nil
	}
	// ephemeral 経路の URI 逆引き (#2397)。ingestNoteWithCreated と同じ理由で、
	// これが無いと Announce 等の解決経路から同じ投稿が別 ID で再取り込みされる。
	if ephemeral {
		if existing := r.ephemeralNoteByURI(uri); existing != nil {
			return existing, nil
		}
	}
	// federation policy gate: ホワイトリスト外 / blocked な host の note は
	// HTTP fetch も DB 永続化もしない。Announce 経由で第三者 host の note を
	// 渡されるケースを塞ぐ (handleAnnounce → ResolveNote)。既存行 (上の
	// FindByURI hit) は素通しで legacy 互換。
	if !r.hostAllowedForURI(uri) {
		return nil, ErrHostNotAllowed
	}
	body, finalURL, err := r.fetchObjectWithFinalURL(uri)
	if err != nil {
		return nil, err
	}
	// note.id host が body を返した host と一致するか検証 (object-spoofing 防止、
	// #1820)。IngestNote は apNote.ID をそのまま note.URI に採用するため、ここで
	// bind しておく。
	var idProbe struct {
		ID      string `json:"id"`
		Context any    `json:"@context"`
	}
	_ = json.Unmarshal(body, &idProbe)
	// fetch した document が AS @context を持つことを要求する (本家 resolve の
	// invalid-response guard、#1828)。inbound delivery (handleCreate → IngestNote)
	// で配送される inlined Note は @context を持たないことがあるためここでは
	// 検証せず、fetch した standalone document (= この resolveNoteOnce 経路) の
	// みに適用する。
	if !hasActivityStreamsContext(idProbe.Context) {
		slog.Warn("federation: note missing ActivityStreams @context", "uri", uri)
		return nil, ErrInvalidNote
	}
	// host 検証も正規化後の値で行う (#2662)。実際の取り込みは
	// ingestNoteWithCreated 側で改めて正規化する。
	idProbe.ID = trimWHATWGURL(idProbe.ID)
	if err := assertResponseHostMatches(finalURL, idProbe.ID); err != nil {
		slog.Warn("federation: note id host mismatch", "uri", uri, "finalURL", finalURL, "id", idProbe.ID)
		return nil, err
	}
	// request URI host == id host も要求する (Strict、#1828)。Announce / quote 等
	// attacker 制御の entry URI から cross-host redirect で第三者 note を解決させる
	// のを防ぐ。user-initiated ap/show (allowCrossHost) は cross-host を許容。
	if !allowCrossHost {
		if err := assertRequestHostMatches(uri, idProbe.ID); err != nil {
			slog.Warn("federation: note request host mismatch", "uri", uri, "id", idProbe.ID)
			return nil, err
		}
	}
	// **fetch 後にもう一度 in-flight を見る** (#2695)。手前の判定は取得 URI で
	// 引くので、featured collection が**別名 URL** を載せていると空振りする
	// (`https://h/@user/x` を取りに行って document の id が `https://h/notes/x`)。
	// id はここまで来ないと分からない。
	//
	// **featured の取り込みから入った呼び出しに限る。** ここで諦めると呼び出し側は
	// 「そのピンを 1 件落とす」だけで、次の actor 更新で拾い直せる。一方 quote 経路で
	// 諦めると `renoteId` が nil のまま保存され、再取り込みは FindByURI で早期
	// return するので**恒久的に失われる** (#2684 review R2 HIGH-1 と同じ型)。
	//
	// **判定に chain.mayWait() を使わない。** best-effort の印は枝ごと引き継がれる
	// ので、featured の取り込みの**内側**で走る引用解決も印を持ってしまい、
	// 上の「限定」が効かない。実測で、取り込み中の note を featured のピンが別名
	// URL で引用しているだけで renoteId が落ちた (#2710 review HIGH-1)。入口が
	// 何だったかは resolveNoteDepthOpt の mayWait が持っているので、それを渡す。
	//
	// **代償は created。** ピンが取り込み中の投稿を別名 URL で**引用**している形
	// (featured には正規 URI で載っている) では、その引用解決がここを素通りして
	// 内側で先に行を作るので、外側の Create は UNIQUE に当たり created=false の
	// まま。#2686 の通知欠落はその形では残る。renoteId の恒久的な欠落のほうが
	// 重いので、意図してそちらを取っている (#2710 review MEDIUM-1)。
	if bestEffortEntry {
		if ancestorID, ingesting := chain.ingestedDocID(idProbe.ID); ingesting {
			// 既に行があるならそれで足りる (取り込み済みのピンを落とさない)。
			if n := r.noteByAnyURI(uri, ancestorID); n != nil {
				return n, nil
			}
			return nil, ErrNoteAncestorIngesting
		}
	}
	// **入場した singleflight の鍵を、この区間だけ台帳に載せる** (#2684)。
	// singleflight は「この鍵が in-flight か」を外から問い合わせられないので、
	// 入れ子の解決が「待つと自分を待つことになる」を判定できない。
	//
	// fetch 経由は配送 actor が無いので attribution==actor 検証は skip ("")。
	// quote chain の depth を引き継いで再帰上限を効かせる。
	//
	// **区間を ingest に限る。** ここより手前 (FindByURI / ephemeral 逆引き /
	// HTTP fetch / host 検査 / 取得後の in-flight 判定) から resolveNoteDepth や
	// updateFeatured へ
	// 到達する経路は無いので、載せても再入は防げず**他の goroutine を
	// 取りこぼさせるだけ**になる。実際、singleflight 本体全体を覆うと
	// (1) 別 worker が同じ引用先を待てずに renoteId を落とし、
	// (2) 取り込み済みのピンが FindByURI 中に skip されて
	// ReplaceByUser に消される (#2684 review HIGH-1 / HIGH-2)。
	// **値に document id を持たせる。** 呼び出し側は取得 URI しか知らないが、
	// note 行は document id で保存される。別名 URL では両者が食い違うので、
	// 取得 URI だけで既存行を引くと**必ず空振りする** (#2684 review MED-1)。
	// fetch して id が確定したので、鍵にも id を紐づけ直す。別名 URL では
	// 取得 URI と食い違うので、両方から既存行を引けるようにしておく。
	resolved := chainAfterProbe(chain, uri, allowCrossHost, ephemeral, idProbe.ID)
	note, _, err := r.ingestNoteWithCreated(body, "", depth, ephemeral, resolved)
	return note, err
}

// resolveQuoteTarget resolves the quote target of an inbound note from its
// `_misskey_quote` / `quoteUrl` URIs (misskeyQuote takes precedence, matching
// upstream ApNoteService `note._misskey_quote ?? note.quoteUrl`). Returns nil
// when no quote URI is present or the target cannot be resolved (best-effort:
// an unresolvable quote degrades to a plain note rather than failing ingest).
//
// 既知 note (local ID / 取り込み済み URI) を fetch 無しで優先的に引く。未知 URI は
// ResolveNote で fetch するが、その URI が現在 ingest 中 (quote cycle) の場合は
// fetch を skip して無限再帰を防ぐ (#1527)。
func (r *Resolver) resolveQuoteTarget(misskeyQuote, quoteURL string, depth int, ephemeral bool, chain *resolveChain) *model.Note {
	if r.noteRepo == nil {
		return nil
	}
	// upstream ApNoteService は `[_misskey_quote, quoteUrl]` を順に解決し、最初に
	// 成功した note を採用する (`.at(0)`)。通常は両者同値だが、片方しか解決できない
	// ケースで取りこぼさないよう順に試す。
	if n := r.resolveQuoteURI(misskeyQuote, depth, ephemeral, chain); n != nil {
		return n
	}
	if quoteURL != misskeyQuote {
		if n := r.resolveQuoteURI(quoteURL, depth, ephemeral, chain); n != nil {
			return n
		}
	}
	return nil
}

// resolveQuoteURI resolves a single quote URI to a local note row. 既知 note
// (local ID / 取り込み済み URI) は fetch 無しで引き、未知 URI は cycle でなければ
// ResolveNote で fetch する。空 URI / 解決不能は nil。
func (r *Resolver) resolveQuoteURI(uri string, depth int, ephemeral bool, chain *resolveChain) *model.Note {
	if uri == "" {
		return nil
	}
	// 1. ローカル note URI なら ID 抽出して DB から (fetch 不要)。
	if id := r.extractLocalNoteID(uri); id != "" {
		if n, err := r.noteRepo.FindByID(id); err == nil {
			return n
		}
		return nil
	}
	// 2. 取り込み済みのリモート note なら DB から (fetch 不要)。
	if n, err := r.noteRepo.FindByURI(uri); err == nil {
		return n
	}
	// 2.5. ephemeral 側に既にあれば再 fetch しない (#2397)。これが無いと引用先を
	// 取り込み直して別 ID で保存し、renoteId が指す先が投稿ごとにずれる。
	if ephemeral {
		if n := r.ephemeralNoteByURI(uri); n != nil {
			return n
		}
	}
	// 3. 未知 URI: cycle でなければ fetch して取り込む (本家同様)。**自分の
	// 祖先が既に扱っている URI** は already-resolved として skip し quote cycle を
	// 防ぐ (#1527、本家 0dc86cf6 guard 相当)。depth+1 で再帰上限も効かせる。
	//
	// 判定はチェーンに閉じているので、無関係な goroutine が同じ引用先を
	// 取り込んでいる最中は**待って引ける** (#2685)。
	if docID, inflight := r.noteInFlightInChain(chain, uri, false, ephemeral); inflight {
		// 別名 URL で既に取り込み済みなら、その行で引用を繋げる。
		if n := r.noteByAnyURI(uri, docID); n != nil {
			return n
		}
		// ephemeral 側も同じ理由で document id で引き直す (2.5 は取得 URI だけ)。
		if ephemeral && docID != "" && docID != uri {
			if n := r.ephemeralNoteByURI(docID); n != nil {
				return n
			}
		}
		return nil
	}
	// quote 解決は federation-loop 扱いで Strict (request host binding 有効)。
	// ephemeral は親から引き継ぐ (引用先だけ DB に落ちるのを防ぐ)。
	if n, err := r.resolveNoteDepth(uri, depth+1, false, ephemeral, chain); err == nil {
		return n
	}
	return nil
}

// IngestNote parses an ActivityStreams Note JSON and persists it as a local
// row authored by the (resolved) attributedTo actor. 既に同じ URI の note を
// 取り込み済みなら既存レコードを返す。
//
// `created` フラグが必要な caller (e.g. processor.handleCreate の chart hook 発火
// 判定) は IngestNoteWithCreated を直接呼ぶこと。
func (r *Resolver) IngestNote(body []byte) (*model.Note, error) {
	// fetch 経由は配送 actor が無いので attribution==actor 検証は skip ("")。
	// id host == attributedTo host 検証は IngestNoteWithCreated 側で常に行う。
	note, _, err := r.ingestNoteWithCreated(body, "", 0, false, nil)
	return note, err
}

// IngestNoteWithCreated is the same as IngestNote but also returns whether
// the note was freshly persisted (`created == true`) or returned from the
// dedup cache (`created == false`). Caller can gate chart hooks on `created`
// so non-idempotent counters such as PerUserNotesChart are not double-applied
// when the same Create activity is delivered twice (#1156).
//
// Return value matrix:
//
//   - fresh INSERT (Create succeeded)           → (note,     true,  nil)
//   - dedup hit (existing row matched by URI)   → (existing, false, nil)
//   - AP poll vote (processed as a Vote)        → (nil,      false, nil)
//   - validation / ingest / actor resolve error → (nil,      false, err)
//
// Note that the dedup-hit case is the only path that returns a non-nil note
// with `created == false`; callers MUST check `created` before firing any
// non-idempotent hook (chart counters, notification, etc.) since the dedup
// path otherwise looks indistinguishable from a fresh ingest at the call site.
// IngestNoteWithCreated は note body を取り込み、新規作成かどうかも返す。
// deliveringActorURI は inbound Create(Note) の配送 actor URI で、note の著者
// (attributedTo) が配送者本人であることの検証に使う (なりすまし防止、#1839)。
// fetch 経由 (IngestNote) では空文字を渡し、この検証を skip する (upstream
// validateNote が actor 未指定時に attribution==actor を skip するのと同じ)。
func (r *Resolver) IngestNoteWithCreated(body []byte, deliveringActorURI string) (*model.Note, bool, error) {
	// inbound delivery 起点は quote chain depth 0。
	return r.ingestNoteWithCreated(body, deliveringActorURI, 0, false, nil)
}

// ingestNoteWithCreated is the body of IngestNoteWithCreated carrying the
// current note/quote-chain recursion depth (#1828)。
func (r *Resolver) ingestNoteWithCreated(body []byte, deliveringActorURI string, depth int, ephemeral bool, chain *resolveChain) (*model.Note, bool, error) {
	if r.noteRepo == nil {
		return nil, false, ErrInvalidNote
	}
	var apNote activitypub.Note
	if err := json.Unmarshal(body, &apNote); err != nil {
		return nil, false, ErrInvalidNote
	}
	// actor と同じ理由で URL 系を正規化する。`note.uri` にそのまま入るので、
	// 生値だと後続の `FindByURI` / host 検証 / 配送先解決が全部ずれる (#2662)。
	apNote.ID = trimWHATWGURL(apNote.ID)
	apNote.AttributedTo = activitypub.APLenientID(trimWHATWGURL(apNote.AttributedTo.String()))
	if apNote.ID == "" || apNote.AttributedTo == "" {
		return nil, false, ErrInvalidNote
	}
	if existing, err := r.noteRepo.FindByURI(apNote.ID); err == nil {
		return existing, false, nil
	}
	// ephemeral 経路では DB の miss が「未取り込み」を意味しないので URI 逆引きも
	// 引く (#2397)。非 ephemeral では引かない: 直接配送で DB 行を作る側は
	// dropSupersededEphemeral で ephemeral 側を落として上書きするのが正しい。
	if ephemeral {
		if existing := r.ephemeralNoteByURI(apNote.ID); existing != nil {
			return existing, false, nil
		}
	}
	// upstream ApNoteService.validateNote 相当の attribution 検証 (#1839):
	//   1. note の id host と著者 (attributedTo) host が一致すること
	//      (別 host の著者を詐称する cross-host forge を弾く)。
	//   2. inbound Create では note の著者が配送 actor 本人であること
	//      (alice@a が bob@b になりすました note を作る forge を弾く)。
	idHost, idErr := hostFromURI(apNote.ID)
	attrHost, attrErr := hostFromURI(apNote.AttributedTo.String())
	// idna 正規化して Unicode/punycode mixed-form の同一 host を誤 reject しない (#1850)。
	if idErr != nil || attrErr != nil || punyHost(idHost) != punyHost(attrHost) {
		slog.Warn("federation: note id/attributedTo host mismatch", "id", apNote.ID, "attributedTo", apNote.AttributedTo)
		return nil, false, ErrNoteAttributionMismatch
	}
	if deliveringActorURI != "" && apNote.AttributedTo.String() != deliveringActorURI {
		slog.Warn("federation: note attribution does not match delivering actor", "attributedTo", apNote.AttributedTo, "actor", deliveringActorURI)
		return nil, false, ErrNoteAttributionMismatch
	}
	// quote cycle 遮断と featured の自己参照遮断は resolveChain が担う (#2685)。
	// ここで chain へ足しておくと、**inbox 直送 (resolveNoteOnce を通らない経路)
	// でも立つ** (#2686)。resolveNoteOnce から来た場合は既に入っているので
	// 二重には増えない。
	chain = chain.with(apNote.ID, apNote.ID)
	// federation policy gate: 直接 handleCreate から受け取った body や、
	// 中継経由で attributedTo が allowlist 外の host を指している payload を
	// DB 永続化させない。既存 row hit (上の FindByURI) は素通しで legacy
	// 互換を維持する。
	if !r.hostAllowedForURI(apNote.AttributedTo.String()) {
		return nil, false, ErrHostNotAllowed
	}
	actor, err := r.resolveNoteAuthor(apNote.AttributedTo.String(), ephemeral, depth, chain)
	if err != nil {
		return nil, false, err
	}
	// 遅延配送 / origin downtime 後の bulk push などで note が遅れて届くケース
	// では AP の `published` を採用しないと timeline 上の並びが受信時刻順に
	// なってしまい remote とずれる (#940)。time-based ID (aidx 等) なので
	// idGen.Generate に published を渡せば自動的に tl 並びも remote 一致する。
	now := parseAPPublishedTime(apNote.Published.String(), time.Now())
	noteURI := apNote.ID
	note := &model.Note{
		ID:         r.idGen.Generate(now),
		UserID:     actor.ID,
		UserHost:   actor.Host,
		URI:        &noteURI,
		Visibility: deriveVisibility(apNote.To, apNote.CC),
	}
	// #2106 N14: silenced instance (meta.silencedHosts) の remote public note は home に
	// 降格する (upstream NoteCreateService.ts:491-495 の inSilencedInstance 降格)。public
	// timeline / 連合 broadcast から外す admin moderation 機能。silencedChecker 未配線時は
	// 降格しない (legacy 挙動)。
	if note.Visibility == model.NoteVisibilityPublic && actor.Host != nil && *actor.Host != "" &&
		r.silencedChecker != nil && r.silencedChecker.IsSilenced(*actor.Host) {
		note.Visibility = model.NoteVisibilityHome
	}
	// TS本家ApNoteService.tsと同じ3段フォールバックでtext抽出:
	// 1. source.content (MFM) → 2. _misskey_content → 3. content (HTML→MFM変換)
	if apNote.Source != nil &&
		apNote.Source.MediaType == "text/x.misskeymarkdown" &&
		apNote.Source.Content != "" {
		text := apNote.Source.Content
		note.Text = &text
	} else if apNote.MisskeyContent != "" {
		text := apNote.MisskeyContent.String()
		note.Text = &text
	} else if apNote.Content != "" {
		text := mfm.FromHTML(apNote.Content)
		if text != "" {
			note.Text = &text
		}
	}
	if apNote.Summary != "" {
		summary := apNote.Summary.String()
		note.CW = &summary
	}
	if apNote.Sensitive && note.CW == nil {
		// Sensitive かつ Summary が無いケース: 空文字 CW を設定して NSFW を表現
		empty := ""
		note.CW = &empty
	}
	// 返信先がローカルに存在すれば紐付ける。リモート返信先の解決は後続 phase で
	// 対応するため、現状では nil のままにする。
	var replyTarget *model.Note
	if apNote.InReplyTo != "" {
		// inReplyTo も同じ (#2662)。生値だと返信チェーンが繋がらない。
		apNote.InReplyTo = activitypub.APLenientID(trimWHATWGURL(apNote.InReplyTo.String()))
		if id := r.extractLocalNoteID(apNote.InReplyTo.String()); id != "" {
			if reply, err := r.noteRepo.FindByID(id); err == nil {
				replyTarget = reply
			}
		} else if reply, err := r.noteRepo.FindByURI(apNote.InReplyTo.String()); err == nil {
			replyTarget = reply
		}
		if replyTarget != nil {
			note.ReplyID = &replyTarget.ID
			note.ReplyUserID = &replyTarget.UserID
			note.ReplyUserHost = replyTarget.UserHost
			// thread-muting が深い thread でも効くよう threadId を thread root に
			// 揃える (local create path #1928 と同 logic、upstream NoteCreateService:
			// 623-627: threadId = data.reply.threadId ?? data.reply.id)。AP 経由で
			// 取り込んだ remote reply にも適用しないと remote thread の deep muting が
			// 効かない (#1931)。
			threadID := replyTarget.ID
			if replyTarget.ThreadID != nil && *replyTarget.ThreadID != "" {
				threadID = *replyTarget.ThreadID
			}
			note.ThreadID = &threadID
			// reply 先より広い可視性にはできない (upstream 2026.7.0 #17747)。
			// upstream の ApNoteService は NoteCreateService.create を経由する
			// ため、リモート発の reply にも同じクランプが掛かる。これが無いと
			// 「ローカルの followers 限定 note へリモートが to:[Public] で返信」
			// でローカル TL / 再配送に本文文脈が漏れる。
			note.Visibility = corenote.ClampVisibilityForReply(replyTarget.Visibility, note.Visibility)
		}
	}
	// AP vote 判定: reply target が poll を持ち apNote.Name (choice 名) が
	// 入っていれば「投票」として処理し、note は作らずに早期 return する
	// (#690)。Misskey TS の ApNoteService.create と同等の wire format。
	// pollRepo / pollVoter 未配線環境では従来通り reply note として fall
	// through する (legacy 互換)。
	if replyTarget != nil && replyTarget.HasPoll && apNote.Name != "" && r.pollRepo != nil && r.pollVoter != nil {
		if poll, err := r.pollRepo.FindByNoteID(replyTarget.ID); err == nil && poll != nil {
			idx := -1
			for i, c := range poll.Choices {
				if c == apNote.Name.String() {
					idx = i
					break
				}
			}
			if idx >= 0 {
				if err := r.pollVoter.Vote(actor, replyTarget.ID, idx); err != nil {
					slog.Warn("federation: AP poll vote rejected",
						"actor", actor.ID, "noteId", replyTarget.ID, "choice", apNote.Name, "err", err)
				}
				// vote として処理した場合、reply note は作成せず終了
				// (note=nil で caller の fanout / notification も skip される)。
				return nil, false, nil
			}
			slog.Warn("federation: AP poll vote choice not found",
				"actor", actor.ID, "noteId", replyTarget.ID, "choice", apNote.Name)
		}
	}
	// メンション抽出は本文と AP `tag` 配列の Mention 両方から行う。本文だけだと
	// specified DM (本文に @ が無いケース) で受信者を取りこぼす (#397)。
	// 本文 @username / tag href のいずれも最終的に user ID へ解決して保存する
	// (mentions 列はローカル create 経路と同じ user ID の配列にする)。
	var textMentions []string
	if note.Text != nil {
		textMentions = r.resolveTextMentionUserIDs(corenote.ExtractMentionStructs(*note.Text))
	}
	tagHrefs := extractMentionTags(apNote.Tag)
	tagMentions := r.resolveMentionedUserIDs(tagHrefs)
	note.Mentions = mergeMentionIDs(textMentions, tagMentions)
	// upstream Misskey #17167 (= 2026.5.0 fix / triage #1004): mentionLimit を
	// 超える note は無効と扱い、保存せずに ErrContainsTooManyMentions を返す。
	// caller (processor.handleCreate) が当該 sentinel を catch して queue retry
	// 経路から除外することで、罠の inbox job が永続蓄積するのを防ぐ。
	// upstream #17576: 制限判定は「解決できたユーザー数」(= len(note.Mentions)) でなく、
	// remote が宣言した raw mention 数 (AP tag の Mention href ユニーク数) との max で
	// 行う。一部しか解決できなくても大量 mention をすり抜けさせない。limit 値は
	// corenote.DefaultMentionLimit (= 20)。local create path は role policy の
	// 値を優先するようになったが (#2321)、こちらはリモートユーザーが対象で
	// ローカルの role を持たないため既定値のみで判定する。
	rawTagSet := make(map[string]struct{}, len(tagHrefs))
	for _, h := range tagHrefs {
		rawTagSet[h] = struct{}{}
	}
	effectiveMentions := len(note.Mentions)
	if len(rawTagSet) > effectiveMentions {
		effectiveMentions = len(rawTagSet)
	}
	if corenote.DefaultMentionLimit > 0 && effectiveMentions > corenote.DefaultMentionLimit {
		return nil, false, corenote.ErrContainsTooManyMentions
	}
	// specified visibility では AP `to` 配列が宛先 actor URI 列。CanView の
	// VisibleUserIDs チェック (core/note/visibility.go) で受信者が note を
	// 参照できるよう、ここで ID へ解決して埋める (#397)。
	if note.Visibility == model.NoteVisibilitySpecified {
		visible := r.resolveMentionedUserIDs(apNote.To)
		// reply target を必ず含める (upstream NoteCreateService.ts:603-605、
		// ローカル create 経路の #2106 N13 と同型)。特に #17747 の clamp で
		// `to:[Public]` の reply を specified へ降格させた場合、apNote.To から
		// は宛先が 1 件も解決できず VisibleUserIDs が空になり、reply 先の当人
		// (= specified note の作者) すら本文を参照できなくなる。
		if replyTarget != nil && replyTarget.UserID != note.UserID {
			alreadyVisible := false
			for _, id := range visible {
				if id == replyTarget.UserID {
					alreadyVisible = true
					break
				}
			}
			if !alreadyVisible {
				visible = append(visible, replyTarget.UserID)
			}
		}
		note.VisibleUserIDs = model.StringArray(visible)
	}
	// AP Note Tag配列からカスタム絵文字を抽出してDBにupsert
	if actor.Host != nil {
		note.Emojis = r.upsertEmojis(extractEmojiTags(apNote.Tag), *actor.Host)
	}
	// hashtag は AP `tag` 配列の Hashtag entry と本文 / CW の両方から拾い、
	// hashtag.ExtractNoteTags で case-insensitive dedup + 件数 cap + 長さ判定を
	// 一括処理する (#679)。長すぎる tag は truncate ではなく **drop** する。`tag` 配列が空 / Hashtag を含まない実装 (古い Mastodon
	// 等) でも本文 fallback で trends 集計に乗るようにする。
	hashtagSources := extractHashtagTagNames(apNote.Tag)
	if note.Text != nil {
		hashtagSources = append(hashtagSources, *note.Text)
	}
	if note.CW != nil {
		hashtagSources = append(hashtagSources, *note.CW)
	}
	// note.tags は upstream と同じく normalizeForSearch (NFKC+lowercase) + 128drop
	// + 32cap で正規化して格納する (#1948-18)。
	if tags := hashtag.ExtractNoteTags(hashtagSources...); len(tags) > 0 {
		note.Tags = model.StringArray(tags)
	}
	// AP `attachment` 配列を drive_file 行に upsert (#378)。link 形式のみで
	// 実 fetch はせず、frontend が drive_file.url 経由で remote 取得する。
	note.FileIDs = r.upsertAttachments(extractAttachments(apNote.Attachment, apNote.Sensitive.Bool()), &actor.ID, actor.Host)
	if len(note.FileIDs) > 0 {
		// AttachedFileTypes は MIME type の配列 (TS との互換性)。
		note.AttachedFileTypes = r.collectAttachedFileTypes(note.FileIDs)
	}
	// Question（投票）の場合はhasPollフラグをCreate前に設定
	if len(apNote.OneOf) > 0 || len(apNote.AnyOf) > 0 {
		note.HasPoll = true
	}
	// 引用 renote: upstream ApNoteService と同じく `_misskey_quote` / `quoteUrl` が
	// 指す note を解決して renoteId に紐付ける (#1527)。これが無いと remote quote が
	// 「本文だけ」で引用元が表示されない。解決失敗は best-effort で quote 無し扱い。
	// AP vote の早期 return より後 (= 実際に note を作る経路) で解決し、vote object に
	// quote field が乗っていても無駄な fetch をしない。renoteCount の増分は Create 後。
	quoted := r.resolveQuoteTarget(apNote.MisskeyQuote.String(), apNote.QuoteURL.String(), depth, ephemeral, chain)
	// 引用先が followers / specified(DM) の場合は紐付けない。本家
	// NoteCreateService.ts:346-352 は他人の followers note と全 specified note を
	// renote 対象から reject するため、連合の正規 quote がこれらを指すことはない。
	// inbound quote 解決はローカル note を可視性無視で引くため、これらを紐付けると
	// 非可視のローカル note が renote embed 経由で本来見られない viewer へ broadcast
	// される IDOR になる (#1534 / #1532 regression)。entity packing 層に hideNote
	// 相当が無い現状 (#1536) では、ここで弾くのが embed leak への最小防御。quoted=nil に
	// することで下の RenoteID 紐付けと renoteCount 増分の両方が skip される。
	if quoted != nil &&
		(quoted.Visibility == model.NoteVisibilityFollowers ||
			quoted.Visibility == model.NoteVisibilitySpecified) {
		quoted = nil
	}
	if quoted != nil && quoted.ID != note.ID {
		note.RenoteID = &quoted.ID
		note.RenoteUserID = &quoted.UserID
		note.RenoteUserHost = quoted.UserHost
	}
	if ephemeral {
		// リレー由来の投稿は DB に入れず Redis へ置く (#2332)。ここまでの
		// 検証 (host binding / attribution / 可視性 / mention 上限) は
		// 通常経路と完全に同じものを通っている。
		if err := r.ephemeralSink.PutNote(context.Background(), note, actor); err != nil {
			return nil, false, err
		}
		// repliesCount / renoteCount の増分と poll 行の作成は行わない。前者は
		// 対象が DB に無い可能性があり、後者は poll.noteId が note への FK を
		// 持つため行を作れない。hashtag 集計も DB 書き込みなので行わない。
		return note, true, nil
	}
	if err := r.noteRepo.Create(note); err != nil {
		// dedup race: FindByURI (上) と Create の間に別の ingest が同 URI を先に
		// 作ると note.uri の UNIQUE 制約違反になる (#1527 review #2)。その場合は
		// 既存行を引いて dedup hit (created=false) として返し、重複 INSERT と
		// renoteCount / 各 hook の二重発火を防ぐ。FindByURI が引けない (= UNIQUE
		// 違反以外の真の失敗) なら元の err を返す。
		if existing, ferr := r.noteRepo.FindByURI(apNote.ID); ferr == nil && existing != nil {
			return existing, false, nil
		}
		return nil, false, err
	}
	// ローカルノートへの返信の場合、repliesCount を増やす。
	// これにより timeline や API 上の「返信数」表示が federated reply も
	// 含むようになる。失敗はベストエフォートで無視。
	if note.ReplyID != nil {
		_ = r.noteRepo.IncrementCount(*note.ReplyID, "repliesCount", 1)
	}
	// 引用 renote は本家 NoteCreateService と同様、対象 note の renoteCount を増分する
	// (line 786: `data.renote && data.renote.userId !== user.id && !user.isBot`)。
	// 自分の note の自己引用と bot 投稿は除外する (#1527)。pure renote (Announce) は
	// handleAnnounce 側で別途 increment 済み。
	if note.RenoteID != nil && quoted != nil && quoted.UserID != actor.ID && !actor.IsBot {
		_ = r.noteRepo.IncrementCount(*note.RenoteID, "renoteCount", 1)
	}
	// Question (投票) の処理: oneOf/anyOf から Poll レコードを作成
	if r.pollRepo != nil && note.HasPoll {
		r.createPollFromQuestion(note, &apNote)
	}
	// 同じ投稿が先に relay 経由で ephemeral に入っていたら落とす。放置すると
	// timeline の合成で DB 行と ephemeral の両方が出て二重表示になる (#2332)。
	r.dropSupersededEphemeral(apNote.ID, string(note.Visibility), actor)
	// hashtag table の mentionedUsersCount / userIds 更新 (#680 / #719)。
	// hook 実装 (core/hashtag.Service) が内部で goroutine を起こす
	// fire-and-forget 設計なので、IngestNote から見ると即時 return する。
	// inbox processor の drain time が tag 数に比例して伸びる退行を回避。
	if r.hashtagHook != nil && len(note.Tags) > 0 {
		r.hashtagHook.OnNoteCreated(note, actor)
	}
	return note, true, nil
}

// createPollFromQuestion creates a Poll record from an AP Question's oneOf/anyOf choices.
func (r *Resolver) createPollFromQuestion(note *model.Note, apNote *activitypub.Note) {
	choices := apNote.OneOf
	multiple := false
	if len(apNote.AnyOf) > 0 {
		choices = apNote.AnyOf
		multiple = true
	}
	choiceNames := make([]string, len(choices))
	votes := make([]int64, len(choices))
	for i, c := range choices {
		choiceNames[i] = c.Name
		if c.Replies != nil {
			votes[i] = int64(c.Replies.TotalItems)
		}
	}
	poll := &model.Poll{
		NoteID:         note.ID,
		Multiple:       multiple,
		Choices:        choiceNames,
		Votes:          votes,
		NoteVisibility: note.Visibility,
		UserID:         note.UserID,
		UserHost:       note.UserHost,
	}
	// endTime/closedから有効期限を設定
	if apNote.EndTime != "" {
		if t, err := time.Parse(time.RFC3339, apNote.EndTime.String()); err == nil {
			poll.ExpiresAt = &t
		}
	} else if apNote.Closed != "" {
		if t, err := time.Parse(time.RFC3339, apNote.Closed.String()); err == nil {
			poll.ExpiresAt = &t
		}
	}
	_ = r.pollRepo.Create(poll)
}

// UpdateRemoteQuestion applies an inbound Update(Question) to an existing remote
// poll by refreshing its per-choice vote counts from the Question's oneOf/anyOf
// replies.totalItems (upstream ApQuestionService.updateQuestion、#1779)。
//
// 動作:
//   - object を Question として parse できなければ no-op
//   - URI で note を引いて見つからなければ no-op (反映先が無い)
//   - note の著者がローカルなら no-op (ローカル poll は remote Update で変更しない)
//   - poll が無ければ no-op
//   - Update activity の actor が poll 著者と一致しなければ拒否 (他 user の poll を
//     更新させない、upstream の attribution check 相当)
//   - 既存 choice 名で apChoice を照合し、新しい totalItems で votes を上書きする
//
// votes 以外 (choices / expiresAt) は更新しない (upstream も votes のみ更新)。
func (r *Resolver) UpdateRemoteQuestion(object json.RawMessage, actorURI string) error {
	if r.noteRepo == nil || r.pollRepo == nil {
		return nil
	}
	var apNote activitypub.Note
	if err := json.Unmarshal(object, &apNote); err != nil {
		return nil
	}
	// 書き込み側 (ingestNoteWithCreated) が正規化した値で保存しているので、
	// lookup も同じ正規化を通す (#2662)。
	apNote.ID = trimWHATWGURL(apNote.ID)
	if apNote.ID == "" {
		return nil
	}
	note, err := r.noteRepo.FindByURI(apNote.ID)
	if err != nil {
		return nil
	}
	if note.UserHost == nil {
		// ローカル著者の poll は remote からの Update で書き換えない。
		return nil
	}
	poll, err := r.pollRepo.FindByNoteID(note.ID)
	if err != nil || poll == nil {
		return nil
	}
	// attribution: Update の actor は poll 著者 (note author) と一致必須。別 user の
	// poll URI を指定した更新を拒否する。
	if actorURI != "" && r.userRepo != nil {
		if author, aerr := r.userRepo.FindByID(note.UserID); aerr == nil && author != nil && author.URI != nil && *author.URI != actorURI {
			return nil
		}
	}
	apChoices := apNote.OneOf
	if len(apChoices) == 0 {
		apChoices = apNote.AnyOf
	}
	if len(apChoices) == 0 {
		return nil
	}
	// choice 名 -> totalItems。upstream は name 一致で照合する (位置ではない)。
	counts := make(map[string]int64, len(apChoices))
	for _, c := range apChoices {
		if c.Replies != nil && c.Replies.TotalItems >= 0 {
			counts[c.Name] = int64(c.Replies.TotalItems)
		}
	}
	newVotes := make([]int64, len(poll.Choices))
	copy(newVotes, poll.Votes)
	changed := false
	for i, name := range poll.Choices {
		if v, ok := counts[name]; ok && v != newVotes[i] {
			newVotes[i] = v
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return r.pollRepo.UpdateVotes(note.ID, newVotes)
}

// UpdateRemoteNote applies an inbound Update Note activity body to an existing
// remote-authored note row. Misskey 本家にはノート編集機能が無いためローカル
// note は不変だが、Mastodon 等から push されてくる Update activity は受信
// するだけ受信して反映する。
//
// 動作:
//   - URI でノートを引いて見つからなければ何もせず nil
//   - 見つかったが著者がローカルなら何もしない (ローカルノートは変更不可)
//   - Update activity の actor (actorURI) が note 著者と一致しなければ無視
//     (別 remote 著者の note URI を狙った改ざんを拒否、UpdateRemoteQuestion と対称、#1819)
//   - 著者がリモートで actor が一致すれば text/cw/sensitive/mentions を更新
func (r *Resolver) UpdateRemoteNote(body []byte, actorURI string) (*model.Note, error) {
	if r.noteRepo == nil {
		return nil, ErrInvalidNote
	}
	var apNote activitypub.Note
	if err := json.Unmarshal(body, &apNote); err != nil {
		return nil, ErrInvalidNote
	}
	// lookup も書き込み側と同じ正規化を通す (#2662)。
	apNote.ID = trimWHATWGURL(apNote.ID)
	if apNote.ID == "" {
		return nil, ErrInvalidNote
	}
	existing, err := r.noteRepo.FindByURI(apNote.ID)
	if err != nil {
		// 未取得のリモート Note は無視 (こちらに該当データが無いものを編集する
		// 通知が来ても反映先が無いため)。
		return nil, nil
	}
	if existing.UserHost == nil {
		// ローカルノートに対する Update は無視 (Misskey は編集機能を持たない)。
		return existing, nil
	}
	// attribution: Update の actor は note 著者と一致必須。別 remote 著者の note URI を
	// 指定した改ざん (text/cw/sensitive/mentions 上書き) を拒否する。inbox 層の
	// authorizeActor は signer==activity.actor と activity.id host の整合しか保証
	// しないため、ここで対象 note の著者まで照合する (#1819、UpdateRemoteQuestion と対称)。
	if actorURI != "" && r.userRepo != nil {
		if author, aerr := r.userRepo.FindByID(existing.UserID); aerr == nil && author != nil && author.URI != nil && *author.URI != actorURI {
			return existing, nil
		}
	}
	fields := map[string]any{}
	// IngestNoteと同じ3段フォールバックでtext抽出
	var newText string
	if apNote.Source != nil &&
		apNote.Source.MediaType == "text/x.misskeymarkdown" &&
		apNote.Source.Content != "" {
		newText = apNote.Source.Content
	} else if apNote.MisskeyContent != "" {
		newText = apNote.MisskeyContent.String()
	} else if apNote.Content != "" {
		newText = mfm.FromHTML(apNote.Content)
	}
	if newText != "" {
		fields["text"] = &newText
		existing.Text = &newText
	}
	// text が変わらなくても tag 配列の Mention は AP Update で変化しうる (#397)。
	// IngestNote と同じく本文と tag 両方から mention を集めて user ID 配列に
	// 統一する (mentions 列の意味論を local create 経路と揃えるため)。
	var textMentions []string
	switch {
	case newText != "":
		textMentions = r.resolveTextMentionUserIDs(corenote.ExtractMentionStructs(newText))
	case existing.Text != nil:
		textMentions = r.resolveTextMentionUserIDs(corenote.ExtractMentionStructs(*existing.Text))
	}
	tagMentions := r.resolveMentionedUserIDs(extractMentionTags(apNote.Tag))
	mentions := mergeMentionIDs(textMentions, tagMentions)
	if !slices.Equal([]string(existing.Mentions), []string(mentions)) {
		fields["mentions"] = mentions
		existing.Mentions = mentions
	}
	if apNote.Summary != "" {
		summary := apNote.Summary.String()
		fields["cw"] = &summary
		existing.CW = &summary
	} else if apNote.Sensitive {
		// Summary が空でも sensitive なら空 CW を保つ (IngestNote と対称)。
		empty := ""
		fields["cw"] = &empty
		existing.CW = &empty
	}
	// AP Note Tag配列からカスタム絵文字を抽出してDBにupsert
	// 既存値と比較して変化があった場合のみfieldsに含める
	if existing.UserHost != nil {
		emojis := r.upsertEmojis(extractEmojiTags(apNote.Tag), *existing.UserHost)
		if !slices.Equal([]string(existing.Emojis), []string(emojis)) {
			fields["emojis"] = emojis
			existing.Emojis = emojis
		}
	}
	// hashtag は AP `tag` Hashtag entry + 編集後の本文 / CW から再抽出して
	// 差分があれば更新 (#679)。IngestNote と同じ規則。
	//
	// 順序依存に注意: 上の text 更新 (`if newText != "" { existing.Text = &newText }`)
	// と CW 更新が既に適用済みの状態で `existing.Text` / `existing.CW` を
	// 読むので、再抽出は更新後の値で行う。`if newText != ""` を skip した
	// 場合は元の値を保つので、この場合も「現在の本文 + 新 tag」で再計算
	// される。将来 text 更新ロジックを別関数に切り出す等の refactor が
	// 入ったら、この順序依存も追従させること。
	hashtagSources := extractHashtagTagNames(apNote.Tag)
	if existing.Text != nil {
		hashtagSources = append(hashtagSources, *existing.Text)
	}
	if existing.CW != nil {
		hashtagSources = append(hashtagSources, *existing.CW)
	}
	newTags := hashtag.ExtractNoteTags(hashtagSources...) // 正規化して格納 (#1948-18)
	tagsChanged := !slices.Equal([]string(existing.Tags), newTags)
	if tagsChanged {
		// Extract は hashtag が無いと nil を返すが、nil の model.StringArray は
		// Updates() map 経由で SQL NULL になり note.tags (NOT NULL) 制約に違反する。
		// tags が非空→空に変わるケースで踏むため空配列に倒して '{}' を書く。
		noteTags := model.StringArray(newTags)
		if noteTags == nil {
			noteTags = model.StringArray{}
		}
		fields["tags"] = noteTags
		existing.Tags = noteTags
	}
	// AP `attachment` 配列の差分を反映する (#378)。driveFileRepo 未設定時は
	// upsertAttachments が空 slice を返すので何もしない (= 既存 fileIDs を
	// 誤って空に上書きしない、Devin #400 #1)。
	if r.driveFileRepo != nil {
		fileIDs := r.upsertAttachments(extractAttachments(apNote.Attachment, apNote.Sensitive.Bool()), &existing.UserID, existing.UserHost)
		if !slices.Equal([]string(existing.FileIDs), []string(fileIDs)) {
			fields["fileIds"] = model.StringArray(fileIDs)
			existing.FileIDs = model.StringArray(fileIDs)
			types := r.collectAttachedFileTypes(fileIDs)
			fields["attachedFileTypes"] = model.StringArray(types)
			existing.AttachedFileTypes = model.StringArray(types)
		}
	}
	if len(fields) == 0 {
		return existing, nil
	}
	if err := r.noteRepo.UpdateFields(existing.ID, fields); err != nil {
		return nil, err
	}
	// hashtag table の更新は「tags が変化したかつ新規 tag がある」場合のみ呼ぶ
	// (#680)。RecordMention は冪等なので二度叩いても害は無いが、UPDATE を毎回
	// 走らせるのは無駄。tag が増えた / 入れ替わった場合に新規 tag 行が確保され
	// 著者カウントが反映される。逆に tag が「減った」場合の数値減算は upstream
	// Misskey TS も実装していない (新規 mention の積み上げ専用) ので追従しない。
	if tagsChanged && len(newTags) > 0 && r.hashtagHook != nil {
		// existing は user 情報を持たないので、UserID + UserHost から最小限の
		// User を組む。HashtagHook は IsLocal() / ID しか参照しないので十分。
		author := &model.User{ID: existing.UserID, Host: existing.UserHost}
		r.hashtagHook.OnNoteCreated(existing, author)
	}
	return existing, nil
}

// ExtractLocalUserID returns the user ID for a URI matching the local
// /users/{id} pattern, or "" if the URI does not match.
func (r *Resolver) ExtractLocalUserID(uri string) string {
	if r.urls == nil {
		return ""
	}
	prefix := r.urls.UserURI("")
	if !strings.HasPrefix(uri, prefix) {
		return ""
	}
	rest := uri[len(prefix):]
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// extractLocalNoteID returns the trailing note ID for a URI rooted at the
// local instance, or "" if the URI does not match the local pattern.
func (r *Resolver) extractLocalNoteID(uri string) string {
	if r.urls == nil {
		return ""
	}
	prefix := r.urls.NoteURI("")
	if !strings.HasPrefix(uri, prefix) {
		return ""
	}
	rest := uri[len(prefix):]
	// /notes/{id} の "/{id}" 部分しか持たないように、追加のスラッシュは無視する
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// pointerStringsEqual returns true if both *string point to equal values
// (nil/nil 同士も等しい)。upsertEmojis の license diff など、`*string` で
// 「未指定」と「明示空文字列」を区別する経路で使う。
func pointerStringsEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// mergeMentionIDs merges two ordered ID slices preserving order and removing
// duplicates. text 由来 mention と AP `tag` Mention 由来 mention を合算する
// (#397) ために使う。両方空なら nil ではなく非 nil 空 (pq の '{}' 既定値と
// 整合) を返す。
func mergeMentionIDs(a, b []string) model.StringArray {
	out := make(model.StringArray, 0, len(a)+len(b))
	seen := make(map[string]struct{}, len(a)+len(b))
	for _, id := range a {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	for _, id := range b {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// extractMentionTags parses the Tag array of a Note and returns the `href`
// of every Mention entry (type="Mention"). AP では宛先 actor URI が tag 配列
// の Mention に乗ってくるため、本文 @mention 抽出だけでは specified DM の
// 受信者を取りこぼす (#397)。href が空のものはスキップ。
func extractMentionTags(tags []any) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		m, ok := tag.(map[string]any)
		if !ok {
			continue
		}
		if apTypeOf(m) != "Mention" {
			continue
		}
		href, _ := m["href"].(string)
		if href == "" {
			continue
		}
		out = append(out, href)
	}
	return out
}

// resolveMentionedUserIDs maps actor URIs (typically from AP Mention tags or
// the `to` array) to local model.User IDs. ローカル URI は ExtractLocalUserID
// で安価に変換し、リモート URI は既知 (DB に取り込み済) のものだけ
// userRepo.FindByURI でルックアップする。未知リモート URI は federation fetch
// すると inbox 処理が重くなるため skip。返り値は入力順を保ち重複排除する。
func (r *Resolver) resolveMentionedUserIDs(hrefs []string) []string {
	if len(hrefs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(hrefs))
	out := make([]string, 0, len(hrefs))
	for _, href := range hrefs {
		var id string
		if local := r.ExtractLocalUserID(href); local != "" {
			id = local
		} else if r.userRepo != nil {
			if u, err := r.userRepo.FindByURI(href); err == nil && u != nil {
				id = u.ID
			}
		}
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// resolveTextMentionUserIDs maps text-derived `@username[@host]` mentions to
// local model.User IDs. mentions 列の意味論を local create 経路 (user ID 配列)
// と揃えるため、リモート受信 Note でも username → ID 解決を必ず通す (#397)。
// userRepo 未設定 / 未知ユーザーは skip する (NotificationService 等の
// 既存後段は skip でも username fallback で動くが、mentions 列の query は
// ID 完全一致なので残しても無駄)。
func (r *Resolver) resolveTextMentionUserIDs(mentions []corenote.Mention) []string {
	if r.userRepo == nil || len(mentions) == 0 {
		return nil
	}
	out := make([]string, 0, len(mentions))
	for _, m := range mentions {
		var host *string
		if m.Host != "" {
			h := m.Host
			host = &h
		}
		u, err := r.userRepo.FindByUsernameLower(m.Username, host)
		if err != nil || u == nil {
			continue
		}
		out = append(out, u.ID)
	}
	return out
}

// extractHashtagTagNames parses the Tag array of a Note and returns the
// `name` of every Hashtag entry (type="Hashtag"). upstream Misskey の
// renderHashtag は `name: "#" + tag` 形式で出すので、戻り値も `#tag`
// プレフィクス付き。呼び出し側で hashtag.Extract に渡せば本文由来の
// 抽出と統一的に dedup / 正規化される (#679)。
//
// AP spec は `name` の format を厳密に規定していないため、`#` 無しで
// 来る実装 (稀) のために defensive に prefix を補う。よって戻り値は
// 必ずしも入力 `name` と完全一致しない (`"tag"` → `"#tag"` に書き換え
// られる場合がある)。`name` が空のものはスキップ。type 違いも skip。
func extractHashtagTagNames(tags []any) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		m, ok := tag.(map[string]any)
		if !ok {
			continue
		}
		if apTypeOf(m) != "Hashtag" {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		// `#` prefix 欠落は upstream の renderHashtag では起きないが、
		// 他実装が `name: "tag"` だけ送ってきた場合に hashtag.Extract の
		// 正規表現にマッチさせるため補完する。
		if !strings.HasPrefix(name, "#") {
			name = "#" + name
		}
		out = append(out, name)
	}
	return out
}

// extractEmojiTags parses the Tag array of a Person or Note and returns
// any elements with type "Emoji". Tag配列はJSON unmarshal後に []any
// (各要素が map[string]any) として届くため、型アサーションで抽出する。
func extractEmojiTags(tags []any) []activitypub.EmojiTag {
	if len(tags) == 0 {
		return nil
	}
	var out []activitypub.EmojiTag
	for _, tag := range tags {
		m, ok := tag.(map[string]any)
		if !ok {
			continue
		}
		typ := apTypeOf(m)
		if typ != "Emoji" {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		var iconURL string
		if icon, ok := m["icon"].(map[string]any); ok {
			iconURL, _ = icon["url"].(string)
		}
		if iconURL == "" {
			continue
		}
		id, _ := m["id"].(string)
		updated, _ := m["updated"].(string)
		// upstream Misskey TS の renderEmoji は license を `_misskey_license`
		// オブジェクト (`{freeText: string|null}`) で federate する (#731)。
		// 3 状態を区別して保存する:
		//   - wrapper 欠落 → License = nil ("license 情報が federate されて
		//     いない" = 上書きしない)
		//   - wrapper あり + freeText=null → FreeText = nil ("license は明示的
		//     に未設定" = NULL 上書き OK)
		//   - wrapper あり + freeText=string → FreeText = &string (具体値)
		var license *activitypub.MisskeyLicense
		if lic, ok := m["_misskey_license"].(map[string]any); ok {
			license = &activitypub.MisskeyLicense{}
			if free, ok := lic["freeText"].(string); ok {
				license.FreeText = &free
			}
		}
		out = append(out, activitypub.EmojiTag{
			Type:    "Emoji",
			Name:    name,
			Icon:    activitypub.Image{Type: "Image", URL: activitypub.APLenientHref(iconURL)},
			ID:      id,
			Updated: updated,
			License: license,
		})
	}
	return out
}

// upsertEmojis persists remote emoji from AP tags and returns the list of
// short names (without colons). 同一hostに対して FindManyByNamesAndHost で
// バッチ取得し、その結果を元にcreate/updateを判定することでN+1を回避する。
// Create/UpdateFieldsが失敗した場合はその名前を結果から除外し、後段で
// 「名前は載っているが行が存在しない」状態を防ぐ。emojiRepoが未設定なら
// 空配列を返す。
func (r *Resolver) upsertEmojis(tags []activitypub.EmojiTag, host string) model.StringArray {
	if r.emojiRepo == nil || len(tags) == 0 {
		return model.StringArray{}
	}
	// 同名タグが重複して来るケースに備えて重複排除しつつ name → tag を保持
	tagByName := make(map[string]activitypub.EmojiTag, len(tags))
	order := make([]string, 0, len(tags))
	for _, tag := range tags {
		name := strings.Trim(tag.Name, ":")
		if name == "" {
			continue
		}
		if _, ok := tagByName[name]; ok {
			continue
		}
		tagByName[name] = tag
		order = append(order, name)
	}
	if len(order) == 0 {
		return model.StringArray{}
	}

	existingRows, err := r.emojiRepo.FindManyByNamesAndHost(order, &host)
	if err != nil {
		return model.StringArray{}
	}
	existingByName := make(map[string]*model.Emoji, len(existingRows))
	for _, e := range existingRows {
		existingByName[e.Name] = e
	}

	names := make(model.StringArray, 0, len(order))
	for _, name := range order {
		tag := tagByName[name]
		if existing, ok := existingByName[name]; ok {
			// 既存絵文字: URL/URIが変わっていればまとめてupdate
			updates := map[string]any{}
			if existing.OriginalURL != tag.Icon.URL.String() {
				updates["originalUrl"] = tag.Icon.URL.String()
				updates["publicUrl"] = tag.Icon.URL.String()
			}
			if tag.ID != "" && (existing.URI == nil || *existing.URI != tag.ID) {
				updates["uri"] = &tag.ID
			}
			// AP `_misskey_license.freeText` の差分も追従する (#731)。
			// nil wrapper = AP tag に license フィールド無し → 既存値を温存
			// (連合先が一時的に license export を停止した場合に上書きしない)。
			// FreeText nil 内部 = wrapper はあるが空 → 明示的に空 license で上書き。
			if tag.License != nil {
				newLicense := tag.License.FreeText
				existingLicense := existing.License
				if !pointerStringsEqual(newLicense, existingLicense) {
					updates["license"] = newLicense
				}
			}
			if len(updates) > 0 {
				if err := r.emojiRepo.UpdateFields(existing.ID, updates); err != nil {
					// updateに失敗してもrow自体は存在するので名前は返してよい
					slog.Warn("upsertEmojis: update failed",
						"name", name, "host", host, "err", err)
				}
			}
			names = append(names, name)
			continue
		}
		// 新規絵文字: create
		now := r.clock()
		uri := tag.ID
		emoji := &model.Emoji{
			ID:          r.idGen.Generate(now),
			Name:        name,
			Host:        &host,
			OriginalURL: tag.Icon.URL.String(),
			PublicURL:   tag.Icon.URL.String(),
		}
		// #731: AP `_misskey_license` 経由で federation 直後に保存。
		// wrapper があれば FreeText (nil 含む) を取り込む、wrapper 自体が
		// nil なら model.Emoji.License は nil のまま。
		if tag.License != nil {
			emoji.License = tag.License.FreeText
		}
		if uri != "" {
			emoji.URI = &uri
		}
		if err := r.emojiRepo.Create(emoji); err != nil {
			// createに失敗した場合、行が存在しないままになる可能性があるため
			// 結果から除外する。EmojiResolverは欠損nameを単に解決しないだけで
			// クライアント側のフォールバック表示が利く。
			slog.Warn("upsertEmojis: create failed",
				"name", name, "host", host, "err", err)
			continue
		}
		names = append(names, name)
	}
	return names
}

// deriveVisibility maps an AS to/cc audience pair to a Misskey visibility,
// mirroring upstream ApAudienceService.parseAudience (#1864):
//
//   - to に Public があれば public
//   - cc に Public があれば home
//   - to / cc のいずれかに followers があれば followers
//   - それ以外 (specific actor 列挙) は specified
//
// Public は upstream isPublic と同じく full IRI / as:Public / 裸 Public の 3 形式を
// 受ける。followers collection の判定は upstream isFollowers のような actor の
// followersUri 厳密一致ではなく /followers サフィックスで近似する。このため別 actor の
// followers URL が to/cc に入っていると upstream の specified でなく followers になる等の
// 差は出るが、to/cc は note の author (Announce では announcer) が自分の note/boost に
// 対して設定するものなので、緩めても自分のコンテンツの可視性が変わるだけで第三者の
// note を露出させることはない。
//
// #2106 L31 (documented limitation): mk-go は remote user の followersUri を保持しないため
// upstream ApAudienceService.isFollowers (`id === actor.followersUri ?? actor.uri+'/followers'`)
// の厳密一致を行えず suffix heuristic で近似する。exotic audience (他人の followers collection
// を to/cc に含む note) で upstream が specified に倒すところを followers に倒す微差が残る
// (#1864 の既知トレードオフ)。将来 followersUri を保持・参照できるようになったら厳密化する。
func deriveVisibility(to, cc []string) model.NoteVisibility {
	hasFollowers := func(list []string) bool {
		for _, v := range list {
			if strings.HasSuffix(v, "/followers") {
				return true
			}
		}
		return false
	}
	if hasPublicAudience(to) {
		return model.NoteVisibilityPublic
	}
	if hasPublicAudience(cc) {
		return model.NoteVisibilityHome
	}
	if hasFollowers(to) || hasFollowers(cc) {
		return model.NoteVisibilityFollowers
	}
	return model.NoteVisibilitySpecified
}

// hasPublicAudience reports whether list contains the AS public collection in
// any of the forms upstream ApAudienceService.isPublic accepts (#1864)。
func hasPublicAudience(list []string) bool {
	for _, v := range list {
		if v == activitypub.Public || v == "as:Public" || v == "Public" {
			return true
		}
	}
	return false
}

// hostFromURI extracts the host portion from an actor URI.
// **正規化して返す。** ここが host を作る唯一の入口で、`user.host` /
// `instance.host` / `emoji.host` / `note.userHost` / `following.*Host` などは
// すべてこの値の写し。`url.Parse` は小文字化も punycode 化もしないので、
// `https://Mixed.Example/users/x` のような actor URI を出すサーバーの行が
// 非正規化で入り、acct 解決が空振りする (#2704 / #2706)。upstream も保存時に
// `punyHost` を掛けている (ApPersonService.ts)。
//
// 比較専用の punyHost とは役割が違う。あちらは**読み取り側で両辺を揃える**もので、
// backfill 前の非正規化行が残っているあいだは併存する。
func hostFromURI(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host in %q", uri)
	}
	return idnhost.Puny(u.Host), nil
}

// punyHost normalizes a host for comparison the way upstream
// UtilityService.toPuny does (idna.ToASCII(lowercase), UTS#46)。Unicode IDN と
// punycode の mixed-form (例: `パイ.example` vs `xn--eckve.example`) を同一視
// するため host 一致比較の両辺に適用する (#1850)。idna が失敗する不正入力のみ
// 小文字化で返す (Go default の lenient UTS#46 profile では port 付き host も
// 成功し ASCII tail はそのまま残るため、fallback は実質ほぼ発生しない)。これは
// 保存側 (`hostFromURI`) も #2706 で同じ正規化を掛けるようになったので、両者は
// 同じ値を作る。punyHost が今も要るのは、**backfill 前に非正規化で保存された行**と
// 外から渡ってくる acct の host を突き合わせるため。
//
// なお Go の idna は ideographic/fullwidth dot (U+3002 等) を `.` に畳まない
// (Node の domainToASCII と異なるが、別 authority を同一視しない安全側)。
func punyHost(host string) string { return idnhost.Puny(host) }

// finalURLFetcher は redirect 後の最終 URL も返せる fetcher。本番 APFetcher が
// 実装し、resolver が fetch 元 host と object id host の一致検証に使う (#1820)。
// テストダブル (stubFetcher) は実装しなくてよく、その場合 resolver は最終 URL
// 不明として host 検証を skip する (本番 APFetcher は必ず最終 URL を返すので
// production では常に検証される)。
type finalURLFetcher interface {
	FetchObjectWithFinalURL(uri string) ([]byte, string, error)
}

// fetchObjectWithFinalURL fetches uri and returns (body, finalURL). finalURL は
// fetcher が finalURLFetcher を実装する場合のみ非空。未実装 (テスト) では ""
// を返し、呼び出し側の host 検証を skip させる。
func (r *Resolver) fetchObjectWithFinalURL(uri string) ([]byte, string, error) {
	if ff, ok := r.fetcher.(finalURLFetcher); ok {
		return ff.FetchObjectWithFinalURL(uri)
	}
	body, err := r.fetcher.FetchObject(uri)
	return body, "", err
}

// hasActivityStreamsContext reports whether a fetched AP object's `@context`
// includes the ActivityStreams 2.0 namespace, replicating upstream
// Resolver.resolve の invalid-response guard (72180409, #1828)。本家同様に
// `@context` が配列なら AS URL を含むこと、配列でなければ AS URL と完全一致
// することを要求する。AS context を持たない任意 JSON を actor/note として
// 取り込むのを防ぐ。fetch 経路専用 — inbound delivery で配送される inlined
// object は外側 activity 側に @context があり object 自身は持たないことがある
// ため、この検証は fetch した standalone document にのみ適用する。
func hasActivityStreamsContext(ctx any) bool {
	switch v := ctx.(type) {
	case string:
		return v == activitypub.ContextURL
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && s == activitypub.ContextURL {
				return true
			}
		}
		return false
	default:
		// nil (欠落) / object / number 等はいずれも AS context 不在として reject。
		return false
	}
}

// assertResponseHostMatches enforces upstream assertActivityMatchesUrl の hard
// requirement: fetch した AP object の id host が、実際に body を返した最終 URL
// の host と一致すること。これにより evil.example の AP サーバーが
// `{"id":"https://victim.example/..."}` を返して別 host になりすます
// object-spoofing を防ぐ。id 欠落 / https→http downgrade も拒否する (#1820)。
// finalURL が空 (最終 URL 不明) の場合は検証を skip する。
func assertResponseHostMatches(finalURL, objectID string) error {
	if finalURL == "" {
		return nil
	}
	if objectID == "" {
		return ErrObjectHostMismatch
	}
	fu, ferr := url.Parse(finalURL)
	iu, ierr := url.Parse(objectID)
	if ferr != nil || ierr != nil || fu.Hostname() == "" || iu.Hostname() == "" {
		return ErrObjectHostMismatch
	}
	// https で取得したのに id が http の場合は downgrade 拒否 (upstream と同じ)。
	if fu.Scheme == "https" && iu.Scheme != "https" {
		return ErrObjectHostMismatch
	}
	if normalizeMatchHost(fu) != normalizeMatchHost(iu) {
		return ErrObjectHostMismatch
	}
	return nil
}

// assertRequestHostMatches enforces that the original request URI host equals
// the fetched object's id host, mirroring upstream assertActivityMatchesUrl の
// request-url ↔ id Strict 検証 (#1828)。federation-loop fetch (fetchActor /
// resolveNoteOnce) で attacker 制御の entry URI から cross-host redirect を辿って
// 第三者 object の解決/refresh を誘発される SSRF/amplification を防ぐ。finalURL ↔
// id の binding は assertResponseHostMatches が別途担保するので、ここは request ↔
// id の host 一致のみを見る (downgrade は final ↔ id 側で検出済み)。user-initiated
// な ap/show 経路は upstream 同様 cross-host を許容するため、この検証を skip する。
func assertRequestHostMatches(requestURI, objectID string) error {
	if objectID == "" {
		return ErrObjectHostMismatch
	}
	ru, rerr := url.Parse(requestURI)
	iu, ierr := url.Parse(objectID)
	if rerr != nil || ierr != nil || ru.Hostname() == "" || iu.Hostname() == "" {
		return ErrObjectHostMismatch
	}
	if normalizeMatchHost(ru) != normalizeMatchHost(iu) {
		return ErrObjectHostMismatch
	}
	return nil
}

// crossHostKey namespaces a singleflight key for cross-host-allowed (ap/show)
// resolves so a Strict federation-loop resolve never collapses onto a relaxed
// in-flight one (and thereby skip its request-host binding)。Strict 経路の key は
// uri そのままなので通常の dedup 挙動は不変。
// pickID returns the pre-assigned ID when one was carried over from the
// ephemeral store, and a freshly generated one otherwise (#2332)。
func pickID(preassigned string, gen id.Generator, now time.Time) string {
	if preassigned != "" {
		return preassigned
	}
	return gen.Generate(now)
}

// actorGroupKey extends crossHostKey with the skip-featured flag.
//
// **キーに混ぜないと再帰の抑止が破れる。** 同じキーの呼び出しは 1 つに畳まれるので、featured を引く呼び出しと引かない呼び出しが合流すると、
// ノート解決の内側から featured の取り込みが走りうる (#2552)。
func actorGroupKey(uri string, allowCrossHost, skipFeatured bool) string {
	if skipFeatured {
		return "nofeat\x00" + crossHostKey(uri, allowCrossHost)
	}
	return crossHostKey(uri, allowCrossHost)
}

func crossHostKey(uri string, allowCrossHost bool) string {
	if allowCrossHost {
		return "xhost\x00" + uri
	}
	return uri
}

// noteGroupKey extends crossHostKey with the ephemeral flag so that a
// database-bound resolve and an ephemeral one are never collapsed into the
// same singleflight call (#2332)。
func noteGroupKey(uri string, allowCrossHost, ephemeral bool) string {
	if ephemeral {
		return "eph\x00" + crossHostKey(uri, allowCrossHost)
	}
	return crossHostKey(uri, allowCrossHost)
}

// normalizeMatchHost canonicalizes a URL host for object-host comparison,
// mirroring upstream の URL.host (default port を除去) + normalizeSynonymousSubdomain
// (先頭 www. を除去)。これが無いと `remote.example:443` vs `remote.example` や
// `www.remote.example` vs `remote.example` を誤って弾く (#1820 review)。
func normalizeMatchHost(u *url.URL) string {
	// idna 正規化で Unicode IDN と punycode の mixed-form を同一視する (#1850)。
	// `www.` 除去は upstream の `normalizeSynonymousSubdomain` 相当で、upstream も
	// **`assertActivityMatchesUrl` (= fetch した document の id と URL の照合)
	// でしか使わない**。actor が申告する値 (inbox / sharedInbox / publicKey.id /
	// assertionMethod[].id) の host 検証は upstream の `punyHost` に合わせて
	// `sameDeliveryHost` を使うこと。`www.` を同一視すると、`www` サブドメインを
	// 名乗る actor が親ドメインの値を宣言できてしまう (#2662)。
	host := punyHost(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	port := u.Port()
	isDefaultPort := (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")
	if port != "" && !isDefaultPort {
		return host + ":" + port
	}
	return host
}

// sameDeliveryHost reports whether two absolute URIs share the same host under
// upstream's punyHost rules (punycode + port, **no `www.` stripping**).
//
// 配送先 (`inbox` / `sharedInbox`) の host 検証はこちらを使う。
// `normalizeMatchHost` は `#1820` の object-host binding 用に upstream の
// `normalizeSynonymousSubdomain` (`/^www\./` 除去) を取り込んでいるが、
// **upstream はそれを `assertActivityMatchesUrl` でしか使わず、
// `validateActor` の inbox 検証には `punyHost` しか通さない**。`www.` を
// 同一視すると、`www` サブドメインが別管理下にある (dangling DNS など) 環境で
// DM を含む outbound をそちらへ向けられる (#2662)。
func sameDeliveryHost(a, b string) bool {
	au, aerr := url.Parse(trimWHATWGURL(a))
	bu, berr := url.Parse(trimWHATWGURL(b))
	if aerr != nil || berr != nil || au.Hostname() == "" || bu.Hostname() == "" {
		return false
	}
	return punyHostPort(au) == punyHostPort(bu)
}

// trimWHATWGURL strips the characters that the WHATWG URL parser removes before
// parsing, so `url.Parse` accepts what upstream's `new URL()` accepts.
//
// upstream は `punyHost` = `new URL()` なので、**前後の C0 制御文字と空白は
// 除去され、tab / CR / LF は全位置で除去される**。Go の `net/url.Parse` は
// これらをエラーにするので、そのままだと「末尾に改行が付いた inbox」を出す
// 実装の actor が丸ごと reject される (#2662)。
func trimWHATWGURL(raw string) string {
	raw = strings.Trim(raw, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\t\n\v\f\r\x0e\x0f"+
		"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f ")
	return strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(raw)
}

// punyHostPort mirrors upstream's punyHost (idna host + non-default port).
func punyHostPort(u *url.URL) string {
	host := punyHost(u.Hostname())
	port := u.Port()
	isDefaultPort := (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")
	if port != "" && !isDefaultPort {
		return host + ":" + port
	}
	return host
}

// extractAttachments parses the AP `attachment` array (heterogeneous []any
// after JSON unmarshal) and returns Document entries. type が upstream の
// `validDocumentTypes` ("Audio" / "Document" / "Image" / "Page" / "Video") の
// いずれかで `url` を持つもののみ採用する。#378 / #2662。
// noteSensitive は upstream の `attach.sensitive ??= note.sensitive` に対応する。
// 添付側に `sensitive` が無いとき note レベルの値を継ぐ。
func extractAttachments(rawAttachments []any, noteSensitive bool) []activitypub.Document {
	if len(rawAttachments) == 0 {
		return nil
	}
	out := make([]activitypub.Document, 0, len(rawAttachments))
	for _, raw := range rawAttachments {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ := apTypeOf(m)
		switch typ {
		// upstream の validDocumentTypes は Page も含む (type.ts:263)。
		case "Document", "Image", "Audio", "Video", "Page":
			// ok
		default:
			continue
		}
		urlStr, _ := m["url"].(string)
		if urlStr == "" {
			continue
		}
		mediaType, _ := m["mediaType"].(string)
		name, _ := m["name"].(string)
		// upstream が実際に NSFW 判定に使うのは添付単位の `sensitive`
		// (`attach.sensitive ??= note.sensitive` → `DriveFile.isSensitive`)。
		// note レベルだけ寛容にして file レベルを `.(bool)` のままにすると、
		// `"sensitive": "true"` の添付が非 NSFW になる (#2662)。
		// 添付側に読める値が無ければ note レベルを継ぐ (upstream の `??=`)。
		// `??=` は null / undefined のときだけ代入するので、**欠落と明示 null は
		// 継承、読めた値は明示 false でも優先**する。
		sensitive := noteSensitive
		if raw, present := m["sensitive"]; present && raw != nil {
			if v, known := activitypub.ParseBoolish(raw); known {
				sensitive = v
			}
		}
		// width / height / blurhash / icon は省略実装が多いので欠落時は
		// zero value のまま。upsertAttachments 側で 0/空チェックして
		// DriveFile に書き込むかを判断する (#460/#461)。
		width := numberAsInt(m["width"])
		height := numberAsInt(m["height"])
		blurhash, _ := m["_misskey_blurhash"].(string)
		var icon *activitypub.Image
		if iconMap, ok := m["icon"].(map[string]any); ok {
			iconURL, _ := iconMap["url"].(string)
			iconType := apTypeOf(iconMap)
			if iconURL != "" {
				icon = &activitypub.Image{Type: activitypub.APType(iconType), URL: activitypub.APLenientHref(iconURL)}
			}
		}
		out = append(out, activitypub.Document{
			Type:      typ,
			MediaType: mediaType,
			URL:       urlStr,
			Name:      name,
			Sensitive: sensitive,
			Width:     width,
			Height:    height,
			Icon:      icon,
			Blurhash:  blurhash,
		})
	}
	return out
}

// numberAsInt は JSON unmarshal 後の `any` を int に丸めて返す。
// encoding/json は数値を float64 でデコードするため、width/height の
// ような integer 想定 field を取り出すときに毎回 type switch するのを
// 避けるためのヘルパー。NaN / 負値は 0 として扱い、後段の `> 0`
// ガードで弾く。
func numberAsInt(v any) int {
	switch x := v.(type) {
	case float64:
		if x <= 0 {
			return 0
		}
		return int(x)
	case int:
		if x <= 0 {
			return 0
		}
		return x
	case int64:
		if x <= 0 {
			return 0
		}
		return int(x)
	default:
		return 0
	}
}

// upsertAttachments persists each AP Document as a drive_file row (link
// 形式、isLink=true、実 fetch なし) and returns the resulting drive_file IDs
// in original order. URI による dedup を行うので、同じ remote attachment が
// 複数の note に紐付いても drive_file は 1 行のみ。
//
// driveFileRepo が未設定なら空 (model.StringArray{}) を返す (旧挙動)。userID は
// リモート user の ID (note.UserID 相当)、host はリモート host (nil =
// ローカル、attachment 文脈ではほぼ常に non-nil)。
func (r *Resolver) upsertAttachments(docs []activitypub.Document, userID, host *string) model.StringArray {
	if r.driveFileRepo == nil || len(docs) == 0 {
		return model.StringArray{}
	}
	ids := make(model.StringArray, 0, len(docs))
	for _, doc := range docs {
		// URI ベースで dedup。既存ならその ID を再利用する。
		if existing, err := r.driveFileRepo.FindByURI(doc.URL); err == nil && existing != nil {
			ids = append(ids, existing.ID)
			continue
		}
		now := r.clock()
		mediaType := doc.MediaType
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		name := doc.Name
		if name == "" {
			name = "file" // NOT NULL カラムへのフォールバック
		}
		var comment *string
		if doc.Name != "" {
			cn := doc.Name
			comment = &cn
		}
		uri := doc.URL
		f := &model.DriveFile{
			ID:             r.idGen.Generate(now),
			UserID:         userID,
			UserHost:       host,
			MD5:            "", // link 形式のため content hash 取得不可、placeholder
			Name:           name,
			Type:           mediaType,
			Size:           0, // 未 fetch のため未知
			Comment:        comment,
			URL:            doc.URL,
			URI:            &uri,
			IsLink:         true,
			IsSensitive:    doc.Sensitive,
			MaybeSensitive: doc.Sensitive,
			StoredInternal: false,
		}
		// AP Document に乗ってきた metadata を可能な限り永続化する
		// (#460 thumbnail / #461 properties)。link-format なので実体
		// 画像解析はしないが、remote 側が宣言している width/height/
		// icon/blurhash を信頼してそのまま保存する。
		if doc.Icon != nil && doc.Icon.URL != "" {
			thumb := doc.Icon.URL.String()
			f.ThumbnailURL = &thumb
		}
		width := doc.Width
		height := doc.Height
		// 上流 Misskey TS の renderDocument は width/height を AP に
		// 載せないため、image MIME の場合は best-effort で URL を
		// fetch して画像ヘッダから dimensions を復元する。失敗時は
		// 0/0 のまま属性 JSON を空にしておき、表示側のフォールバック
		// に任せる。タイムアウト 3s で inbox 全体は止めない。
		if (width == 0 || height == 0) && strings.HasPrefix(mediaType, "image/") && r.imageProbeClient != nil {
			if w, h, ok := probeImageDimensions(r.imageProbeClient, doc.URL); ok {
				if width == 0 {
					width = w
				}
				if height == 0 {
					height = h
				}
			}
		}
		if width > 0 || height > 0 {
			// upstream Misskey は properties JSON を `{width, height}` で
			// 持つ。orientation は EXIF 由来で AP には来ないので省略。
			props := map[string]int{}
			if width > 0 {
				props["width"] = width
			}
			if height > 0 {
				props["height"] = height
			}
			if encoded, err := json.Marshal(props); err == nil {
				f.Properties = encoded
			}
		}
		if doc.Blurhash != "" {
			bh := doc.Blurhash
			f.Blurhash = &bh
		}
		if err := r.driveFileRepo.Create(f); err != nil {
			slog.Warn("upsertAttachments: create failed",
				"url", doc.URL, "err", err)
			continue
		}
		ids = append(ids, f.ID)
	}
	return ids
}

// collectAttachedFileTypes returns the MIME type list for the given drive
// file IDs. note.AttachedFileTypes は TS 互換のためノート行に冗長保持される。
func (r *Resolver) collectAttachedFileTypes(fileIDs []string) model.StringArray {
	if r.driveFileRepo == nil || len(fileIDs) == 0 {
		return model.StringArray{}
	}
	files, err := r.driveFileRepo.FindByIDs(fileIDs)
	if err != nil {
		return model.StringArray{}
	}
	// FindByIDs の戻り順は不定なので id → type のマップで再整列する。
	byID := make(map[string]string, len(files))
	for _, f := range files {
		byID[f.ID] = f.Type
	}
	out := make(model.StringArray, 0, len(fileIDs))
	for _, id := range fileIDs {
		if t, ok := byID[id]; ok {
			out = append(out, t)
		}
	}
	return out
}

// RemoteMoveProcessor carries a moved account's followers and relationships
// over to its destination. Implemented by core/move.Service.
//
// federation → core/move の一方向依存なので循環しない。interface で受けるのは
// テストで差し替えるため。
type RemoteMoveProcessor interface {
	PostMoveProcess(src, dst *model.User)
}

// SetMoveProcessor wires the handler invoked when a remote account is observed
// to have moved.
func (r *Resolver) SetMoveProcessor(p RemoteMoveProcessor) { r.moveProcessor = p }

// HasHostBlockChecker reports whether the host block checker was wired.
//
// 未配線だと federation 設定 (none / specified / blockedHosts) を無視して
// リモートを解決する。起動時検査に使う (#2683)。
func (r *Resolver) HasHostBlockChecker() bool { return r.hostBlocker != nil }

// HasSilencedHostChecker reports whether the silenced-host checker was wired.
//
// 未配線だと silenced instance の remote public note が home へ降格されず
// public timeline に出る。起動時検査に使う (#2708)。
func (r *Resolver) HasSilencedHostChecker() bool { return r.silencedChecker != nil }
