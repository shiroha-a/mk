// Package ephemeral keeps relay-delivered notes and their authors in Redis
// instead of the database.
//
// リレー経由でしか観測しない投稿は、ローカルの誰とも関係が無いまま流れていく
// だけなので DB に入れる価値が無い。それでも保存すると、リレーの流量に比例して
// INSERT と DELETE が両方発生し、WAL / インデックス更新 / デッドタプル /
// autovacuum の負荷が二重に掛かる。これがリレー参加の障壁になっている (#2332)。
//
// そこで TTL 付きで Redis に置き、誰も触らなければ期限切れで消えるようにする。
// ローカルユーザーの操作や連合からの参照で **DB 行への外部キー参照が必要に
// なったとき** にだけ materialize する。
//
// 保存するのは packed JSON ではなく `model.Note` / `model.User` そのもの。
// 下流 (ApplyFilter / PackNotes / PublishNote) はいずれも `*model.Note` を
// 受け取るので、読み戻して渡せばフィルタ・パック・ストリーミングが無改造で通る。
package ephemeral

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/shiroha-a/mk/internal/model"
)

// Key prefixes. keyPrefix (`<host>:`) が更に前に付く。
const (
	notePrefix    = "ephNote:"
	userPrefix    = "ephUser:"
	noteURIPrefix = "ephNoteURI:"
	userURIPrefix = "ephUserURI:"
)

// userTTLFactor extends the author TTL relative to the note TTL.
//
// 著者は複数のノートから共有される。ノートと同じ TTL にすると、連投の途中で
// 著者だけ先に消えて後続ノートが著者不明になる。ノートより長く持たせる。
const userTTLFactor = 4

// Settings carries the operator-controlled knobs for the ephemeral store.
type Settings struct {
	// Enabled gates *ingestion*. 無効でも読み出しは続ける: 既に置かれている
	// ぶんは TTL 切れまで表示できた方が、切り替えた瞬間に timeline から
	// 消えるより自然なため。
	Enabled bool
	// TTL は Redis 上の保持時間。0 以下なら defaultTTL。
	TTL time.Duration
}

// Store persists ephemeral notes and authors in Redis.
type Store struct {
	client    *redis.Client
	keyPrefix string
	settings  func() Settings
}

// NewStore constructs a Store bound to the given Redis database.
//
// settings は meta 由来の設定を都度読むための関数。起動時に固定すると管理画面
// での変更が再起動まで効かない。keyPrefix は FanoutTimelineService と同じ
// `<host>:`。
func NewStore(client *redis.Client, keyPrefix string, settings func() Settings) *Store {
	return &Store{client: client, keyPrefix: keyPrefix, settings: settings}
}

// Enabled reports whether relay-delivered notes should be diverted to Redis
// instead of the database.
//
// これが false のときは取り込み経路が通常の DB 保存に倒れる。既定無効の約束を
// ここで担保する (sink が配線されているだけで機能が入ってしまわないように)。
func (s *Store) Enabled() bool {
	if s == nil || s.client == nil || s.settings == nil {
		return false
	}
	return s.settings().Enabled
}

// defaultTTL is used when the configured TTL is unset or non-positive.
const defaultTTL = time.Hour

func (s *Store) noteTTL() time.Duration {
	if s == nil || s.settings == nil {
		return defaultTTL
	}
	if d := s.settings().TTL; d > 0 {
		return d
	}
	return defaultTTL
}

func (s *Store) key(prefix, id string) string { return s.keyPrefix + prefix + id }

// PutNote stores a note together with its author.
//
// note.User は別キーに分けて保存する (同じ著者の連投で本文が重複しないように)。
// 呼び出し側が組み立てた構造体は変更しない。
func (s *Store) PutNote(ctx context.Context, n *model.Note, author *model.User) error {
	if s == nil || s.client == nil || n == nil || author == nil {
		return nil
	}
	if n.ID == "" || n.URI == nil || *n.URI == "" {
		return fmt.Errorf("ephemeral: note requires id and uri")
	}
	ttl := s.noteTTL()

	// リレーションを外した複製を保存する。User は ephUser 側が持つので、
	// ここに埋めると同じ著者ぶん JSON が重複する。
	stored := *n
	stored.User = nil
	stored.Reply = nil
	stored.Renote = nil
	stored.Poll = nil

	noteJSON, err := json.Marshal(&stored)
	if err != nil {
		return fmt.Errorf("ephemeral: marshal note: %w", err)
	}
	userJSON, err := json.Marshal(author)
	if err != nil {
		return fmt.Errorf("ephemeral: marshal user: %w", err)
	}

	pipe := s.client.Pipeline()
	pipe.Set(ctx, s.key(notePrefix, n.ID), noteJSON, ttl)
	pipe.Set(ctx, s.key(noteURIPrefix, *n.URI), n.ID, ttl)
	pipe.Set(ctx, s.key(userPrefix, author.ID), userJSON, ttl*userTTLFactor)
	if author.URI != nil && *author.URI != "" {
		pipe.Set(ctx, s.key(userURIPrefix, *author.URI), author.ID, ttl*userTTLFactor)
	}
	_, err = pipe.Exec(ctx)
	return err
}

