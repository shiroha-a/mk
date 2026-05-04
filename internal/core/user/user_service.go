// Package user provides core business logic services for users.
package user

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// MaxPinnedNotes is the upper limit on pinned notes per user.
// Misskey本家のデフォルトと同じく5件。
const MaxPinnedNotes = 5

// Errors returned by Service.
var (
	// ErrUserNotFound is returned when the target user does not exist.
	ErrUserNotFound = errors.New("user not found")
	// ErrFailedToResolveRemoteUser is returned when ShowByUsername is called
	// with a non-local host and remote resolution fails. Handlers map this
	// to the dedicated FAILED_TO_RESOLVE_REMOTE_USER API error.
	ErrFailedToResolveRemoteUser = errors.New("failed to resolve remote user")
	// ErrInvalidParam is returned when neither userId nor username is given.
	ErrInvalidParam = errors.New("userId or username is required")
	// ErrNoteNotFound is returned when the target note does not exist or is
	// not owned by the requesting user.
	ErrNoteNotFound = errors.New("note not found")
	// ErrAlreadyPinned is returned when pinning an already-pinned note.
	ErrAlreadyPinned = errors.New("already pinned")
	// ErrPinLimitExceeded is returned when the user already has MaxPinnedNotes notes pinned.
	ErrPinLimitExceeded = errors.New("pin limit exceeded")
	// ErrPinNotFound is returned when unpinning a note that is not pinned.
	ErrPinNotFound = errors.New("pin not found")
	// ErrAvatarNotFound is returned when avatarId points to a missing or
	// non-owned drive_file. Mirrors upstream Misskey's NO_SUCH_AVATAR.
	ErrAvatarNotFound = errors.New("avatar drive file not found")
	// ErrAvatarNotImage is returned when avatarId points to a non-image
	// drive_file. Mirrors upstream's AVATAR_NOT_AN_IMAGE.
	ErrAvatarNotImage = errors.New("avatar drive file is not an image")
	// ErrBannerNotFound / ErrBannerNotImage are the banner counterparts.
	ErrBannerNotFound = errors.New("banner drive file not found")
	ErrBannerNotImage = errors.New("banner drive file is not an image")
)

// MainStreamPublisher emits real-time events to a single target user's `main`
// WebSocket channel. Used here for `meUpdated` so the frontend can reflect
// profile changes immediately. 循環依存を避けるため interface で受け取る
// (実装は internal/stream)。
type MainStreamPublisher interface {
	PublishMainEvent(userID, eventType string, body any)
}

// RemoteUserResolver resolves a (username, host) pair into a local User row,
// fetching from the remote host via WebFinger + ActivityPub when the user is
// not yet cached locally. 循環依存を避けるため interface で受け取る (実装は
// core/federation)。
type RemoteUserResolver interface {
	ResolveByUsernameHost(username, host string) (*model.User, error)
}

// Service provides user-related business logic.
type Service struct {
	userRepo            repository.UserRepository
	noteRepo            repository.NoteRepository
	piningRepo          repository.UserNotePiningRepository
	idGen               id.Generator
	mainStreamPublisher MainStreamPublisher
	remoteResolver      RemoteUserResolver
	// driveFileRepo は avatar / banner SET 時に drive_file 行を引いて
	// 所有者検証 + image MIME チェックするのに使う。未配線時は media
	// 更新経路全体を skip して旧来挙動 (avatar / banner 不変) に戻す。
	driveFileRepo repository.DriveFileRepository
}

// NewService creates a new user Service.
// noteRepo, piningRepo, idGen are optional for callers that only need the
// read-only show methods (pass nil); the pin-related methods require them.
func NewService(
	userRepo repository.UserRepository,
	noteRepo repository.NoteRepository,
	piningRepo repository.UserNotePiningRepository,
	idGen id.Generator,
) *Service {
	return &Service{
		userRepo:   userRepo,
		noteRepo:   noteRepo,
		piningRepo: piningRepo,
		idGen:      idGen,
	}
}

// SetMainStreamPublisher attaches a publisher used to emit `main` channel
// events (currently `meUpdated`). Optional — nil disables emit.
func (s *Service) SetMainStreamPublisher(p MainStreamPublisher) {
	s.mainStreamPublisher = p
}

// SetDriveFileRepository attaches a DriveFileRepository used by
// UpdateProfile to resolve avatarId / bannerId into the corresponding
// drive_file row (for ownership and image-MIME validation, plus
// avatarUrl / avatarBlurhash population). Optional — nil leaves the
// avatar / banner update paths inert (the input fields are silently
// ignored, matching pre-#467 behaviour).
func (s *Service) SetDriveFileRepository(repo repository.DriveFileRepository) {
	s.driveFileRepo = repo
}

