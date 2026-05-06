package model

import (
	"time"

	"github.com/lib/pq"
	"gorm.io/datatypes"
)

// User represents the `user` table.
// Misskey互換のカラム名・型を維持する
type User struct {
	ID               string     `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	UpdatedAt        *time.Time `gorm:"column:updatedAt;type:timestamp with time zone" json:"updatedAt"`
	LastFetchedAt    *time.Time `gorm:"column:lastFetchedAt;type:timestamp with time zone" json:"lastFetchedAt"`
	LastActiveDate   *time.Time `gorm:"column:lastActiveDate;type:timestamp with time zone" json:"lastActiveDate"`
	HideOnlineStatus bool       `gorm:"column:hideOnlineStatus;default:false" json:"hideOnlineStatus"`
	Username         string     `gorm:"column:username;type:varchar(128);not null" json:"username"`
	UsernameLower    string     `gorm:"column:usernameLower;type:varchar(128);not null" json:"usernameLower"`
	// IsRoot: Misskey TS upstream の "instance root" マーカー。drop-in 互換
	// (TS で signup した最初のユーザーは isRoot=true 行を持つが mk-go の
	// rootUserId フィールドは set されていない) のため、本フィールドを読み
	// 取って role.Service.IsAdministrator が true を返すよう fallback する
	// (#785)。mk-go signup でも同時に true を書く (forward compat)。
	IsRoot                       bool           `gorm:"column:isRoot;default:false" json:"isRoot"`
	Name                         *string        `gorm:"column:name;type:varchar(128)" json:"name"`
	FollowersCount               int            `gorm:"column:followersCount;default:0" json:"followersCount"`
	FollowingCount               int            `gorm:"column:followingCount;default:0" json:"followingCount"`
	MovedToURI                   *string        `gorm:"column:movedToUri;type:varchar(512)" json:"movedToUri"`
	MovedAt                      *time.Time     `gorm:"column:movedAt;type:timestamp with time zone" json:"movedAt"`
	AlsoKnownAs                  *string        `gorm:"column:alsoKnownAs;type:text" json:"alsoKnownAs"`
	NotesCount                   int            `gorm:"column:notesCount;default:0" json:"notesCount"`
	AvatarID                     *string        `gorm:"column:avatarId;type:varchar(32)" json:"avatarId"`
	BannerID                     *string        `gorm:"column:bannerId;type:varchar(32)" json:"bannerId"`
	AvatarURL                    *string        `gorm:"column:avatarUrl;type:varchar(1024)" json:"avatarUrl"`
	BannerURL                    *string        `gorm:"column:bannerUrl;type:varchar(512)" json:"bannerUrl"`
	AvatarBlurhash               *string        `gorm:"column:avatarBlurhash;type:varchar(128)" json:"avatarBlurhash"`
	BannerBlurhash               *string        `gorm:"column:bannerBlurhash;type:varchar(128)" json:"bannerBlurhash"`
	AvatarDecorations            datatypes.JSON `gorm:"column:avatarDecorations;type:jsonb;default:'[]'" json:"avatarDecorations"`
	Tags                         pq.StringArray `gorm:"column:tags;type:varchar(128)[];default:'{}'" json:"tags"`
	Score                        int            `gorm:"column:score;default:0" json:"score"`
	IsSuspended                  bool           `gorm:"column:isSuspended;default:false" json:"isSuspended"`
	IsLocked                     bool           `gorm:"column:isLocked;default:false" json:"isLocked"`
	IsBot                        bool           `gorm:"column:isBot;default:false" json:"isBot"`
	IsCat                        bool           `gorm:"column:isCat;default:false" json:"isCat"`
	IsExplorable                 bool           `gorm:"column:isExplorable;default:true" json:"isExplorable"`
	IsHibernated                 bool           `gorm:"column:isHibernated;default:false" json:"isHibernated"`
	RequireSigninToViewContents  bool           `gorm:"column:requireSigninToViewContents;default:false" json:"requireSigninToViewContents"`
	MakeNotesFollowersOnlyBefore *int           `gorm:"column:makeNotesFollowersOnlyBefore" json:"makeNotesFollowersOnlyBefore"`
	MakeNotesHiddenBefore        *int           `gorm:"column:makeNotesHiddenBefore" json:"makeNotesHiddenBefore"`
	IsDeleted                    bool           `gorm:"column:isDeleted;default:false" json:"isDeleted"`
	Emojis                       pq.StringArray `gorm:"column:emojis;type:varchar(128)[];default:'{}'" json:"emojis"`
	ChatScope                    string         `gorm:"column:chatScope;type:varchar(128);default:'mutual'" json:"chatScope"`
	Host                         *string        `gorm:"column:host;type:varchar(128)" json:"host"`
	Inbox                        *string        `gorm:"column:inbox;type:varchar(512)" json:"inbox"`
	SharedInbox                  *string        `gorm:"column:sharedInbox;type:varchar(512)" json:"sharedInbox"`
	Featured                     *string        `gorm:"column:featured;type:varchar(512)" json:"featured"`
	URI                          *string        `gorm:"column:uri;type:varchar(512)" json:"uri"`
	FollowersURI                 *string        `gorm:"column:followersUri;type:varchar(512)" json:"followersUri"`
	Token                        *string        `gorm:"column:token;type:char(16)" json:"-"`

	// Relations
	Avatar *DriveFile `gorm:"foreignKey:AvatarID" json:"avatar,omitempty"`
	Banner *DriveFile `gorm:"foreignKey:BannerID" json:"banner,omitempty"`
}

func (User) TableName() string { return "user" }

// IsLocal returns true if the user is a local user.
func (u *User) IsLocal() bool {
	return u.Host == nil || *u.Host == ""
}

// UserListFilter holds filter/sort/pagination for admin/show-users.
type UserListFilter struct {
	State    string // "all", "admin", "moderator", "suspended", "alive"
	Origin   string // "local", "remote", "combined"
	Hostname string // if non-empty, restricts to a specific remote host
	Sort     string // "+createdAt", "-createdAt", "+updatedAt", "-updatedAt", etc.
	Limit    int
	Offset   int
}