// GetNote returns the ephemeral note for id with its author attached, or nil
// when the entry is absent or expired.
func (s *Store) GetNote(ctx context.Context, id string) (*model.Note, error) {
	notes, err := s.GetNotes(ctx, []string{id})
	if err != nil || len(notes) == 0 {
		return nil, err
	}
	return notes[0], nil
}

// GetNotes returns the ephemeral notes for ids, skipping any that are absent.
// 戻り値の順序は ids の順序を保つ (欠損は詰める)。
func (s *Store) GetNotes(ctx context.Context, ids []string) ([]*model.Note, error) {
	if s == nil || s.client == nil || len(ids) == 0 {
		return nil, nil
	}
	pipe := s.client.Pipeline()
	cmds := make([]*redis.StringCmd, 0, len(ids))
	for _, id := range ids {
		cmds = append(cmds, pipe.Get(ctx, s.key(notePrefix, id)))
	}
	// redis.Nil は「そのキーが無い」だけなので握り潰す。それ以外の error は
	// 呼び出し側 (timeline hydrate) が DB fallback を選べるように返す。
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}

	out := make([]*model.Note, 0, len(ids))
	authors := map[string]*model.User{}
	for _, cmd := range cmds {
		raw, err := cmd.Bytes()
		if err != nil {
			continue
		}
		var n model.Note
		if err := json.Unmarshal(raw, &n); err != nil {
			continue
		}
		authors[n.UserID] = nil
		out = append(out, &n)
	}
	if len(out) == 0 {
		return nil, nil
	}

	// 著者をまとめて引いて詰め直す。PackNotes / ApplyFilter は note.User を見る。
	if err := s.attachAuthors(ctx, out, authors); err != nil {
		return nil, err
	}
	return out, nil
}

// attachAuthors fills note.User for every note in notes.
//
// 著者を引けなかったノートは落とさずそのまま返す。PackNotes は User == nil でも
// 落ちない設計なので、著者の TTL 切れで timeline 全体が欠けるより良い。
func (s *Store) attachAuthors(ctx context.Context, notes []*model.Note, want map[string]*model.User) error {
	ids := make([]string, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	pipe := s.client.Pipeline()
	cmds := make(map[string]*redis.StringCmd, len(ids))
	for _, id := range ids {
		cmds[id] = pipe.Get(ctx, s.key(userPrefix, id))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return err
	}
	for id, cmd := range cmds {
		raw, err := cmd.Bytes()
		if err != nil {
			continue
		}
		var u model.User
		if err := json.Unmarshal(raw, &u); err != nil {
			continue
		}
		want[id] = &u
	}
	for _, n := range notes {
		n.User = want[n.UserID]
	}
	return nil
}

// NoteIDByURI returns the ephemeral note ID assigned to uri, or "" when absent.
func (s *Store) NoteIDByURI(ctx context.Context, uri string) (string, error) {
	return s.lookupID(ctx, noteURIPrefix, uri)
}

// UserIDByURI returns the ephemeral user ID assigned to an actor URI, or ""
// when absent.
//
// 同じ著者の 2 件目以降の投稿で同じ ID を再利用するために引く。これが無いと
// 投稿ごとに別 ID を採番してしまい、同一人物が別人として並ぶ。
func (s *Store) UserIDByURI(ctx context.Context, uri string) (string, error) {
	return s.lookupID(ctx, userURIPrefix, uri)
}

func (s *Store) lookupID(ctx context.Context, prefix, uri string) (string, error) {
	if s == nil || s.client == nil || uri == "" {
		return "", nil
	}
	id, err := s.client.Get(ctx, s.key(prefix, uri)).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// GetUser returns the ephemeral author for id, or nil when absent.
func (s *Store) GetUser(ctx context.Context, id string) (*model.User, error) {
	if s == nil || s.client == nil || id == "" {
		return nil, nil
	}
	raw, err := s.client.Get(ctx, s.key(userPrefix, id)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var u model.User
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// DropNote removes an ephemeral note and its URI mapping.
//
// materialize したときと、同じ URI が直接配送で DB に入ったときに呼ぶ。著者は
// 他のノートが参照しているかもしれないので消さない (TTL に任せる)。
func (s *Store) DropNote(ctx context.Context, id, uri string) error {
	if s == nil || s.client == nil || id == "" {
		return nil
	}
	keys := []string{s.key(notePrefix, id)}
	if uri != "" {
		keys = append(keys, s.key(noteURIPrefix, uri))
	}
	return s.client.Del(ctx, keys...).Err()
}

// Touch extends the TTL of a note (and its author) so that entries being
// actively viewed do not expire out from under the reader.
//
// 閲覧では materialize しない方針 (#2332) なので、詳細を開いた直後に TTL 切れ
// でリアクションできなくなる穴が空く。読み取り時に打ち直して塞ぐ。
func (s *Store) Touch(ctx context.Context, n *model.Note) error {
	if s == nil || s.client == nil || n == nil || n.ID == "" {
		return nil
	}
	ttl := s.noteTTL()
	pipe := s.client.Pipeline()
	pipe.Expire(ctx, s.key(notePrefix, n.ID), ttl)
	if n.URI != nil && *n.URI != "" {
		pipe.Expire(ctx, s.key(noteURIPrefix, *n.URI), ttl)
	}
	if n.UserID != "" {
		pipe.Expire(ctx, s.key(userPrefix, n.UserID), ttl*userTTLFactor)
	}
	_, err := pipe.Exec(ctx)
	return err
}