// SetRemoteUserResolver attaches a resolver for remote (non-local) users.
// When set, ShowByUsername falls back to remote fetch (WebFinger + AP) for
// DB misses with non-nil host. Optional — nil disables remote fallback and
// leaves ShowByUsername DB-only.
func (s *Service) SetRemoteUserResolver(r RemoteUserResolver) {
	s.remoteResolver = r
}

// UserWithProfile bundles a user and its profile for handlers.
type UserWithProfile struct {
	User    *model.User
	Profile *model.UserProfile
}

// ShowByID returns the user (and profile) for the given ID.
func (s *Service) ShowByID(id string) (*UserWithProfile, error) {
	u, err := s.userRepo.FindByID(id)
	if err != nil {
		return nil, ErrUserNotFound
	}
	// Profileの取得失敗は致命ではないので無視する
	profile, _ := s.userRepo.FindProfileByUserID(u.ID)
	return &UserWithProfile{User: u, Profile: profile}, nil
}

// ShowManyByIDs returns the users (and their profiles) for the given ID set
// in a constant number of queries (1 user batch + 1 profile batch). Order
// matches `ids`; missing rows are skipped silently. Replaces the per-ID
// ShowByID loop in the users/show bulk path (#503).
func (s *Service) ShowManyByIDs(ids []string) ([]*UserWithProfile, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	users, err := s.userRepo.FindManyByIDs(ids)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return nil, nil
	}
	foundIDs := make([]string, 0, len(users))
	for _, u := range users {
		foundIDs = append(foundIDs, u.ID)
	}
	profiles, err := s.userRepo.FindProfilesByUserIDs(foundIDs)
	if err != nil {
		// Profile fetch 失敗は ShowByID と挙動を合わせて非致命にする
		// (個別 ShowByID も err を握りつぶしている)。
		profiles = nil
	}
	profileByUser := make(map[string]*model.UserProfile, len(profiles))
	for _, p := range profiles {
		profileByUser[p.UserID] = p
	}
	userByID := make(map[string]*model.User, len(users))
	for _, u := range users {
		userByID[u.ID] = u
	}
	out := make([]*UserWithProfile, 0, len(ids))
	for _, id := range ids {
		u, ok := userByID[id]
		if !ok {
			continue
		}
		out = append(out, &UserWithProfile{User: u, Profile: profileByUser[u.ID]})
	}
	return out, nil
}

// ShowByUsername returns the user (and profile) for the given username and host.
//
// host が nil もしくは空の場合はローカルユーザーとして DB を参照するのみ。
// host が指定されていてローカル DB に該当が無い場合、RemoteUserResolver が
// 設定されていれば WebFinger + ActivityPub 経由でリモート fetch を試みる。
// resolver 未設定の場合は ErrUserNotFound を返し (後方互換)、設定済みで解決
// に失敗した場合は ErrFailedToResolveRemoteUser を返す。
func (s *Service) ShowByUsername(username string, host *string) (*UserWithProfile, error) {
	u, err := s.userRepo.FindByUsernameLower(username, host)
	if err == nil {
		profile, _ := s.userRepo.FindProfileByUserID(u.ID)
		return &UserWithProfile{User: u, Profile: profile}, nil
	}
	// ローカル DB miss。host 指定なしや resolver 未注入の場合は従来どおり
	// ErrUserNotFound を返し、handler 側で NO_SUCH_USER にマップさせる。
	if host == nil || *host == "" || s.remoteResolver == nil {
		return nil, ErrUserNotFound
	}
	resolved, resolveErr := s.remoteResolver.ResolveByUsernameHost(username, *host)
	if resolveErr != nil || resolved == nil {
		return nil, ErrFailedToResolveRemoteUser
	}
	profile, _ := s.userRepo.FindProfileByUserID(resolved.ID)
	return &UserWithProfile{User: resolved, Profile: profile}, nil
}

// GetProfile returns the profile for the given user ID, or nil if not found.
func (s *Service) GetProfile(userID string) *model.UserProfile {
	profile, err := s.userRepo.FindProfileByUserID(userID)
	if err != nil {
		return nil
	}
	return profile
}

