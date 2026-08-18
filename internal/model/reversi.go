package model

import (
	"time"

	"gorm.io/datatypes"
)

// ReversiGame represents the `reversi_game` table.
type ReversiGame struct {
	ID                   string         `gorm:"column:id;type:varchar(32);primaryKey" json:"id"`
	StartedAt            *time.Time     `gorm:"column:startedAt;type:timestamp with time zone" json:"startedAt"`
	EndedAt              *time.Time     `gorm:"column:endedAt;type:timestamp with time zone" json:"endedAt"`
	User1ID              string         `gorm:"column:user1Id;type:varchar(32);not null" json:"user1Id"`
	User2ID              string         `gorm:"column:user2Id;type:varchar(32);not null" json:"user2Id"`
	User1Ready           bool           `gorm:"column:user1Ready;default:false" json:"user1Ready"`
	User2Ready           bool           `gorm:"column:user2Ready;default:false" json:"user2Ready"`
	Black                *int           `gorm:"column:black;type:integer" json:"black"`
	IsStarted            bool           `gorm:"column:isStarted;default:false" json:"isStarted"`
	IsEnded              bool           `gorm:"column:isEnded;default:false" json:"isEnded"`
	WinnerID             *string        `gorm:"column:winnerId;type:varchar(32)" json:"winnerId"`
	SurrenderedUserID    *string        `gorm:"column:surrenderedUserId;type:varchar(32)" json:"surrenderedUserId"`
	TimeoutUserID        *string        `gorm:"column:timeoutUserId;type:varchar(32)" json:"timeoutUserId"`
	TimeLimitForEachTurn int            `gorm:"column:timeLimitForEachTurn;type:smallint;default:90" json:"timeLimitForEachTurn"`
	Logs                 datatypes.JSON `gorm:"column:logs;type:jsonb;default:'[]'" json:"logs"`
	Map                  StringArray    `gorm:"column:map;type:varchar(64)[]" json:"map"`
	BW                   string         `gorm:"column:bw;type:varchar(32)" json:"bw"`
	NoIrregularRules     bool           `gorm:"column:noIrregularRules;default:false" json:"noIrregularRules"`
	IsLlotheo            bool           `gorm:"column:isLlotheo;default:false" json:"isLlotheo"`
	CanPutEverywhere     bool           `gorm:"column:canPutEverywhere;default:false" json:"canPutEverywhere"`
	LoopedBoard          bool           `gorm:"column:loopedBoard;default:false" json:"loopedBoard"`
	Form1                datatypes.JSON `gorm:"column:form1;type:jsonb" json:"form1"`
	Form2                datatypes.JSON `gorm:"column:form2;type:jsonb" json:"form2"`
	CRC32                *string        `gorm:"column:crc32;type:varchar(32)" json:"crc32"`

	User1 *User `gorm:"foreignKey:User1ID" json:"user1,omitempty"`
	User2 *User `gorm:"foreignKey:User2ID" json:"user2,omitempty"`
}

func (ReversiGame) TableName() string { return "reversi_game" }
