package entity

import (
	"encoding/json"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"gorm.io/datatypes"
)

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
type UserDetailed struct {
	UserLite
	AvatarID            *string        `json:"avatarId"`
	BannerID            *string        `json:"bannerId"`
	BannerURL           *string        `json:"bannerUrl"`
	BannerBlurhash      *string        `json:"bannerBlurhash"`
	IsLocked            bool           `json:"isLocked"`
	IsSilenced          bool           `json:"isSilenced"`
	IsSuspended         bool           `json:"isSuspended"`
	Description         *string        `json:"description"`
	Location            *string        `json:"location"`
	Birthday            *string        `json:"birthday"`
	Lang                *string        `json:"lang"`
	FollowedMessage     *string        `json:"followedMessage"`
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
	ChatScope     string   `json:"chatScope"`
	CreatedAt     string   `json:"createdAt"`
	UpdatedAt     *string  `json:"updatedAt"`
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
	// viewer依存フィールド (ハンドラ側でセット)
	IsFollowing                    *bool   `json:"isFollowing"`
	IsFollowed                     *bool   `json:"isFollowed"`
	IsBlocking                     *bool   `json:"isBlocking,omitempty"`
	IsBlocked                      *bool   `json:"isBlocked,omitempty"`
	IsMuted                        *bool   `json:"isMuted,omitempty"`
	IsRenoteMuted                  *bool   `json:"isRenoteMuted,omitempty"`
	HasPendingFollowRequestFromYou *bool   `json:"hasPendingFollowRequestFromYou,omitempty"`
	HasPendingFollowRequestToYou   *bool   `json:"hasPendingFollowRequestToYou,omitempty"`
	Notify                         *string `json:"notify,omitempty"`
	WithReplies                    *bool   `json:"withReplies,omitempty"`
	Memo                           *string `json:"memo,omitempty"`
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
	IsExplorable             bool `json:"isExplorable"`
	IsDeleted                bool `json:"isDeleted"`
	HideOnlineStatus         bool `json:"hideOnlineStatus"`
	NoCrawle                 bool `json:"noCrawle"`
	PreventAiLearning        bool `json:"preventAiLearning"`
	AutoSensitive            bool `json:"autoSensitive"`
	CarefulBot               bool `json:"carefulBot"`
	AutoAcceptFollowed       bool `json:"autoAcceptFollowed"`
	AlwaysMarkNsfw           bool `json:"alwaysMarkNsfw"`
	ReceiveAnnouncementEmail bool `json:"receiveAnnouncementEmail"`
	InjectFeaturedNote       bool `json:"injectFeaturedNote"`
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
		UserDetailed:     d,
		IsExplorable:     u.IsExplorable,
		IsDeleted:        u.IsDeleted,
		HideOnlineStatus: u.HideOnlineStatus,
		// default は upstream UserEntityService と同じ値。profile が
		// non-nil なら下で JSON column から上書きする。
		EmailNotificationTypes:    []string{"follow", "receiveFollowRequest"},
		MutingNotificationTypes:   []string{},
		NotificationRecieveConfig: map[string]any{},
	}
	if profile != nil {
		out.NoCrawle = profile.NoCrawle
		out.PreventAiLearning = profile.PreventAiLearning
		out.AutoSensitive = profile.AutoSensitive
		out.CarefulBot = profile.CarefulBot
		out.AutoAcceptFollowed = profile.AutoAcceptFollowed
		out.AlwaysMarkNsfw = profile.AlwaysMarkNsfw
		out.ReceiveAnnouncementEmail = profile.ReceiveAnnouncementEmail
		out.InjectFeaturedNote = profile.InjectFeaturedNote
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
	}
	return out
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
		return *u.AvatarURL
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
		OnlineStatus:      "unknown",
		BadgeRoles:        []any{},
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
		UserLite:            PackUserLite(u),
		AvatarID:            u.AvatarID,
		BannerID:            u.BannerID,
		BannerURL:           u.BannerURL,
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
		Roles:               []any{},
		// DBデフォルト値 (user_profileのpublicReactions DEFAULT true)
		PublicReactions: true,
	}

	// IDからcreatedAtを抽出
	if len(idGens) > 0 && idGens[0] != nil {
		if t, err := idGens[0].ParseTime(u.ID); err == nil {
			d.CreatedAt = t.UTC().Format("2006-01-02T15:04:05.000Z")
		}
	}

	if profile != nil {
		d.Description = profile.Description
		d.Location = profile.Location
		d.Birthday = profile.Birthday
		d.Lang = profile.Lang
		d.FollowedMessage = profile.FollowedMessage
		d.PublicReactions = profile.PublicReactions
		d.Fields = profile.Fields
		if len(profile.VerifiedLinks) > 0 {
			d.VerifiedLinks = []string(profile.VerifiedLinks)
		}
		d.FollowersVisibility = string(profile.FollowersVisibility)
		d.FollowingVisibility = string(profile.FollowingVisibility)
	}

	return d
}

// PackUserForFollowStreamEvent returns a UserDetailed envelope suitable for
// the main channel's "follow" and "unfollow" stream events. Upstream Misskey
// packs these events with the UserDetailedNotMe schema, and
// MkFollowButton.onFollowChange reads isFollowing and
// hasPendingFollowRequestFromYou directly to toggle the button — so both
// fields must be populated from the viewer's perspective. Other viewer-
// dependent fields (isBlocking, isMuted, etc.) are left at their zero values
// because the follow button does not consume them.
func PackUserForFollowStreamEvent(u *model.User, isFollowing, hasPendingFollowRequestFromYou bool) UserDetailed {
	d := PackUserDetailed(u, nil)
	d.IsFollowing = &isFollowing
	d.HasPendingFollowRequestFromYou = &hasPendingFollowRequestFromYou
	return d
}