// GetProfilesByUserIDs returns a userID → profile map for the given IDs in
// a single batch query. Missing rows are simply omitted from the map.
// users/search 等の bulk path で per-row GetProfile loop の N+1 を消すために
// 使う (#517)。
func (s *Service) GetProfilesByUserIDs(ids []string) map[string]*model.UserProfile {
	if len(ids) == 0 {
		return map[string]*model.UserProfile{}
	}
	profiles, err := s.userRepo.FindProfilesByUserIDs(ids)
	if err != nil {
		return map[string]*model.UserProfile{}
	}
	out := make(map[string]*model.UserProfile, len(profiles))
	for _, p := range profiles {
		out[p.UserID] = p
	}
	return out
}

// ListRecommendations returns locally-active explorable users the viewer does
// not already follow. Thin wrapper over UserRepository.ListUserRecommendations.
func (s *Service) ListRecommendations(viewerID string, activeSince time.Time, limit, offset int) ([]*model.User, error) {
	return s.userRepo.ListUserRecommendations(viewerID, activeSince, limit, offset)
}

// Search returns users whose username matches the prefix query.
// 空のクエリは空のリストを返す。
func (s *Service) Search(query string, limit, offset int) ([]*model.User, error) {
	q := strings.TrimSpace(strings.TrimPrefix(query, "@"))
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	return s.userRepo.SearchByUsername(strings.ToLower(q), limit, offset)
}

// UpdateInput represents the editable fields of a user profile.
// Each pointer is interpreted as "leave unchanged when nil".
type UpdateInput struct {
	Name              **string
	Description       **string
	Location          **string
	Birthday          **string
	Lang              **string
	FollowedMessage   **string
	PublicReactions   *bool
	IsLocked          *bool
	IsBot             *bool
	IsCat             *bool
	IsExplorable      *bool
	HideOnlineStatus  *bool
	AlwaysMarkNsfw    *bool
	AutoSensitive     *bool
	NoCrawle          *bool
	PreventAiLearning *bool
	// ChatScope は誰からのチャットを許可するか (1-on-1 DM 用)。
	// 受け付けるのは "everyone" / "followers" / "following" / "mutual" / "none"
	// (CherryPick / Misskey TS と同じ enum)。検証は呼び出し側 (#692)。
	ChatScope *string
	// Room は jsonb 列に書き込む生バイト列。nil の場合は更新しない。
	// 呼び出し側で JSON として妥当であることを保証する必要がある。
	Room *json.RawMessage
	// AvatarID / BannerID は drive_file の ID。
	//   nil       → 不変 (no change)
	//   &"<id>"   → SET (drive_file 行を引いて所有権 + image MIME 検証)
	//   &""       → CLEAR (avatarId / avatarUrl / avatarBlurhash を NULL に)
	//
	// JSON `null` を CLEAR として扱う semantic はサポートしない (Go の
	// *string では null と omitted を区別できないため #467 では空文字列
	// を CLEAR の sentinel にする)。frontend が null clear を要求する
	// ようになったら json.RawMessage ベースに昇格させる別 issue。
	AvatarID *string
	BannerID *string
	// AvatarDecorations は jsonb カラム `user.avatarDecorations` に書き込む
	// 正規化済み JSON バイト列。nil なら不変、`[]` なら全外し、
	// `[{id,angle,flipH,offsetX,offsetY}, ...]` で上書き。検証 (catalog
	// 存在 / role 制限 / 個数上限) はハンドラ側で実施済 (#521)。
	AvatarDecorations *[]byte
}

