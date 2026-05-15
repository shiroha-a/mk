// Package federation provides services for processing inbound and outbound
// ActivityPub traffic.
package federation

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"log/slog"

	"github.com/lib/pq"
	"github.com/shiroha-a/mk/internal/activitypub"
	"github.com/shiroha-a/mk/internal/activitypub/mfm"
	corenote "github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/misc/hashtag"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"golang.org/x/sync/singleflight"
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
}

// Errors returned by Resolver.
var (
	// ErrInvalidActor is returned when the fetched JSON cannot be parsed.
	ErrInvalidActor = errors.New("invalid actor document")
	// ErrInvalidNote is returned when a fetched Note cannot be parsed.
	ErrInvalidNote = errors.New("invalid note document")
)

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
type PublickeyExtraStore interface {
	Upsert(pk *model.UserPublickeyExtra) error
	FindByKeyID(keyID string) (*model.UserPublickeyExtra, error)
	ListByUserID(userID string) ([]model.UserPublickeyExtra, error)
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
	keysMu             sync.RWMutex
	keys               map[string]publicKeyEntry      // userID → publicKey + fetchedAt
	clock              func() time.Time               // テストで差し替える時計
	actorTTL           time.Duration                  // アクター情報の最大寿命
	instanceTracker    InstanceTracker                // optional: ホスト発見を通知
	chartHook          ChartHook                      // optional: 新規 remote user の集計
	hashtagHook        HashtagHook                    // optional: per-tag mentionedUsersCount 集計 (#680)
	publickeyRepo      PublickeyStore                 // optional: 公開鍵の永続化 (RSA)
	publickeyExtraRepo PublickeyExtraStore            // optional: 追加公開鍵 (Ed25519 / Multikey) の永続化
	pollRepo           repository.PollRepository      // optional: Question(投票)のPoll作成
	pollVoter          PollVoter                      // optional: AP vote (Note.name) の投票記録
	emojiRepo          repository.EmojiRepository     // optional: リモート絵文字の永続化
	driveFileRepo      repository.DriveFileRepository // optional: リモート添付の link 化
	// imageProbeClient は image attachment の dimension probe (#461) で
	// 使う outbound HTTP client。SSRF-safe transport (router.go で
	// safehttp.NewSSRFSafeTransport を適用したもの) を渡す前提で、
	// 未設定なら probe 自体をスキップする (安全側に倒す: SSRF リスクを
	// 起こすくらいなら properties 空のまま運用)。
	imageProbeClient *http.Client

	// resolveActorGroup / resolveNoteGroup は同一 URI への並行 ResolveActor /
	// ResolveNote 呼び出しを 1 度の DB lookup + HTTP fetch に collapse する
	// (#300 3-7)。inbox 受信時に同じ remote actor / note を参照する activity
	// が連続して届く現実的なケースで thundering herd を抑える。
	resolveActorGroup singleflight.Group
	resolveNoteGroup  singleflight.Group
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
	// 同一 URI への並行呼び出しは singleflight で 1 つに collapse する
	// (#300 3-7)。cache hit 経路は微秒なので serialize の影響は無視でき、
	// cold な fetch + Create で発生する HTTP fan-out + UNIQUE 衝突を抑える
	// 効果が大きい。
	v, err, _ := r.resolveActorGroup.Do(uri, func() (any, error) {
		return r.resolveActorOnce(uri)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, ErrInvalidActor
	}
	return v.(*model.User), nil
}

// resolveActorOnce is the body of ResolveActor, invoked once per URI by
// singleflight.Do.
func (r *Resolver) resolveActorOnce(uri string) (*model.User, error) {
	if existing, err := r.userRepo.FindByURI(uri); err == nil {
		if r.shouldRefreshActor(existing) {
			r.refreshActor(existing, uri)
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

	actor, err := r.fetchActor(uri)
	if err != nil {
		return nil, err
	}

	host, err := hostFromURI(actor.ID)
	if err != nil {
		return nil, ErrInvalidActor
	}

	now := r.clock()
	user := &model.User{
		ID:            r.idGen.Generate(now),
		Username:      actor.PreferredUsername,
		UsernameLower: strings.ToLower(actor.PreferredUsername),
		Host:          &host,
		URI:           &actor.ID,
		Inbox:         &actor.Inbox,
		IsBot:         activitypub.IsBotActorType(actor.Type),
		// AP actorの manuallyApprovesFollowers を IsLocked (承認制) として
		// 取り込む。これが false だとリモートの承認制ユーザに対するフォロー
		// が非 locked として処理されて即 Following が成立し、ボタン挙動と
		// AP仕様が崩れる。
		IsLocked:      actor.ManuallyApproves,
		LastFetchedAt: &now,
	}
	if name := actor.Name; name != "" {
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
	if actor.Endpoints.SharedInbox != "" {
		user.SharedInbox = &actor.Endpoints.SharedInbox
	}
	if actor.Featured != "" {
		user.Featured = &actor.Featured
	}
	if actor.MovedTo != "" {
		user.MovedToURI = &actor.MovedTo
		user.MovedAt = &now
	}
	if len(actor.AlsoKnownAs) > 0 {
		aka := strings.Join(actor.AlsoKnownAs, ",")
		user.AlsoKnownAs = &aka
	}
	// actor.icon / actor.image はそれぞれアバター / バナー画像。空 URL や
	// icon自体が欠落している actor (Service 系など) もあるので nil チェック。
	if actor.Icon != nil && actor.Icon.URL != "" {
		avatarURL := actor.Icon.URL
		user.AvatarURL = &avatarURL
	}
	if actor.Image != nil && actor.Image.URL != "" {
		bannerURL := actor.Image.URL
		user.BannerURL = &bannerURL
	}
	// AP Person Tag配列からカスタム絵文字を抽出してDBにupsert
	user.Emojis = r.upsertEmojis(extractEmojiTags(actor.Tag), host)
	if err := r.userRepo.Create(user); err != nil {
		return nil, err
	}
	// remote actor の bio (= AP `summary` または `_misskey_summary`) を
	// `user_profile.description` に取り込む (#1022)。profile 行を作成しないと
	// 既存の Description 取得経路が常に NULL を返してしまい、frontend で
	// 自己紹介欄が空のまま表示される regression につながる。
	hostCopy := host
	profile := &model.UserProfile{
		UserID:      user.ID,
		Description: extractRemoteDescription(actor),
		UserHost:    &hostCopy,
	}
	if err := r.userRepo.CreateProfile(profile); err != nil {
		// profile 作成失敗時は user 取り込みは成立させる (= 次回 refreshActor
		// で back-fill する経路を維持)。production race / fixture 衝突など
		// transient な失敗時に actor resolve 自体まで道連れにしない。
		slog.Warn("create remote user profile failed", "userId", user.ID, "err", err)
	}
	r.cachePublicKey(user.ID, actor.PublicKey.ID, actor.PublicKey.PublicKeyPEM)
	r.cacheAssertionMethods(user.ID, actor.AssertionMethod)
	r.notifyInstance(host)
	if r.chartHook != nil {
		r.chartHook.OnRemoteUserCreated(user)
	}
	return user, nil
}

// extractRemoteDescription returns the actor's bio mapped to
// UserProfile.Description. upstream `ApPersonService` の logic と同じく
// `_misskey_summary` (MFM そのまま) を優先し、なければ AP `summary` (HTML
// or plain text) を採用する。upstream は htmlToMfm で MFM 変換してから保存
// するが、mk-go には未実装なので生 HTML をそのまま保存する (= frontend
// 側で render される、見た目は upstream と完全互換ではないが Empty 化は
// 回避される。HTML→MFM 変換は将来の別 issue で扱う)。
// varchar(2048) 制約を rune 単位で respect し、空は nil で表す。
func extractRemoteDescription(actor *activitypub.Person) *string {
	var raw string
	if actor.MisskeySummary != "" {
		raw = actor.MisskeySummary
	} else if actor.Summary != "" {
		raw = actor.Summary
	}
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
		r.refreshActor(existing, uri)
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
func (r *Resolver) refreshActor(existing *model.User, uri string) {
	actor, err := r.fetchActor(uri)
	if err != nil {
		return
	}
	now := r.clock()
	fields := map[string]any{
		"lastFetchedAt": &now,
	}
	// actor type が変わるケース (Person ↔ Service など) を反映する。常に上書きする
	// (TS Misskey も同様)。
	isBot := activitypub.IsBotActorType(actor.Type)
	fields["isBot"] = isBot
	existing.IsBot = isBot
	// manuallyApprovesFollowers の切り替えも追従する (リモート側でlock/unlock
	// された場合にローカルの判定もずれないように)。
	fields["isLocked"] = actor.ManuallyApproves
	existing.IsLocked = actor.ManuallyApproves
	if actor.Name != "" {
		name := actor.Name
		fields["name"] = &name
		existing.Name = &name
	}
	if actor.Inbox != "" {
		inbox := actor.Inbox
		fields["inbox"] = &inbox
		existing.Inbox = &inbox
	}
	if actor.Endpoints.SharedInbox != "" {
		shared := actor.Endpoints.SharedInbox
		fields["sharedInbox"] = &shared
		existing.SharedInbox = &shared
	}
	if actor.Featured != "" {
		featured := actor.Featured
		fields["featured"] = &featured
		existing.Featured = &featured
	}
	if actor.MovedTo != "" {
		movedTo := actor.MovedTo
		fields["movedToUri"] = &movedTo
		fields["movedAt"] = &now
		existing.MovedToURI = &movedTo
		existing.MovedAt = &now
	}
	if len(actor.AlsoKnownAs) > 0 {
		akaStr := strings.Join(actor.AlsoKnownAs, ",")
		fields["alsoKnownAs"] = &akaStr
		existing.AlsoKnownAs = &akaStr
	}
	// アバター / バナー画像のURLリモート側で変更された場合に追従する。
	// 他フィールドと同様、空値や欠落時は既存値を温存する (削除は追わない)。
	if actor.Icon != nil && actor.Icon.URL != "" {
		avatarURL := actor.Icon.URL
		fields["avatarUrl"] = &avatarURL
		existing.AvatarURL = &avatarURL
	}
	if actor.Image != nil && actor.Image.URL != "" {
		bannerURL := actor.Image.URL
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
	existing.LastFetchedAt = &now
	// UpdateUserエラーはベストエフォートで無視 (次回再試行される)
	_ = r.userRepo.UpdateUser(existing.ID, fields)
	// UserProfile.Description (#1022)。actor.Summary が変わった場合に追従する。
	// profile 行が無いケース (= 本 fix 以前に取り込まれた remote user) は
	// back-fill する。Update / Create のいずれも best-effort で fail しても
	// user 更新は維持する。
	desc := extractRemoteDescription(actor)
	if _, err := r.userRepo.FindProfileByUserID(existing.ID); err != nil {
		_ = r.userRepo.CreateProfile(&model.UserProfile{
			UserID:      existing.ID,
			Description: desc,
			UserHost:    existing.Host,
		})
	} else {
		_ = r.userRepo.UpdateProfile(existing.ID, map[string]any{"description": desc})
	}
	r.cachePublicKey(existing.ID, actor.PublicKey.ID, actor.PublicKey.PublicKeyPEM)
	r.cacheAssertionMethods(existing.ID, actor.AssertionMethod)
	if existing.Host != nil {
		r.notifyInstance(*existing.Host)
	}
}

// fetchActor fetches and decodes a remote actor document. Reject any
// document whose `type` is not in activitypub.ValidActorTypes — this guards
// against a non-Actor object (e.g. a Note) being interpreted as a Person.
func (r *Resolver) fetchActor(uri string) (*activitypub.Person, error) {
	body, err := r.fetcher.FetchObject(uri)
	if err != nil {
		return nil, err
	}
	var actor activitypub.Person
	if err := json.Unmarshal(body, &actor); err != nil {
		return nil, ErrInvalidActor
	}
	if actor.ID == "" || actor.PreferredUsername == "" {
		return nil, ErrInvalidActor
	}
	if !activitypub.IsValidActorType(actor.Type) {
		// Note/Activity 等 Actor でない object が actor として参照された場合の
		// 防衛策。デバッグのため URI と type を残してから拒否する。
		slog.Warn("federation: rejecting non-actor type", "uri", uri, "type", actor.Type)
		return nil, ErrInvalidActor
	}
	return &actor, nil
}

// refreshPublicKey fetches the actor again and caches its public key. エラー
// は呼び出し側でログするだけで上には伝搬しない。
func (r *Resolver) refreshPublicKey(userID, uri string) {
	actor, err := r.fetchActor(uri)
	if err != nil {
		return
	}
	r.cachePublicKey(userID, actor.PublicKey.ID, actor.PublicKey.PublicKeyPEM)
	r.cacheAssertionMethods(userID, actor.AssertionMethod)
}

// cachePublicKey stores a PEM in the in-memory cache and optionally persists
// it to the user_publickey table.
func (r *Resolver) cachePublicKey(userID, keyID, pem string) {
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

// cacheAssertionMethods parses FEP-521a Multikey entries from a remote actor's
// `assertionMethod[]` and upserts the recognised Ed25519 keys into
// user_publickey_extra (#1067 / #1070). decode 失敗・非 Ed25519 multicodec・
// その他不正 entry は warn log + skip して RSA only にフォールバックする。
// publickeyExtraRepo 未配線 (= 旧 deployment) のときは何もしない。
func (r *Resolver) cacheAssertionMethods(userID string, ams []activitypub.Multikey) {
	if r.publickeyExtraRepo == nil || len(ams) == 0 {
		return
	}
	for _, am := range ams {
		if am.Type != activitypub.MultikeyType || am.ID == "" || am.PublicKeyMultibase == "" {
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
		}
	}
}

// PublicKeyForKeyID returns the PEM-encoded public key whose keyId matches the
// given fragment URI. user_publickey_extra (Ed25519 / Multikey) を最初に探し、
// miss なら user_publickey の primary key (RSA) を返す fallback semantics。
// 受信した HTTP Signature の keyId fragment (e.g. `#main-key` / `#ed25519-key`)
// から正しい公開鍵を選ぶための path で、in-memory cache は介さない (= 永続層
// の最新値で verify する。assertionMethod が削除されたら次回 fetch で extra
// 行が古いままになる可能性はあるが、verify はそれでも通る = 後方互換)。
func (r *Resolver) PublicKeyForKeyID(actorID, keyID string) (string, error) {
	if r.publickeyExtraRepo != nil && keyID != "" {
		if row, err := r.publickeyExtraRepo.FindByKeyID(keyID); err == nil {
			return row.KeyPEM, nil
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
	// 同一 URI への並行呼び出しは singleflight で collapse (#300 3-7)。
	// ResolveActor と同じく cold path の HTTP fetch + IngestNote を重複
	// 実行しないことが目的。
	v, err, _ := r.resolveNoteGroup.Do(uri, func() (any, error) {
		return r.resolveNoteOnce(uri)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, ErrInvalidNote
	}
	return v.(*model.Note), nil
}

// resolveNoteOnce is the body of ResolveNote, invoked once per URI by
// singleflight.Do.
func (r *Resolver) resolveNoteOnce(uri string) (*model.Note, error) {
	if r.noteRepo == nil {
		return nil, ErrInvalidNote
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
	body, err := r.fetcher.FetchObject(uri)
	if err != nil {
		return nil, err
	}
	return r.IngestNote(body)
}

// IngestNote parses an ActivityStreams Note JSON and persists it as a local
// row authored by the (resolved) attributedTo actor. 既に同じ URI の note を
// 取り込み済みなら既存レコードを返す。
func (r *Resolver) IngestNote(body []byte) (*model.Note, error) {
	if r.noteRepo == nil {
		return nil, ErrInvalidNote
	}
	var apNote activitypub.Note
	if err := json.Unmarshal(body, &apNote); err != nil {
		return nil, ErrInvalidNote
	}
	if apNote.ID == "" || apNote.AttributedTo == "" {
		return nil, ErrInvalidNote
	}
	if existing, err := r.noteRepo.FindByURI(apNote.ID); err == nil {
		return existing, nil
	}
	actor, err := r.ResolveActor(apNote.AttributedTo)
	if err != nil {
		return nil, err
	}
	// 遅延配送 / origin downtime 後の bulk push などで note が遅れて届くケース
	// では AP の `published` を採用しないと timeline 上の並びが受信時刻順に
	// なってしまい remote とずれる (#940)。time-based ID (aidx 等) なので
	// idGen.Generate に published を渡せば自動的に tl 並びも remote 一致する。
	now := parseAPPublishedTime(apNote.Published, time.Now())
	noteURI := apNote.ID
	note := &model.Note{
		ID:         r.idGen.Generate(now),
		UserID:     actor.ID,
		UserHost:   actor.Host,
		URI:        &noteURI,
		Visibility: deriveVisibility(apNote.To, apNote.CC),
	}
	// TS本家ApNoteService.tsと同じ3段フォールバックでtext抽出:
	// 1. source.content (MFM) → 2. _misskey_content → 3. content (HTML→MFM変換)
	if apNote.Source != nil &&
		apNote.Source.MediaType == "text/x.misskeymarkdown" &&
		apNote.Source.Content != "" {
		text := apNote.Source.Content
		note.Text = &text
	} else if apNote.MisskeyContent != "" {
		text := apNote.MisskeyContent
		note.Text = &text
	} else if apNote.Content != "" {
		text := mfm.FromHTML(apNote.Content)
		if text != "" {
			note.Text = &text
		}
	}
	if apNote.Summary != "" {
		summary := apNote.Summary
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
		if id := r.extractLocalNoteID(apNote.InReplyTo); id != "" {
			if reply, err := r.noteRepo.FindByID(id); err == nil {
				replyTarget = reply
			}
		} else if reply, err := r.noteRepo.FindByURI(apNote.InReplyTo); err == nil {
			replyTarget = reply
		}
		if replyTarget != nil {
			note.ReplyID = &replyTarget.ID
			note.ReplyUserID = &replyTarget.UserID
			note.ReplyUserHost = replyTarget.UserHost
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
				if c == apNote.Name {
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
				return nil, nil
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
	tagMentions := r.resolveMentionedUserIDs(extractMentionTags(apNote.Tag))
	note.Mentions = mergeMentionIDs(textMentions, tagMentions)
	// upstream Misskey #17167 (= 2026.5.0 fix / triage #1004): mentionLimit を
	// 超える note は無効と扱い、保存せずに ErrContainsTooManyMentions を返す。
	// caller (processor.handleCreate) が当該 sentinel を catch して queue retry
	// 経路から除外することで、罠の inbox job が永続蓄積するのを防ぐ。
	// limit 値は corenote.DefaultMentionLimit (= 20) を参照し、local create
	// path (NoteCreateService.checkMentionLimit) と同じ policy を federation
	// 受信側にも適用する。
	if corenote.DefaultMentionLimit > 0 && len(note.Mentions) > corenote.DefaultMentionLimit {
		return nil, corenote.ErrContainsTooManyMentions
	}
	// specified visibility では AP `to` 配列が宛先 actor URI 列。CanView の
	// VisibleUserIDs チェック (core/note/visibility.go) で受信者が note を
	// 参照できるよう、ここで ID へ解決して埋める (#397)。
	if note.Visibility == model.NoteVisibilitySpecified {
		note.VisibleUserIDs = pq.StringArray(r.resolveMentionedUserIDs(apNote.To))
	}
	// AP Note Tag配列からカスタム絵文字を抽出してDBにupsert
	if actor.Host != nil {
		note.Emojis = r.upsertEmojis(extractEmojiTags(apNote.Tag), *actor.Host)
	}
	// hashtag は AP `tag` 配列の Hashtag entry と本文 / CW の両方から拾い、
	// hashtag.Extract で case-insensitive dedup + 長さ truncate を一括処理
	// する (#679)。`tag` 配列が空 / Hashtag を含まない実装 (古い Mastodon
	// 等) でも本文 fallback で trends 集計に乗るようにする。
	hashtagSources := extractHashtagTagNames(apNote.Tag)
	if note.Text != nil {
		hashtagSources = append(hashtagSources, *note.Text)
	}
	if note.CW != nil {
		hashtagSources = append(hashtagSources, *note.CW)
	}
	if tags := hashtag.Extract(hashtagSources...); len(tags) > 0 {
		note.Tags = pq.StringArray(tags)
	}
	// AP `attachment` 配列を drive_file 行に upsert (#378)。link 形式のみで
	// 実 fetch はせず、frontend が drive_file.url 経由で remote 取得する。
	note.FileIDs = r.upsertAttachments(extractAttachments(apNote.Attachment), &actor.ID, actor.Host)
	if len(note.FileIDs) > 0 {
		// AttachedFileTypes は MIME type の配列 (TS との互換性)。
		note.AttachedFileTypes = r.collectAttachedFileTypes(note.FileIDs)
	}
	// Question（投票）の場合はhasPollフラグをCreate前に設定
	if len(apNote.OneOf) > 0 || len(apNote.AnyOf) > 0 {
		note.HasPoll = true
	}
	if err := r.noteRepo.Create(note); err != nil {
		return nil, err
	}
	// ローカルノートへの返信の場合、repliesCount を増やす。
	// これにより timeline や API 上の「返信数」表示が federated reply も
	// 含むようになる。失敗はベストエフォートで無視。
	if note.ReplyID != nil {
		_ = r.noteRepo.IncrementCount(*note.ReplyID, "repliesCount", 1)
	}
	// Question (投票) の処理: oneOf/anyOf から Poll レコードを作成
	if r.pollRepo != nil && note.HasPoll {
		r.createPollFromQuestion(note, &apNote)
	}
	// hashtag table の mentionedUsersCount / userIds 更新 (#680 / #719)。
	// hook 実装 (core/hashtag.Service) が内部で goroutine を起こす
	// fire-and-forget 設計なので、IngestNote から見ると即時 return する。
	// inbox processor の drain time が tag 数に比例して伸びる退行を回避。
	if r.hashtagHook != nil && len(note.Tags) > 0 {
		r.hashtagHook.OnNoteCreated(note, actor)
	}
	return note, nil
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
		if t, err := time.Parse(time.RFC3339, apNote.EndTime); err == nil {
			poll.ExpiresAt = &t
		}
	} else if apNote.Closed != "" {
		if t, err := time.Parse(time.RFC3339, apNote.Closed); err == nil {
			poll.ExpiresAt = &t
		}
	}
	_ = r.pollRepo.Create(poll)
}

// UpdateRemoteNote applies an inbound Update Note activity body to an existing
// remote-authored note row. Misskey 本家にはノート編集機能が無いためローカル
// note は不変だが、Mastodon 等から push されてくる Update activity は受信
// するだけ受信して反映する。
//
// 動作:
//   - URI でノートを引いて見つからなければ何もせず nil
//   - 見つかったが著者がローカルなら何もしない (ローカルノートは変更不可)
//   - 著者がリモートなら text/cw/sensitive/mentions を更新
func (r *Resolver) UpdateRemoteNote(body []byte) (*model.Note, error) {
	if r.noteRepo == nil {
		return nil, ErrInvalidNote
	}
	var apNote activitypub.Note
	if err := json.Unmarshal(body, &apNote); err != nil {
		return nil, ErrInvalidNote
	}
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
	fields := map[string]any{}
	// IngestNoteと同じ3段フォールバックでtext抽出
	var newText string
	if apNote.Source != nil &&
		apNote.Source.MediaType == "text/x.misskeymarkdown" &&
		apNote.Source.Content != "" {
		newText = apNote.Source.Content
	} else if apNote.MisskeyContent != "" {
		newText = apNote.MisskeyContent
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
		summary := apNote.Summary
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
	newTags := hashtag.Extract(hashtagSources...)
	tagsChanged := !slices.Equal([]string(existing.Tags), newTags)
	if tagsChanged {
		fields["tags"] = pq.StringArray(newTags)
		existing.Tags = pq.StringArray(newTags)
	}
	// AP `attachment` 配列の差分を反映する (#378)。driveFileRepo 未設定時は
	// upsertAttachments が空 slice を返すので何もしない (= 既存 fileIDs を
	// 誤って空に上書きしない、Devin #400 #1)。
	if r.driveFileRepo != nil {
		fileIDs := r.upsertAttachments(extractAttachments(apNote.Attachment), &existing.UserID, existing.UserHost)
		if !slices.Equal([]string(existing.FileIDs), []string(fileIDs)) {
			fields["fileIds"] = pq.StringArray(fileIDs)
			existing.FileIDs = pq.StringArray(fileIDs)
			types := r.collectAttachedFileTypes(fileIDs)
			fields["attachedFileTypes"] = pq.StringArray(types)
			existing.AttachedFileTypes = pq.StringArray(types)
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
func mergeMentionIDs(a, b []string) pq.StringArray {
	out := make(pq.StringArray, 0, len(a)+len(b))
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
		if typ, _ := m["type"].(string); typ != "Mention" {
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
		if typ, _ := m["type"].(string); typ != "Hashtag" {
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
		typ, _ := m["type"].(string)
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
			Icon:    activitypub.Image{Type: "Image", URL: iconURL},
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
func (r *Resolver) upsertEmojis(tags []activitypub.EmojiTag, host string) pq.StringArray {
	if r.emojiRepo == nil || len(tags) == 0 {
		return pq.StringArray{}
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
		return pq.StringArray{}
	}

	existingRows, err := r.emojiRepo.FindManyByNamesAndHost(order, &host)
	if err != nil {
		return pq.StringArray{}
	}
	existingByName := make(map[string]*model.Emoji, len(existingRows))
	for _, e := range existingRows {
		existingByName[e.Name] = e
	}

	names := make(pq.StringArray, 0, len(order))
	for _, name := range order {
		tag := tagByName[name]
		if existing, ok := existingByName[name]; ok {
			// 既存絵文字: URL/URIが変わっていればまとめてupdate
			updates := map[string]any{}
			if existing.OriginalURL != tag.Icon.URL {
				updates["originalUrl"] = tag.Icon.URL
				updates["publicUrl"] = tag.Icon.URL
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
			OriginalURL: tag.Icon.URL,
			PublicURL:   tag.Icon.URL,
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

// deriveVisibility maps an AS to/cc audience pair to a Misskey visibility.
//
//   - to に Public があれば public
//   - cc に Public があり to に followers があれば home
//   - to に followers のみ (Public 無し) なら followers
//   - それ以外 (specific actor 列挙) は specified
func deriveVisibility(to, cc []string) model.NoteVisibility {
	hasFollowers := func(list []string) bool {
		for _, v := range list {
			if strings.HasSuffix(v, "/followers") {
				return true
			}
		}
		return false
	}
	if slices.Contains(to, activitypub.Public) {
		return model.NoteVisibilityPublic
	}
	if slices.Contains(cc, activitypub.Public) && hasFollowers(to) {
		return model.NoteVisibilityHome
	}
	if hasFollowers(to) {
		return model.NoteVisibilityFollowers
	}
	return model.NoteVisibilitySpecified
}

// hostFromURI extracts the host portion from an actor URI.
func hostFromURI(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host in %q", uri)
	}
	return u.Host, nil
}

// extractAttachments parses the AP `attachment` array (heterogeneous []any
// after JSON unmarshal) and returns Document entries. type が "Document" /
// "Image" / "Audio" / "Video" のいずれかで `url` を持つもののみ採用する。
// #378。
func extractAttachments(rawAttachments []any) []activitypub.Document {
	if len(rawAttachments) == 0 {
		return nil
	}
	out := make([]activitypub.Document, 0, len(rawAttachments))
	for _, raw := range rawAttachments {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		switch typ {
		case "Document", "Image", "Audio", "Video":
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
		sensitive, _ := m["sensitive"].(bool)
		// width / height / blurhash / icon は省略実装が多いので欠落時は
		// zero value のまま。upsertAttachments 側で 0/空チェックして
		// DriveFile に書き込むかを判断する (#460/#461)。
		width := numberAsInt(m["width"])
		height := numberAsInt(m["height"])
		blurhash, _ := m["_misskey_blurhash"].(string)
		var icon *activitypub.Image
		if iconMap, ok := m["icon"].(map[string]any); ok {
			iconURL, _ := iconMap["url"].(string)
			iconType, _ := iconMap["type"].(string)
			if iconURL != "" {
				icon = &activitypub.Image{Type: iconType, URL: iconURL}
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
// driveFileRepo が未設定なら空 (pq.StringArray{}) を返す (旧挙動)。userID は
// リモート user の ID (note.UserID 相当)、host はリモート host (nil =
// ローカル、attachment 文脈ではほぼ常に non-nil)。
func (r *Resolver) upsertAttachments(docs []activitypub.Document, userID, host *string) pq.StringArray {
	if r.driveFileRepo == nil || len(docs) == 0 {
		return pq.StringArray{}
	}
	ids := make(pq.StringArray, 0, len(docs))
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
			thumb := doc.Icon.URL
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
func (r *Resolver) collectAttachedFileTypes(fileIDs []string) pq.StringArray {
	if r.driveFileRepo == nil || len(fileIDs) == 0 {
		return pq.StringArray{}
	}
	files, err := r.driveFileRepo.FindByIDs(fileIDs)
	if err != nil {
		return pq.StringArray{}
	}
	// FindByIDs の戻り順は不定なので id → type のマップで再整列する。
	byID := make(map[string]string, len(files))
	for _, f := range files {
		byID[f.ID] = f.Type
	}
	out := make(pq.StringArray, 0, len(fileIDs))
	for _, id := range fileIDs {
		if t, ok := byID[id]; ok {
			out = append(out, t)
		}
	}
	return out
}
