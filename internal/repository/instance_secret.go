package repository

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MediaProxySecretKey is the instance_secret key holding the media proxy HMAC
// signing key.
const MediaProxySecretKey = "mediaProxySecret"

// generatedSecretBytes is the entropy of a generated secret. 32 bytes matches
// the HMAC-SHA256 block usage and is stored hex-encoded.
const generatedSecretBytes = 32

// InstanceSecretRepository stores per-instance generated secrets.
type InstanceSecretRepository interface {
	// GetOrCreate returns the stored secret for key, generating and persisting
	// a cryptographically random one on first use.
	GetOrCreate(key string) ([]byte, error)
}

type instanceSecretRepository struct {
	db *gorm.DB
}

// NewInstanceSecretRepository creates an InstanceSecretRepository.
func NewInstanceSecretRepository(db *gorm.DB) InstanceSecretRepository {
	return &instanceSecretRepository{db: db}
}

// instanceSecretRow maps the mk-go-only instance_secret table.
type instanceSecretRow struct {
	Key   string `gorm:"column:key;primaryKey"`
	Value string `gorm:"column:value"`
}

func (instanceSecretRow) TableName() string { return "instance_secret" }

// GetOrCreate returns the secret for key, creating it if absent.
//
// 生成と保存は ON CONFLICT DO NOTHING + 再読み込みで行う。複数プロセスが同時に
// 初回起動しても、勝った 1 行だけが残り全員が同じ値を読む。素直に
// 「SELECT して無ければ INSERT」と書くと、競合したプロセスが別々の鍵を持ったまま
// 動き続けてしまう。
func (r *instanceSecretRepository) GetOrCreate(key string) ([]byte, error) {
	if value, err := r.find(key); err == nil {
		return value, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	generated := make([]byte, generatedSecretBytes)
	if _, err := rand.Read(generated); err != nil {
		return nil, fmt.Errorf("generate instance secret: %w", err)
	}
	row := instanceSecretRow{Key: key, Value: hex.EncodeToString(generated)}
	if err := r.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("persist instance secret: %w", err)
	}

	// 競合して DoNothing になった場合は相手が入れた値が正なので読み直す。
	value, err := r.find(key)
	if err != nil {
		return nil, fmt.Errorf("read back instance secret: %w", err)
	}
	return value, nil
}

func (r *instanceSecretRepository) find(key string) ([]byte, error) {
	var row instanceSecretRow
	if err := r.db.Where("key = ?", key).First(&row).Error; err != nil {
		return nil, err
	}
	decoded, err := hex.DecodeString(row.Value)
	if err != nil {
		return nil, fmt.Errorf("decode instance secret %q: %w", key, err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("instance secret %q is empty", key)
	}
	return decoded, nil
}