// UpdateProfile applies the non-nil fields to the user and user_profile rows.
func (s *Service) UpdateProfile(userID string, in UpdateInput) (*UserWithProfile, error) {
	if _, err := s.userRepo.FindByID(userID); err != nil {
		return nil, ErrUserNotFound
	}

	userFields := map[string]any{}
	profileFields := map[string]any{}

	if in.Name != nil {
		userFields["name"] = *in.Name
	}
	if in.IsLocked != nil {
		userFields["isLocked"] = *in.IsLocked
	}
	if in.IsBot != nil {
		userFields["isBot"] = *in.IsBot
	}
	if in.IsCat != nil {
		userFields["isCat"] = *in.IsCat
	}
	if in.IsExplorable != nil {
		userFields["isExplorable"] = *in.IsExplorable
	}
	if in.HideOnlineStatus != nil {
		userFields["hideOnlineStatus"] = *in.HideOnlineStatus
	}
	if in.Description != nil {
		profileFields["description"] = *in.Description
	}
	if in.Location != nil {
		profileFields["location"] = *in.Location
	}
	if in.Birthday != nil {
		profileFields["birthday"] = *in.Birthday
	}
	if in.Lang != nil {
		profileFields["lang"] = *in.Lang
	}
	if in.FollowedMessage != nil {
		profileFields["followedMessage"] = *in.FollowedMessage
	}
	if in.PublicReactions != nil {
		profileFields["publicReactions"] = *in.PublicReactions
	}
	if in.AlwaysMarkNsfw != nil {
		profileFields["alwaysMarkNsfw"] = *in.AlwaysMarkNsfw
	}
	if in.AutoSensitive != nil {
		profileFields["autoSensitive"] = *in.AutoSensitive
	}
	if in.NoCrawle != nil {
		profileFields["noCrawle"] = *in.NoCrawle
	}
	if in.PreventAiLearning != nil {
		profileFields["preventAiLearning"] = *in.PreventAiLearning
	}
	if in.ChatScope != nil {
		userFields["chatScope"] = *in.ChatScope
	}
	if in.Room != nil {
		// GORM は map で渡された値を jsonb 列に直接書き込む。
		// []byte を渡すと bytea 扱いされてしまうので string にキャストする。
		profileFields["room"] = string(*in.Room)
	}
	if in.AvatarDecorations != nil {
		// avatarDecorations は user (not user_profile) 側の jsonb 列。
		// Room と同じく []byte ではなく string で渡して bytea 化を防ぐ。
		userFields["avatarDecorations"] = string(*in.AvatarDecorations)
	}

	// avatarId / bannerId 更新 (#467)。driveFileRepo 未配線時は media
	// 更新経路を skip して旧 behaviour に戻す (テスト互換維持)。
	if s.driveFileRepo != nil {
		if err := s.applyMediaUpdate(userID, in.AvatarID, "avatar", userFields, ErrAvatarNotFound, ErrAvatarNotImage); err != nil {
			return nil, err
		}
		if err := s.applyMediaUpdate(userID, in.BannerID, "banner", userFields, ErrBannerNotFound, ErrBannerNotImage); err != nil {
			return nil, err
		}
	}

	if err := s.userRepo.UpdateUser(userID, userFields); err != nil {
		return nil, err
	}
	if err := s.userRepo.UpdateProfile(userID, profileFields); err != nil {
		return nil, err
	}

	bundle, err := s.ShowByID(userID)
	if err != nil {
		return nil, err
	}
	// TS本家 UserEntityService.updateMe() 完了後に `meUpdated` を自分の main
	// に publish する (プロフィールページ / UI 側 state を即時再描画させる
	// ため)。body は packed UserDetailed full object。
	s.publishMeUpdated(bundle)
	return bundle, nil
}

// applyMediaUpdate writes prefix+"Id" / prefix+"Url" / prefix+"Blurhash"
// onto userFields based on idPtr's tri-state semantics:
//
//   - nil pointer       → no change (skip)
//   - &""               → CLEAR: set all three columns to NULL
//   - &"<drive_file_id>" → SET: lookup drive_file, validate ownership +
//     image MIME, copy URL / Blurhash to user.
//
// `prefix` is "avatar" or "banner". notFoundErr / notImageErr are the
// specific sentinel errors callers want surfaced (handler maps them to
// distinct API error codes).
func (s *Service) applyMediaUpdate(userID string, idPtr *string, prefix string, userFields map[string]any, notFoundErr, notImageErr error) error {
	if idPtr == nil {
		return nil
	}
	if *idPtr == "" {
		// CLEAR — frontend が "" を送った想定 (JSON null は Go の *string
		// で omitted と区別できないため空文字列を sentinel にする)。
		userFields[prefix+"Id"] = nil
		userFields[prefix+"Url"] = nil
		userFields[prefix+"Blurhash"] = nil
		return nil
	}
	file, err := s.driveFileRepo.FindByID(*idPtr)
	if err != nil || file == nil {
		return notFoundErr
	}
	// 他人の drive_file を avatar / banner に勝手に指定できないように
	// 所有権を検証する (upstream Misskey と同じ)。userId が NULL
	// (未紐付け) も拒否対象。
	if file.UserID == nil || *file.UserID != userID {
		return notFoundErr
	}
	if !strings.HasPrefix(file.Type, "image/") {
		return notImageErr
	}
	userFields[prefix+"Id"] = file.ID
	userFields[prefix+"Url"] = file.URL
	if file.Blurhash != nil {
		userFields[prefix+"Blurhash"] = *file.Blurhash
	} else {
		userFields[prefix+"Blurhash"] = nil
	}
	return nil
}

