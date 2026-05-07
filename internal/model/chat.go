package model

import "github.com/lib/pq"

// ChatRoom represents the `chat_room` table.
type ChatRoom struct {
	ID          string `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	Name        string `gorm:"column:name;type:varchar(256);not null" json:"name"`
	OwnerID     string `gorm:"column:ownerId;type:varchar(32);not null" json:"ownerId"`
	Description string `gorm:"column:description;type:varchar(2048);default:''" json:"description"`
	IsArchived  bool   `gorm:"column:isArchived;default:false" json:"isArchived"`

	Owner *User `gorm:"foreignKey:OwnerID" json:"owner,omitempty"`
}

func (ChatRoom) TableName() string { return "chat_room" }

// ChatMessage represents the `chat_message` table.
type ChatMessage struct {
	ID              string         `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	FromUserID      string         `gorm:"column:fromUserId;type:varchar(32);not null" json:"fromUserId"`
	ToUserID        *string        `gorm:"column:toUserId;type:varchar(32)" json:"toUserId"`
	ToRoomID        *string        `gorm:"column:toRoomId;type:varchar(32)" json:"toRoomId"`
	Text            *string        `gorm:"column:text;type:varchar(4096)" json:"text"`
	URI             *string        `gorm:"column:uri;type:varchar(512)" json:"uri"`
	Reads           pq.StringArray `gorm:"column:reads;type:varchar(32)[];default:'{}'" json:"reads"`
	FileID          *string        `gorm:"column:fileId;type:varchar(32)" json:"fileId"`
	Reactions       pq.StringArray `gorm:"column:reactions;type:varchar(1024)[];default:'{}'" json:"reactions"`
	Emojis          pq.StringArray `gorm:"column:emojis;type:varchar(128)[];default:'{}'" json:"emojis"`
	IsDelivering    bool           `gorm:"column:isDelivering;default:false" json:"isDelivering"`
	IsDeliverFailed bool           `gorm:"column:isDeliverFailed;default:false" json:"isDeliverFailed"`

	FromUser *User      `gorm:"foreignKey:FromUserID" json:"fromUser,omitempty"`
	ToUser   *User      `gorm:"foreignKey:ToUserID" json:"toUser,omitempty"`
	ToRoom   *ChatRoom  `gorm:"foreignKey:ToRoomID" json:"toRoom,omitempty"`
	File     *DriveFile `gorm:"foreignKey:FileID" json:"file,omitempty"`
}

func (ChatMessage) TableName() string { return "chat_message" }

// ChatRoomMembership represents the `chat_room_membership` table.
type ChatRoomMembership struct {
	ID      string `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UserID  string `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	RoomID  string `gorm:"column:roomId;type:varchar(32);not null" json:"roomId"`
	IsMuted bool   `gorm:"column:isMuted;default:false" json:"isMuted"`

	User *User     `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Room *ChatRoom `gorm:"foreignKey:RoomID" json:"room,omitempty"`
}

func (ChatRoomMembership) TableName() string { return "chat_room_membership" }

// ChatRoomInvitation represents the `chat_room_invitation` table.
type ChatRoomInvitation struct {
	ID      string `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UserID  string `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	RoomID  string `gorm:"column:roomId;type:varchar(32);not null" json:"roomId"`
	Ignored bool   `gorm:"column:ignored;default:false" json:"ignored"`

	User *User     `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Room *ChatRoom `gorm:"foreignKey:RoomID" json:"room,omitempty"`
}

func (ChatRoomInvitation) TableName() string { return "chat_room_invitation" }

// ChatApproval represents the `chat_approval` table. It manages per-user
// overrides for 1-on-1 chat permissions (works with User.ChatScope).
type ChatApproval struct {
	ID      string `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UserID  string `gorm:"column:userId;type:varchar(32);not null" json:"userId"`
	OtherID string `gorm:"column:otherId;type:varchar(32);not null" json:"otherId"`

	User  *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Other *User `gorm:"foreignKey:OtherID" json:"other,omitempty"`
}

func (ChatApproval) TableName() string { return "chat_approval" }

// UserPending represents the `user_pending` table. It holds users awaiting
// email confirmation during invite-based registration.
type UserPending struct {
	ID       string `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	Code     string `gorm:"column:code;type:varchar(128);not null;uniqueIndex" json:"code"`
	Username string `gorm:"column:username;type:varchar(128);not null" json:"username"`
	Email    string `gorm:"column:email;type:varchar(128);not null" json:"email"`
	Password string `gorm:"column:password;type:varchar(128);not null" json:"-"`
	// InvitationTicketID は招待制 + email 確認制の併用時に消費した
	// registration_ticket の ID。PromotePending 完了時に MarkUsed する。
	// 通常 (招待制無効) signup では nil。
	InvitationTicketID *string `gorm:"column:invitationTicketId;type:varchar(32)" json:"invitationTicketId,omitempty"`
}

func (UserPending) TableName() string { return "user_pending" }
