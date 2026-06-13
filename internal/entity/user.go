package entity

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/datatypes"
)

// upstream const.ts: USER_ONLINE_THRESHOLD=10min, USER_ACTIVE_THRESHOLD=3days。
const (
	userOnlineThreshold = 10 * time.Minute
	userActiveThreshold = 3 * 24 * time.Hour
)

// computeOnlineStatus mirrors upstream UserEntityService.getOnlineStatus:
// hideOnlineStatus or no lastActiveDate → "unknown"; otherwise online/active/
// offline by elapsed time since lastActiveDate.
func computeOnlineStatus(u *model.User) string {
	if u.HideOnlineStatus || u.LastActiveDate == nil {
		return "unknown"
	}
	elapsed := time.Since(*u.LastActiveDate)
	switch {
	case elapsed < userOnlineThreshold:
		return "online"
	case elapsed < userActiveThreshold:
		return "active"
	default:
		return "offline"
	}
}

// UserLite is the minimal user representation returned by most API endpoints.
// Phase 7-5a (#247) added requireSigninToViewContents, makeNotes*Before and
// instance as optional TS-compat fields. All use omitempty so absent values
// are elided from the response (TS: `?? undefined` / `?: undefined`).
type UserLite struct {
	ID       string  `json:"id"`
	Name     *string `json:"name"`
	Username string  `json:"username"`
	Host     *string `json:"host"`
	// AvatarURL は upstream Misskey TS 互換で常に non-null。
	// avatarId が無い / DB の avatarUrl が空のときは IdenticonURL() が
	// /identicon/<username>(@<host>) にフォールバックする (#710)。
	AvatarURL         string                 `json:"avatarUrl"`
	AvatarBlurhash    *string                `json:"avatarBlurhash"`
	AvatarDecorations []AvatarDecorationItem `json:"avatarDecorations"`
	IsBot             bool                   `json:"isBot"`
	IsCat             bool                   `json:"isCat"`
	Emojis            map[string]string      `json:"emojis"`
	OnlineStatus      string                 `json:"onlineStatus"`
	BadgeRoles        []any                  `json:"badgeRoles"`
	// CanChat は upstream Misskey TS の boolean field (#692)。FE の
	// /chat/room.vue が `!user.canChat` で「DM 受け付け不可」warning を
	// 出すので、出さないと local user 同士で DM できないと誤表示される。
	// upstream 互換で role policy の `chatAvailability === "available"`
	// 由来で計算する (#988)。PackUserLite が resolveCanChat() 経由で
	// CanChatLookup を呼び、router で wired される。lookup が unwired
	// の path (= 単体テスト等) は default `true` で fallback。
	CanChat bool `json:"canChat"`
	// Optional TS-compat fields (Phase 7-5a)。
	// TS側は `requireSigninToViewContents: user.x === false ? undefined : true`
	// なので、値が true のときのみ expose する (*bool を &true に設定、
	// false は nil のまま)。
	RequireSigninToViewContents  *bool         `json:"requireSigninToViewContents,omitempty"`
	MakeNotesFollowersOnlyBefore *int          `json:"makeNotesFollowersOnlyBefore,omitempty"`
	MakeNotesHiddenBefore        *int          `json:"makeNotesHiddenBefore,omitempty"`
	Instance                     *InstanceLite `json:"instance,omitempty"`
}