// publishMeUpdated emits `meUpdated` to the user's main channel with the
// packed UserDetailed object. No-op when no publisher is attached or the
// bundle is missing its User. PinnedNoteIDs is populated from piningRepo
// so pin/unpin changes are reflected in the event body.
func (s *Service) publishMeUpdated(bundle *UserWithProfile) {
	if s.mainStreamPublisher == nil || bundle == nil || bundle.User == nil {
		return
	}
	// profile update / pin / unpin は頻度が低く hot path ではないため
	// full pack する (UserLite ではなく UserDetailed)。
	body := entity.PackUserDetailed(bundle.User, bundle.Profile, s.idGen)
	// PackUserDetailed は pinnedNoteIDs を空で返すため、piningRepo が
	// あれば pin ID だけ埋める (full note pack は不要で frontend 側で
	// 必要に応じて fetch する)。piningRepo がない read-only 構成や
	// 取得失敗時はそのまま空で emit する (best-effort)。
	if s.piningRepo != nil {
		if pinings, err := s.piningRepo.ListByUser(bundle.User.ID); err == nil {
			ids := make([]string, 0, len(pinings))
			for _, p := range pinings {
				ids = append(ids, p.NoteID)
			}
			body.PinnedNoteIDs = ids
		}
	}
	s.mainStreamPublisher.PublishMainEvent(bundle.User.ID, "meUpdated", body)
}

// emitMeUpdatedForUser re-fetches the user bundle and publishes `meUpdated`.
// Returns early when no publisher is attached so the ShowByID DB read is
// avoided. contextLabel is used only in the warning log.
func (s *Service) emitMeUpdatedForUser(userID, contextLabel string) {
	if s.mainStreamPublisher == nil {
		return
	}
	bundle, err := s.ShowByID(userID)
	if err != nil {
		slog.Warn("user: ShowByID failed, skipping meUpdated emit", "context", contextLabel, "userID", userID, "err", err)
		return
	}
	s.publishMeUpdated(bundle)
}

// PinNote pins the given note to the user's profile.
// Returns ErrNoteNotFound if the note doesn't exist or isn't owned by the user,
// ErrAlreadyPinned, or ErrPinLimitExceeded.
func (s *Service) PinNote(userID, noteID string) error {
	note, err := s.noteRepo.FindByID(noteID)
	if err != nil {
		return ErrNoteNotFound
	}
	if note.UserID != userID {
		return ErrNoteNotFound
	}

	if _, err := s.piningRepo.FindByPair(userID, noteID); err == nil {
		return ErrAlreadyPinned
	}

	count, err := s.piningRepo.CountByUser(userID)
	if err != nil {
		return err
	}
	if count >= MaxPinnedNotes {
		return ErrPinLimitExceeded
	}

	p := &model.UserNotePining{
		ID:     s.idGen.Generate(time.Now()),
		UserID: userID,
		NoteID: noteID,
	}
	if err := s.piningRepo.Create(p); err != nil {
		return err
	}
	// pinnedNoteIds が変わったので main に meUpdated を publish して
	// UI を即時同期する (TS本家 UserEntityService.pinNote と同等)。
	// best-effort emit: publisher 未注入なら何もせず、ShowByID 失敗時は
	// ログのみ残して pin 自体の成功は返す。
	s.emitMeUpdatedForUser(userID, "PinNote")
	return nil
}

// UnpinNote removes a pinning entry. Returns ErrPinNotFound if the user has
// not pinned the given note.
func (s *Service) UnpinNote(userID, noteID string) error {
	p, err := s.piningRepo.FindByPair(userID, noteID)
	if err != nil {
		return ErrPinNotFound
	}
	if err := s.piningRepo.Delete(p); err != nil {
		return err
	}
	s.emitMeUpdatedForUser(userID, "UnpinNote")
	return nil
}

// ListPinnedNotes returns the notes pinned by userID, in pinning order
// (most recent pin first).
func (s *Service) ListPinnedNotes(userID string) ([]*model.Note, error) {
	pinings, err := s.piningRepo.ListByUser(userID)
	if err != nil {
		return nil, err
	}
	if len(pinings) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(pinings))
	for _, p := range pinings {
		ids = append(ids, p.NoteID)
	}
	return s.noteRepo.FindManyByIDsWithUser(ids)
}

// UpdateUserFields updates arbitrary fields on the user table.
func (s *Service) UpdateUserFields(userID string, fields map[string]any) error {
	return s.userRepo.UpdateUser(userID, fields)
}

// UpdateProfileFields updates arbitrary fields on the user_profile table.
func (s *Service) UpdateProfileFields(userID string, fields map[string]any) error {
	return s.userRepo.UpdateProfile(userID, fields)
}

// FindProfileByVerifyCode looks up a profile by emailVerifyCode.
func (s *Service) FindProfileByVerifyCode(code string) (*model.UserProfile, error) {
	return s.userRepo.FindProfileByVerifyCode(code)
}
