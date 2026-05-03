package model

import (
	"github.com/lib/pq"
	"gorm.io/datatypes"
)

// NoteVisibility represents the visibility level of a note.
type NoteVisibility string

const (
	NoteVisibilityPublic    NoteVisibility = "public"
	NoteVisibilityHome      NoteVisibility = "home"
	NoteVisibilityFollowers NoteVisibility = "followers"
	NoteVisibilitySpecified NoteVisibility = "specified"
)

// Note represents the `note` table.
type Note struct {
	ID                       string         `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	ReplyID                  *string        `gorm:"column:replyId;type:varchar(32)" json:"replyId"`
	RenoteID                 *string        `gorm:"column:renoteId;type:varchar(32)" json:"renoteId"`
	ThreadID                 *string        `gorm:"column:threadId;type:varchar(256)" json:"threadId"`
	Text                     *string        `gorm:"column:text;type:text" json:"text"`
	Name                     *string        `gorm:"column:name;type:varchar(256)" json:"name"`
	CW                       *string        `gorm:"column:cw;type:varchar(512)" json:"cw"`
	UserID                   string         `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	LocalOnly                bool           `gorm:"column:localOnly;default:false" json:"localOnly"`
	ReactionAcceptance       *string        `gorm:"column:reactionAcceptance;type:varchar(64)" json:"reactionAcceptance"`
	RenoteCount              int16          `gorm:"column:renoteCount;type:smallint;default:0" json:"renoteCount"`
	RepliesCount             int16          `gorm:"column:repliesCount;type:smallint;default:0" json:"repliesCount"`
	ClippedCount             int16          `gorm:"column:clippedCount;type:smallint;default:0" json:"clippedCount"`
	PageCount                int16          `gorm:"column:pageCount;type:smallint;default:0" json:"pageCount"`
	Reactions                datatypes.JSON `gorm:"column:reactions;type:jsonb;default:'{}'" json:"reactions"`
	Visibility               NoteVisibility `gorm:"column:visibility;type:note_visibility_enum;not null" json:"visibility"`
	URI                      *string        `gorm:"column:uri;type:varchar(512)" json:"uri"`
	URL                      *string        `gorm:"column:url;type:varchar(512)" json:"url"`
	FileIDs                  pq.StringArray `gorm:"column:fileIds;type:varchar(32)[];default:'{}'" json:"fileIds"`
	AttachedFileTypes        pq.StringArray `gorm:"column:attachedFileTypes;type:varchar(256)[];default:'{}'" json:"attachedFileTypes"`
	VisibleUserIDs           pq.StringArray `gorm:"column:visibleUserIds;type:varchar(32)[];default:'{}'" json:"visibleUserIds"`
	Mentions                 pq.StringArray `gorm:"column:mentions;type:varchar(32)[];default:'{}'" json:"mentions"`
	MentionedRemoteUsers     string         `gorm:"column:mentionedRemoteUsers;type:text;default:'[]'" json:"mentionedRemoteUsers"`
	ReactionAndUserPairCache pq.StringArray `gorm:"column:reactionAndUserPairCache;type:varchar(1024)[];default:'{}'" json:"reactionAndUserPairCache"`
	Emojis                   pq.StringArray `gorm:"column:emojis;type:varchar(128)[];default:'{}'" json:"emojis"`
	Tags                     pq.StringArray `gorm:"column:tags;type:varchar(128)[];default:'{}'" json:"tags"`
	HasPoll                  bool           `gorm:"column:hasPoll;default:false" json:"hasPoll"`
	ChannelID                *string        `gorm:"column:channelId;type:varchar(32)" json:"channelId"`
	// Denormalized fields
	UserHost        *string `gorm:"column:userHost;type:varchar(128)" json:"userHost"`
	ReplyUserID     *string `gorm:"column:replyUserId;type:varchar(32)" json:"replyUserId"`
	ReplyUserHost   *string `gorm:"column:replyUserHost;type:varchar(128)" json:"replyUserHost"`
	RenoteUserID    *string `gorm:"column:renoteUserId;type:varchar(32)" json:"renoteUserId"`
	RenoteUserHost  *string `gorm:"column:renoteUserHost;type:varchar(128)" json:"renoteUserHost"`
	RenoteChannelID *string `gorm:"column:renoteChannelId;type:varchar(32)" json:"renoteChannelId"`

	// Relations
	Reply  *Note `gorm:"foreignKey:ReplyID" json:"reply,omitempty"`
	Renote *Note `gorm:"foreignKey:RenoteID" json:"renote,omitempty"`
	User   *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
	// Poll は note.id ↔ poll.noteId の 1:1 リレーション。Preload しない経路
	// (FindByID 単発など) では nil のまま。entity.PackNote が non-nil の
	// ときだけ poll フィールドを response に詰める (#690)。
	Poll *Poll `gorm:"foreignKey:NoteID;references:ID" json:"poll,omitempty"`
}

func (Note) TableName() string { return "note" }

// NoteSearchFilter narrows down the result of NoteRepository.SearchByFilter.
// repository / core/search / testutil の三方から参照されるため model パッケージ
// に置く (循環依存の回避)。
//
// 各文字列フィールドは zero value (空文字) でフィルタ無効を意味する。
// Host == "." はローカル限定 (userHost IS NULL) を表す。
type NoteSearchFilter struct {
	Query     string
	UserID    string
	ChannelID string
	Host      string
	UntilID   string
	SinceID   string
	Limit     int
	// Pgroonga, when true, switches the WHERE clause from ILIKE to the
	// PGroonga `&@~` full-text match operator. Mirrors upstream Misskey TS
	// SearchService.searchNoteByLike, which swaps the predicate when
	// fulltextSearch.provider == "sqlPgroonga".
	Pgroonga bool
}

// ReplyTargetCount is a (userID, count) pair returned by
// NoteRepository.CountReplyTargets. repository と testutil の両方から参照される
// ため model パッケージに置く (循環依存の回避)。
type ReplyTargetCount struct {
	UserID string `gorm:"column:replyUserId"`
	Count  int64  `gorm:"column:count"`
}
