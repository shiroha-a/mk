package notification

// Kind classifies where a notification type comes from.
//
// 型フィルタ (includeTypes / excludeTypes) での扱いが Kind で変わるので、
// 「upstream にあるか」を型ごとに宣言する。
type Kind int

const (
	// KindUpstream is a type present in upstream Misskey's `notificationTypes`
	// (packages/backend/src/types.ts).
	KindUpstream Kind = iota
	// KindObsolete is a type present in upstream's `obsoleteNotificationTypes`.
	// It stays valid as a filter value but is not counted by the
	// "excludeTypes covers everything" check, matching upstream.
	KindObsolete
	// KindMkGo is a type that only mk-go produces.
	KindMkGo
)

// Descriptor declares one notification type.
type Descriptor struct {
	Type Type
	Kind Kind
	// Produced reports whether anything in mk-go currently creates this type.
	// false = the constant exists for compatibility with persisted rows or
	// with upstream's enum, but no code path emits it.
	Produced bool
}

// registry is the single source of truth for notification types.
//
// **API 層はここから導出する。** 以前は `internal/api/notifications` が
// upstream の一覧を独立したリテラルで持っており、core の定数との間で
// 片側更新が起きていた (`importCompleted` が core にだけあった)。同じ一覧を
// 2 箇所に置くと、固有型を足すたびにこの穴を踏む。
//
// 並び順は upstream `notificationTypes` の順を保つ。全指定判定
// (emptyByTypeFilter) が見る集合の順序に意味は無いが、upstream の types.ts と
// 目視で突き合わせられる形にしておく。
var registry = []Descriptor{
	{Type: TypeNote, Kind: KindUpstream, Produced: true},
	{Type: TypeFollow, Kind: KindUpstream, Produced: true},
	{Type: TypeMention, Kind: KindUpstream, Produced: true},
	{Type: TypeReply, Kind: KindUpstream, Produced: true},
	{Type: TypeRenote, Kind: KindUpstream, Produced: true},
	{Type: TypeQuote, Kind: KindUpstream, Produced: true},
	{Type: TypeReaction, Kind: KindUpstream, Produced: true},
	{Type: TypePollEnded, Kind: KindUpstream, Produced: true},
	{Type: TypeScheduledNotePosted, Kind: KindUpstream, Produced: true},
	{Type: TypeScheduledNotePostFailed, Kind: KindUpstream, Produced: true},
	{Type: TypeReceiveFollowReq, Kind: KindUpstream, Produced: true},
	{Type: TypeFollowRequestAccept, Kind: KindUpstream, Produced: true},
	{Type: TypeRoleAssigned, Kind: KindUpstream, Produced: true},
	{Type: TypeChatRoomInvitationReceived, Kind: KindUpstream, Produced: true},
	{Type: TypeAchievementEarned, Kind: KindUpstream, Produced: true},
	{Type: TypeExportCompleted, Kind: KindUpstream, Produced: true},
	{Type: TypeLogin, Kind: KindUpstream, Produced: true},
	{Type: TypeCreateToken, Kind: KindUpstream, Produced: true},
	{Type: TypeApp, Kind: KindUpstream, Produced: true},
	{Type: TypeTest, Kind: KindUpstream, Produced: true},

	// obsolete。upstream も filter 値としては受け付けるが、全指定判定には
	// 数えない。pollVote は永続化済みの行があるので型としては残る (#690)。
	{Type: TypePollVote, Kind: KindObsolete, Produced: false},
	{Type: TypeGroupInvited, Kind: KindObsolete, Produced: false},

	// mk-go 固有。
	//
	// importCompleted は exportCompleted と対で定数だけが存在し、**現在どこ
	// からも発火しない**。upstream にも無い。enum に入れておかないと
	// 「core にあるのに filter に指定できない型」が残るので登録する。
	{Type: TypeImportCompleted, Kind: KindMkGo, Produced: false},
	// 通報をモデレーターの通知欄に残す (#2868)。upstream は email /
	// system webhook / admin stream しか持たない。
	{Type: TypeAbuseReport, Kind: KindMkGo, Produced: true},
	// 絵文字の登録申請の結果を申請者へ返す (#2934)。upstream には申請の
	// 概念自体が無い。
	{Type: TypeEmojiApplicationProcessed, Kind: KindMkGo, Produced: true},
}

// UpstreamTypeNames returns the types counted by the "excludeTypes covers
// everything" check, in registry order.
//
// **mk-go 固有の型を入れてはいけない (#2898)。** 一度入れて回帰させた:
// upstream 由来のクライアント (misskey-js の notificationTypes を送るもの、
// fork frontend の「すべて無効」を含む) は upstream の 20 種しか送らないので、
// 固有型が集合にあると被覆判定が成立しなくなる。すると呼び出し側が早期 return
// を抜けて既読化まで進み、**1 件も返していないのにユーザーが受け取っていない
// 通知まで既読位置が飛ぶ** (#2833 / #2835 が塞いだ害の再オープン)。
//
// 固有型は enum には入るので、includeTypes / excludeTypes の値としては指定
// できる。「upstream の全種を除外したら固有型も一緒に消える」のは、通知を
// 全部切ったユーザーの意図にも沿う。
func UpstreamTypeNames() []string {
	out := make([]string, 0, len(registry))
	for _, d := range registry {
		if d.Kind == KindUpstream {
			out = append(out, string(d.Type))
		}
	}
	return out
}

// MkGoTypeNames returns the mk-go specific types in registry order.
//
// enum (filter 値の検証) にだけ入る。全指定判定に入れない理由は
// UpstreamTypeNames を参照。
func MkGoTypeNames() []string {
	out := make([]string, 0, 2)
	for _, d := range registry {
		if d.Kind == KindMkGo {
			out = append(out, string(d.Type))
		}
	}
	return out
}

// ObsoleteTypeNames returns the obsolete types in registry order.
func ObsoleteTypeNames() []string {
	out := make([]string, 0, 2)
	for _, d := range registry {
		if d.Kind == KindObsolete {
			out = append(out, string(d.Type))
		}
	}
	return out
}

// Descriptors returns a copy of the registry.
func Descriptors() []Descriptor {
	out := make([]Descriptor, len(registry))
	copy(out, registry)
	return out
}