// UserDetailed includes additional fields for detailed user views.
//
// 注: avatarId / bannerId は **MeDetailed 専用** (self view のみ) で、MeDetailed
// struct 側に持つ。misskey_dart の UserDetailed union dispatch は `avatarId` key
// の存在で MeDetailed を判別する (`containsKey("avatarId") → MeDetailed`) ため、
// 非self の UserDetailed に avatarId を出すと MeDetailed と誤判定されて crash する
// (#1251)。よって base UserDetailed には含めない。
type UserDetailed struct {
	UserLite
	BannerURL      *string `json:"bannerUrl"`
	BannerBlurhash *string `json:"bannerBlurhash"`
	IsLocked       bool    `json:"isLocked"`
	IsSilenced     bool    `json:"isSilenced"`
	IsSuspended    bool    `json:"isSuspended"`
	Description    *string `json:"description"`
	Location       *string `json:"location"`
	Birthday       *string `json:"birthday"`
	Lang           *string `json:"lang"`
	// FollowedMessage は follower / self だけに見せる private 文字列なので
	// omitempty で「非 follower には省略」を表現する。packer はこの field を
	// set せず、self は AsMeDetailed、follower は users/show handler が set する
	// (upstream UserEntityService の relation/isMe ブロックでのみ emit、#1558)。
	FollowedMessage     *string        `json:"followedMessage,omitempty"`
	PublicReactions     bool           `json:"publicReactions"`
	Fields              datatypes.JSON `json:"fields"`
	VerifiedLinks       []string       `json:"verifiedLinks"`
	FollowersCount      int            `json:"followersCount"`
	FollowingCount      int            `json:"followingCount"`
	NotesCount          int            `json:"notesCount"`
	FollowersVisibility string         `json:"followersVisibility"`
	FollowingVisibility string         `json:"followingVisibility"`
	// ChatScope は 1-on-1 チャットの受信許可レベル (#692)。FE の
	// /settings/privacy が `i/update` レスポンスから直接 `$i.chatScope` に
	// 反映するため、この field を expose しないと UI が保存後に古い値で
	// 再描画される (DB は更新されているのに UI が "保存されない" と見える)。
	ChatScope     string  `json:"chatScope"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     *string `json:"updatedAt"`
	LastFetchedAt *string `json:"lastFetchedAt"`
	// MovedTo / AlsoKnownAs はアカウント移行 (Mastodon 互換 Move) のフィールド。
	// upstream UserEntityService は movedToUri / alsoKnownAs (URI 群) をローカル
	// ユーザー ID に解決した上で返す (resolvePerson(...).then(u => u.id))。
	// pure packer は repository アクセスを持たないので null のまま残し、repo を
	// 持つ handler が ResolveMoveTargets で URI→ID 解決して埋める。解決不能な
	// URI は null になる (golden: movedTo string|null / alsoKnownAs string[]|null)。
	MovedTo       *string  `json:"movedTo"`
	AlsoKnownAs   []string `json:"alsoKnownAs"`
	URI           *string  `json:"uri"`
	URL           *string  `json:"url"`
	PinnedNoteIDs []string `json:"pinnedNoteIds"`
	PinnedNotes   []any    `json:"pinnedNotes"`
	// PinnedPageID is the user_profile.pinnedPageId. PinnedPage is the fully
	// packed page object corresponding to that id (nil when the user has no
	// pinned page or the page has been deleted). Both are populated by the
	// caller (handler) since packing requires PageRepository lookup.
	PinnedPageID *string `json:"pinnedPageId"`
	PinnedPage   any     `json:"pinnedPage"`
	Roles        []any   `json:"roles"`
	// viewer依存フィールド (ハンドラ側でセット)。viewer===target の self-view や
	// 未認証では relation を計算せず nil のままになる。upstream Misskey は self/
	// no-me のとき relation フィールド一式を省略する (`detail && meId !== userId`)
	// ため、ここも omitempty で揃える。omitempty が無いと nil が `null` として
	// 出力され、misskey_dart の UserDetailedNotMeWithRelations が
	// `isFollowing as bool` で null cast に失敗する (#1228)。
	IsFollowing                    *bool   `json:"isFollowing,omitempty"`
	IsFollowed                     *bool   `json:"isFollowed,omitempty"`
	IsBlocking                     *bool   `json:"isBlocking,omitempty"`
	IsBlocked                      *bool   `json:"isBlocked,omitempty"`
	IsMuted                        *bool   `json:"isMuted,omitempty"`
	IsRenoteMuted                  *bool   `json:"isRenoteMuted,omitempty"`
	HasPendingFollowRequestFromYou *bool   `json:"hasPendingFollowRequestFromYou,omitempty"`
	HasPendingFollowRequestToYou   *bool   `json:"hasPendingFollowRequestToYou,omitempty"`
	Notify                         *string `json:"notify,omitempty"`
	WithReplies                    *bool   `json:"withReplies,omitempty"`
	// Memo は viewer 自身が target に付けたメモ。golden UserDetailedNotMeOnly は
	// `memo: string | null` (必須・null 許容)。MeDetailed も合成型
	// `UserLite & UserDetailedNotMeOnly & MeDetailedOnly` で memo を含むため、
	// not-me / self どちらの view でも memo は present 必須。よって上の relation
	// bool 群 (golden optional) と違い omitempty を付けず、未設定時は null を出す。
	// #1228 の null-cast crash は非 null bool (`isFollowing as bool`) の話で、
	// nullable string の memo には当てはまらない。
	Memo *string `json:"memo"`
	// ModerationNote は moderator viewer のみに見える profile.moderationNote
	// (upstream UserEntityService.ts:574: iAmModerator ? note ?? '' : undefined)。
	// 非 moderator には omit する (#1547)。handler 側で viewer の role を見て set。
	ModerationNote *string `json:"moderationNote,omitempty"`
}

// MeDetailed extends UserDetailed with fields that upstream Misskey TS
// exposes only when the viewer is the user themselves (i.e. /api/i,
// /api/i/update, and meUpdated stream events). These fields originate
// from UserEntityService.ts:590-630 (`MeDetailed` shape).
//
// Privacy boundary: do NOT use PackMeDetailed for /api/users/show or
// any "viewing another user" path — the fields below are deliberately
// scoped to self-view (#968 / sibling of #693 ChatScope drift).
type MeDetailed struct {
	UserDetailed
	// FollowedMessage は MeDetailed では required (self は isMe ブロックで必ず
	// emit、null 可)。base UserDetailed 側は omitempty で「非 follower には省略」
	// を表すため、MeDetailed では omitempty 無しの field で shadow して常に出す
	// (#1558)。encoding/json は浅い深さの本 field を優先する。
	FollowedMessage *string `json:"followedMessage"`
	// avatarId / bannerId は self view 専用 (#1251)。misskey_dart の dispatch が
	// `avatarId` key の存在で MeDetailed を判別するため、MeDetailed のみが出す。
	AvatarID                 *string `json:"avatarId"`
	BannerID                 *string `json:"bannerId"`
	IsExplorable             bool    `json:"isExplorable"`
	IsDeleted                bool    `json:"isDeleted"`
	HideOnlineStatus         bool    `json:"hideOnlineStatus"`
	NoCrawle                 bool    `json:"noCrawle"`
	PreventAiLearning        bool    `json:"preventAiLearning"`
	AutoSensitive            bool    `json:"autoSensitive"`
	CarefulBot               bool    `json:"carefulBot"`
	AutoAcceptFollowed       bool    `json:"autoAcceptFollowed"`
	AlwaysMarkNsfw           bool    `json:"alwaysMarkNsfw"`
	ReceiveAnnouncementEmail bool    `json:"receiveAnnouncementEmail"`
	InjectFeaturedNote       bool    `json:"injectFeaturedNote"`
	// 以下は misskey_dart の MeDetailed.fromJson が非null bool として cast する
	// ため、self-view (users/show me) でも必ず値を出す必要がある (#1237)。
	// /api/i は handler 層でこれらを正確な値に上書きするが、users/show の
	// self-view は AsMeDetailed をそのまま返すので struct 側で埋める。
	// TwoFactorEnabled / UsePasswordLessLogin / SecurityKeys は profile 由来で
	// 正確に埋まる。IsModerator / IsAdmin / HasUnread* /
	// HasPendingReceivedFollowRequest は users/show では権威ソースでないため
	// best-effort で zero (false) に倒す (権威値は /api/i 経由)。
	TwoFactorEnabled                bool `json:"twoFactorEnabled"`
	UsePasswordLessLogin            bool `json:"usePasswordLessLogin"`
	SecurityKeys                    bool `json:"securityKeys"`
	IsModerator                     bool `json:"isModerator"`
	IsAdmin                         bool `json:"isAdmin"`
	HasUnreadNotification           bool `json:"hasUnreadNotification"`
	HasUnreadMentions               bool `json:"hasUnreadMentions"`
	HasUnreadAnnouncement           bool `json:"hasUnreadAnnouncement"`
	HasUnreadAntenna                bool `json:"hasUnreadAntenna"`
	HasUnreadChannel                bool `json:"hasUnreadChannel"`
	HasUnreadSpecifiedNotes         bool `json:"hasUnreadSpecifiedNotes"`
	HasPendingReceivedFollowRequest bool `json:"hasPendingReceivedFollowRequest"`
	HasUnreadChatMessages           bool `json:"hasUnreadChatMessages"`
	// UnreadNotificationsCount は未読通知件数。bool フラグ同様 users/show では
	// best-effort 0 で、権威値は /api/i / i/update / meUpdated 経由 (fillUnreadFields
	// が実値で埋める)。
	UnreadNotificationsCount int `json:"unreadNotificationsCount"`
	// TwoFactorBackupCodesStock は 2FA バックアップコード残量 (full/partial/none)。
	// profile 由来なので AsMeDetailed が計算し全 view で正しい値になる (clobber 無し)。
	TwoFactorBackupCodesStock string `json:"twoFactorBackupCodesStock"`
	// 以下も misskey_dart の MeDetailed.fromJson が非null List / num / Map と
	// して cast するため self-view でも必ず値を出す (#1240)。MutedWords /
	// MutedInstances / Achievements / LoggedInDays は profile 由来で正確に、
	// Policies は role 依存のため handler 層で best-effort default を入れる
	// (権威値は /api/i 経由)。
	MutedWords     []any    `json:"mutedWords"`
	MutedInstances []string `json:"mutedInstances"`
	Achievements   []any    `json:"achievements"`
	LoggedInDays   int      `json:"loggedInDays"`
	// HardMutedWords は profile 由来 (string[][])。MutedWords と同様 jsonb から
	// 展開する (毎 pack 正しい値)。UnreadAnnouncements は未読アナウンス配列で、
	// users/show では best-effort [] / 権威値は fillUnreadFields。
	HardMutedWords      []any `json:"hardMutedWords"`
	UnreadAnnouncements []any `json:"unreadAnnouncements"`
	// Policies は role 依存で AsMeDetailed (entity 層) では nil のまま。omitempty で
	// 「未設定なら省略」にするのが重要 (#1240): PackMeDetailed を override 無しで
	// 直接返す i/update / meUpdated 経路で nil を `null` として出すと frontend の
	// updateCurrentAccountPartial が `$i.policies` を null 上書きして policy 判定が
	// 壊れる。users/show self-view は handler が DefaultPolicies() を明示セットする
	// ので含まれ、/api/i は handler が実 policies で override する。
	Policies map[string]any `json:"policies,omitempty"`
	// EmailNotificationTypes is the list of email-notification categories the
	// user opted in for. Defaults to ["follow", "receiveFollowRequest"] when
	// the profile column is absent (#985).
	EmailNotificationTypes []string `json:"emailNotificationTypes"`
	// MutingNotificationTypes is kept as an empty slice for backward
	// compatibility — upstream UserEntityService.ts:615 returns `[]` with
	// a "後方互換性のため" comment (#985).
	MutingNotificationTypes []string `json:"mutingNotificationTypes"`
	// NotificationRecieveConfig retains the (sic) upstream typo so frontends
	// can read state without renaming. Empty map when unset (#985).
	NotificationRecieveConfig map[string]any `json:"notificationRecieveConfig"`
}

// EnsureRelationFlags fills any nil viewer-relation bool with false **only when
// IsFollowing is already set**. misskey_dart の UserDetailed union dispatch は
// `isFollowing` の存在で UserDetailedNotMeWithRelations を選び、その variant は
// isFollowed / hasPendingFollowRequestFromYou / hasPendingFollowRequestToYou /
// isBlocking / isBlocked / isMuted / isRenoteMuted も非null bool として cast
// するため、一部しかセットしないと `Null is not bool` で落ちる (#1249)。
// relation を部分的にしか計算しない経路 (follow/follower list 等) で本 helper を
// 呼び、未計算 flag を false に倒して shape を完備する。
func (d *UserDetailed) EnsureRelationFlags() {
	if d.IsFollowing == nil {
		return
	}
	f := false
	if d.IsFollowed == nil {
		d.IsFollowed = &f
	}
	if d.HasPendingFollowRequestFromYou == nil {
		d.HasPendingFollowRequestFromYou = &f
	}
	if d.HasPendingFollowRequestToYou == nil {
		d.HasPendingFollowRequestToYou = &f
	}
	if d.IsBlocking == nil {
		d.IsBlocking = &f
	}
	if d.IsBlocked == nil {
		d.IsBlocked = &f
	}
	if d.IsMuted == nil {
		d.IsMuted = &f
	}
	if d.IsRenoteMuted == nil {
		d.IsRenoteMuted = &f
	}
}

// PackMeDetailed packs a self-view user payload, extending PackUserDetailed
// with MeDetailed fields. Self-mutation handlers (i/update, i/2fa/*)
// should use this instead of PackUserDetailed so frontend's
// updateCurrentAccountPartial receives the full $i shape; otherwise
// privacy-scoped fields like isExplorable / noCrawle stay stale in the
// session (#968).
func PackMeDetailed(u *model.User, profile *model.UserProfile, idGens ...id.Generator) MeDetailed {
	return AsMeDetailed(PackUserDetailed(u, profile, idGens...), u, profile)
}

// AsMeDetailed promotes a pre-built UserDetailed payload into MeDetailed by
// layering on the self-view-only fields sourced from User + UserProfile.
// Used directly by users/show (#970) when viewer===target to mirror
// upstream's `pack(user, me)` semantics, and indirectly by PackMeDetailed
// which builds a fresh UserDetailed first.
//
// Why a separate helper: users/show already does additional work on the
// pre-built UserDetailed (remote stats / instance / emojis / pinned /
// viewer-dependent fields). Re-running PackUserDetailed would lose that
// work, so we promote the existing UserDetailed in-place.
func AsMeDetailed(d UserDetailed, u *model.User, profile *model.UserProfile) MeDetailed {
	out := MeDetailed{
		UserDetailed: d,
		// avatarId / bannerId は self view 専用 (#1251)。base UserDetailed には
		// 出さず MeDetailed (= /api/i / users/show self) でのみ u から set する。
		AvatarID:         u.AvatarID,
		BannerID:         u.BannerID,
		IsExplorable:     u.IsExplorable,
		IsDeleted:        u.IsDeleted,
		HideOnlineStatus: u.HideOnlineStatus,
		// default は upstream UserEntityService と同じ値。profile が
		// non-nil なら下で JSON column から上書きする。
		EmailNotificationTypes:    []string{"follow", "receiveFollowRequest"},
		MutingNotificationTypes:   []string{},
		NotificationRecieveConfig: map[string]any{},
		// 非null List 必須 (#1240)。profile があれば下で jsonb から上書き。
		MutedWords:     []any{},
		MutedInstances: []string{},
		Achievements:   []any{},
		// HardMutedWords / TwoFactorBackupCodesStock は profile 由来なので下で
		// 正確に埋める。UnreadAnnouncements は runtime 値で handler が上書きする
		// (users/show は best-effort [])。いずれも非null 必須 (#1258 follow-up)。
		HardMutedWords:            []any{},
		UnreadAnnouncements:       []any{},
		TwoFactorBackupCodesStock: "none",
	}
	if profile != nil {
		// self は upstream の isMe ブロックで followedMessage を必ず見られる
		// (#1558)。base packer は set しないので MeDetailed で埋め直す。
		out.FollowedMessage = profile.FollowedMessage
		out.NoCrawle = profile.NoCrawle
		out.PreventAiLearning = profile.PreventAiLearning
		out.AutoSensitive = profile.AutoSensitive
		out.CarefulBot = profile.CarefulBot
		out.AutoAcceptFollowed = profile.AutoAcceptFollowed
		out.AlwaysMarkNsfw = profile.AlwaysMarkNsfw
		out.ReceiveAnnouncementEmail = profile.ReceiveAnnouncementEmail
		out.InjectFeaturedNote = profile.InjectFeaturedNote
		// security 系は profile から正確に埋める (#1237)。
		out.TwoFactorEnabled = profile.TwoFactorEnabled
		out.UsePasswordLessLogin = profile.UsePasswordLessLogin
		out.SecurityKeys = profile.SecurityKeysAvailable
		// twoFactorBackupCodesStock は profile 由来なので全 view で正しい値にする
		// (handler override 不要 = meUpdated / i-update でも clobber しない)。
		out.TwoFactorBackupCodesStock = backupCodesStock(profile)
		// EmailNotificationTypes / NotificationRecieveConfig は jsonb カラム
		// なので JSON unmarshal してから out にコピーする。parse error は
		// default に倒す (silent — frontend は default で動作可能)。
		if raw := []byte(profile.EmailNotificationTypes); len(raw) > 0 {
			var arr []string
			if err := json.Unmarshal(raw, &arr); err == nil {
				out.EmailNotificationTypes = arr
			}
		}
		if raw := []byte(profile.NotificationRecieveConfig); len(raw) > 0 {
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err == nil && m != nil {
				out.NotificationRecieveConfig = m
			}
		}
		// mutedWords / mutedInstances / achievements は jsonb 配列。空でも
		// null ではなく [] を保つ (#1240)。parse error も default の [] のまま。
		if arr := unmarshalJSONAnySlice(profile.HardMutedWords); arr != nil {
			out.HardMutedWords = arr
		}
		if arr := unmarshalJSONAnySlice(profile.MutedWords); arr != nil {
			out.MutedWords = arr
		}
		if raw := []byte(profile.MutedInstances); len(raw) > 0 {
			var arr []string
			if err := json.Unmarshal(raw, &arr); err == nil && arr != nil {
				out.MutedInstances = arr
			}
		}
		if arr := unmarshalJSONAnySlice(profile.Achievements); arr != nil {
			out.Achievements = arr
		}
		out.LoggedInDays = len(profile.LoggedInDates)
	}
	return out
}

// unmarshalJSONAnySlice decodes a jsonb array column into []any, returning nil
// on empty input or parse error so the caller keeps its non-null default.
func unmarshalJSONAnySlice(raw []byte) []any {
	if len(raw) == 0 {
		return nil
	}
	var arr []any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	return arr
}

// InstanceLite is the minimal instance info embedded in UserLite for remote
// users. Populated by the caller from InstanceRepository when needed; packers
// keep it nil to avoid DB access on hot paths.
type InstanceLite struct {
	Name            *string `json:"name"`
	SoftwareName    *string `json:"softwareName"`
	SoftwareVersion *string `json:"softwareVersion"`
	IconURL         *string `json:"iconUrl"`
	FaviconURL      *string `json:"faviconUrl"`
	ThemeColor      *string `json:"themeColor"`
}

// IdenticonURL returns the avatar URL for u, falling back to the local
// `/identicon/<username>(@<host>)` route when the user has no explicit
// avatarUrl. Mirrors upstream Misskey TS の identicon URL 規則。
//
// 共有 helper として切り出してあるので、UserLite 以外のレスポンス
// (e.g. internal/api/chat の packUser / internal/core/chat の packUserStream)
// も同じ規則を使える (#692 / #708 review)。
func IdenticonURL(u *model.User) string {
	if u.AvatarURL != nil && *u.AvatarURL != "" {
		// remote user の avatarUrl は remote origin を指すため proxy 経由に
		// 書き換えて閲覧者の IP 漏洩を防ぐ (#1529)。local / identicon は素通し
		// (ProxyAvatarURL は同一オリジン・相対 URL を wrap しない)。
		return currentMediaURLContext().ProxyAvatarURL(*u.AvatarURL)
	}
	host := ""
	if u.Host != nil {
		host = "@" + *u.Host
	}
	return "/identicon/" + u.Username + host
}

// PackUserLite converts a model.User to a UserLite DTO.
// Instance (nested remote instance info) must be pre-fetched by the caller
// via InstanceRepository and assigned to the returned UserLite.Instance.
// PackUserLite itself performs no DB access on the steady-state hot path
// (designed for timeline packing). 例外として avatarDecorations の url 解決
// は entity.SetAvatarDecorationLookup() 経由の resolver を引く — 通常実装は
// 30s TTL の in-memory cache (core/avatardecoration.Resolver) で hit 時は
// DB を叩かない。cache miss / TTL 切れでのみ admin catalog の List を 1 回
// 引く (#521 / #524 review)。
func PackUserLite(u *model.User) UserLite {
	out := UserLite{
		ID:                u.ID,
		Name:              u.Name,
		Username:          u.Username,
		Host:              u.Host,
		AvatarURL:         IdenticonURL(u),
		AvatarBlurhash:    u.AvatarBlurhash,
		AvatarDecorations: resolveAvatarDecorations(u.AvatarDecorations),
		IsBot:             u.IsBot,
		IsCat:             u.IsCat,
		Emojis:            make(map[string]string),
		OnlineStatus:      computeOnlineStatus(u),
		BadgeRoles:        packBadgeRoles(u),
		// upstream `chatAvailability === 'available'` 互換 (#988)。
		// 旧実装は `u.ChatScope != "none"` で user 自身の受信設定を返して
		// いたが、upstream は role policy の chatAvailability を見る
		// (admin が role に設定する権限)。lookup が wire されていない
		// path (= 既存 unit test 等) は default true で fallback する。
		CanChat: resolveCanChat(u.ID),
	}
	// requireSigninToViewContents: true のときだけ出す (TS は false→undefined)
	if u.RequireSigninToViewContents {
		tr := true
		out.RequireSigninToViewContents = &tr
	}
	// makeNotes*Before: 設定値があればそのまま出す (nil→omit)
	if u.MakeNotesFollowersOnlyBefore != nil {
		out.MakeNotesFollowersOnlyBefore = u.MakeNotesFollowersOnlyBefore
	}
	if u.MakeNotesHiddenBefore != nil {
		out.MakeNotesHiddenBefore = u.MakeNotesHiddenBefore
	}
	return out
}

// PackUserDetailed converts a model.User and optional profile to UserDetailed.
func PackUserDetailed(u *model.User, profile *model.UserProfile, idGens ...id.Generator) UserDetailed {
	d := UserDetailed{
		UserLite: PackUserLite(u),
		// remote user の bannerUrl も remote origin を指すため proxy 経由に
		// 書き換える (#1529)。local は素通し。
		BannerURL:           currentMediaURLContext().ProxyBannerURL(u.BannerURL),
		BannerBlurhash:      u.BannerBlurhash,
		IsLocked:            u.IsLocked,
		IsSuspended:         u.IsSuspended,
		FollowersCount:      u.FollowersCount,
		FollowingCount:      u.FollowingCount,
		NotesCount:          u.NotesCount,
		Fields:              datatypes.JSON([]byte("[]")),
		VerifiedLinks:       []string{},
		FollowersVisibility: "public",
		FollowingVisibility: "public",
		ChatScope:           u.ChatScope,
		URI:                 u.URI,
		PinnedNoteIDs:       []string{},
		PinnedNotes:         []any{},
		Roles:               packPublicRoles(u),
		// isSilenced は role policy 由来 (canPublicNote を否定する role を持つか)。
		// router で wire される lookup から解決する。unwired なら false。
		IsSilenced: resolveSilenced(u.ID),
		// DBデフォルト値 (user_profileのpublicReactions DEFAULT true)
		PublicReactions: true,
	}

	// IDからcreatedAtを抽出
	if len(idGens) > 0 && idGens[0] != nil {
		if t, err := idGens[0].ParseTime(u.ID); err == nil {
			d.CreatedAt = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}

	// lastFetchedAt は upstream と同じく ISO8601 (ms) で出す。未取得 (ローカル
	// ユーザー等) は null。
	if u.LastFetchedAt != nil {
		s := u.LastFetchedAt.UTC().Format("2006-01-02T15:04:05.000Z")
		d.LastFetchedAt = &s
	}

	// updatedAt は upstream UserEntityService.ts:536 と同じく user.updatedAt を
	// ISO8601 で出す。未更新は null (#1547)。旧実装は常に null だった。
	if u.UpdatedAt != nil {
		s := u.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000Z")
		d.UpdatedAt = &s
	}

	if profile != nil {
		d.Description = profile.Description
		d.Location = profile.Location
		d.Birthday = profile.Birthday
		d.Lang = profile.Lang
		// followedMessage は viewer-dependent (follower / self のみ)。packer は
		// set せず、self は AsMeDetailed、follower は users/show handler が埋める
		// (#1558 privacy gate)。base detailed には出さない。
		d.PublicReactions = profile.PublicReactions
		d.Fields = profile.Fields
		if len(profile.VerifiedLinks) > 0 {
			d.VerifiedLinks = []string(profile.VerifiedLinks)
		}
		d.FollowersVisibility = string(profile.FollowersVisibility)
		d.FollowingVisibility = string(profile.FollowingVisibility)
	}

	// upstream UserEntityService: publicReactions は
	// `isLocalUser(user) ? profile.publicReactions : false` (issue 12964)。
	// remote user は users/reactions が IS_REMOTE_USER を返すため、ここで false
	// にしておかないと frontend が reactions タブを表示して error になる。
	if u.Host != nil {
		d.PublicReactions = false
	}

	return d
}

// countVisible reports whether a followers/following count is visible to a
// viewer, per upstream UserEntityService (followingCount/followersCount are
// nulled → 0 unless): visibility=='public' OR self OR moderator OR
// (visibility=='followers' && the viewer follows the target) (#1558)。
func countVisible(visibility string, isMe, isModerator, isFollowing bool) bool {
	return visibility == string(model.FollowingVisibilityPublic) || isMe || isModerator ||
		(visibility == string(model.FollowingVisibilityFollowers) && isFollowing)
}

// GateCountVisibility zeroes followersCount / followingCount on a packed
// UserDetailed when the viewer must not see them, mirroring upstream's null →
// 0 for restricted (followers / private) visibility (#1558)。d.FollowersVisibility
// / d.FollowingVisibility は PackUserDetailed が profile から埋めた値を読む。
func GateCountVisibility(d *UserDetailed, isMe, isModerator, isFollowing bool) {
	if !countVisible(d.FollowersVisibility, isMe, isModerator, isFollowing) {
		d.FollowersCount = 0
	}
	if !countVisible(d.FollowingVisibility, isMe, isModerator, isFollowing) {
		d.FollowingCount = 0
	}
}

// backupCodesStock returns the 2FA backup-codes stock level. Mirrors upstream
// UserEntityService: "none" when 2FA is off or no codes remain, "full" at >=5,
// otherwise "partial". This is the single source of truth for the
// twoFactorBackupCodesStock field across all self-view paths.
func backupCodesStock(profile *model.UserProfile) string {
	if !profile.TwoFactorEnabled || len(profile.TwoFactorBackupSecret) == 0 {
		return "none"
	}
	if len(profile.TwoFactorBackupSecret) >= 5 {
		return "full"
	}
	return "partial"
}

// MoveTargetResolver resolves an ActivityPub actor URI to a local user ID.
// It must perform a local lookup only (no remote fetch); an unknown URI
// returns ("", false), mirroring upstream's resolve-or-null behavior.
type MoveTargetResolver func(uri string) (id string, ok bool)

// ResolveMoveTargets fills MovedTo / AlsoKnownAs from the account-migration
// URIs stored on u, resolving each to a local user ID via resolve. Upstream
// UserEntityService maps movedToUri / alsoKnownAs (URIs) to local user IDs and
// emits null for the unresolvable ones; this mirrors that. It is a no-op when
// u or resolve is nil, leaving the fields null.
//
// 注: mk-go は alsoKnownAs を comma-joined string で 1 カラムに格納している
// (renderer.go の AP 出力と対称)。ここで split して各 URI を解決する。
func (d *UserDetailed) ResolveMoveTargets(u *model.User, resolve MoveTargetResolver) {
	if u == nil || resolve == nil {
		return
	}
	if u.MovedToURI != nil && *u.MovedToURI != "" {
		if id, ok := resolve(*u.MovedToURI); ok {
			d.MovedTo = &id
		}
	}
	if u.AlsoKnownAs != nil && *u.AlsoKnownAs != "" {
		var ids []string
		for _, uri := range strings.Split(*u.AlsoKnownAs, ",") {
			uri = strings.TrimSpace(uri)
			if uri == "" {
				continue
			}
			if id, ok := resolve(uri); ok {
				ids = append(ids, id)
			}
		}
		// upstream は解決後 0 件なら null (空配列にしない)。
		if len(ids) > 0 {
			d.AlsoKnownAs = ids
		}
	}
}

// PackUserForFollowStreamEvent returns a UserDetailed envelope suitable for
// the main channel's "follow" and "unfollow" stream events. Upstream Misskey
// packs these events with the UserDetailedNotMe schema, and
// MkFollowButton.onFollowChange reads isFollowing and
// hasPendingFollowRequestFromYou directly to toggle the button.
//
// isFollowing をセットするため misskey_dart は UserDetailedNotMeWithRelations
// variant を選び、残りの relation bool + createdAt も非null必須になる (#1251)。
// idGen で createdAt を有効にし、EnsureRelationFlags で未設定 relation bool を
// false 補完して shape を完備する。idGen が nil の場合 createdAt は空になるため
// caller は必ず idGen を渡すこと。
func PackUserForFollowStreamEvent(u *model.User, isFollowing, hasPendingFollowRequestFromYou bool, idGen id.Generator) UserDetailed {
	d := PackUserDetailed(u, nil, idGen)
	d.IsFollowing = &isFollowing
	d.HasPendingFollowRequestFromYou = &hasPendingFollowRequestFromYou
	d.EnsureRelationFlags()
	return d
}
