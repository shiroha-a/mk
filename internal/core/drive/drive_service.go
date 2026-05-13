package drive

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"gorm.io/datatypes"
)

// Errors returned by Service.
var (
	// ErrFileNotFound is returned when the target file does not exist.
	ErrFileNotFound = errors.New("drive file not found")
	// ErrFolderNotFound is returned when the target folder does not exist.
	ErrFolderNotFound = errors.New("drive folder not found")
	// ErrParentFolderNotFound is returned when the parent folder referenced
	// by a create/update operation does not exist. Distinct from
	// ErrFolderNotFound so that drive/folders/update can map this to the
	// upstream NO_SUCH_PARENT_FOLDER code (#977).
	ErrParentFolderNotFound = errors.New("drive parent folder not found")
	// ErrAccessDenied is returned when a file/folder is owned by another user.
	ErrAccessDenied = errors.New("access denied")
	// ErrFolderNotEmpty is returned when attempting to delete a folder that
	// still has children.
	ErrFolderNotEmpty = errors.New("folder is not empty")
	// ErrUnallowedFileType is returned when an upload's MIME type is not
	// covered by the user's `uploadableFileTypes` role policy. Handler
	// translates this into upstream's UNALLOWED_FILE_TYPE error code
	// (UUID 4becd248-...) (#1028).
	ErrUnallowedFileType = errors.New("unallowed file type")
	// ErrCannotUnmarkSensitive is returned when a user with the
	// `alwaysMarkNsfw=true` role policy tries to clear `isSensitive` on
	// their own drive file. Handler translates this into the drive-specific
	// RESTRICTED_BY_ROLE response (UUID 7f59dccb-...) (#1028).
	ErrCannotUnmarkSensitive = errors.New("cannot unmark sensitive while alwaysMarkNsfw policy is active")
)

// StreamingPublisher receives drive file life-cycle events so that
// WebSocket subscribers (the "drive" channel) can be pushed in real time.
// パッケージ間の循環依存を避けるためinterfaceで受け取る(実装はinternal/
// stream)。eventTypeは"fileCreated"/"fileUpdated"/"fileDeleted"。
type StreamingPublisher interface {
	PublishDriveEvent(userID, eventType string, file *model.DriveFile)
}

// MainStreamPublisher emits real-time events to a single target user's `main`
// WebSocket channel. Used here to deliver `driveFileCreated` so the frontend
// can reflect uploads across screens (e.g. drop-upload UI).
// 循環依存を避けるためinterfaceで受け取る(実装はinternal/stream)。
type MainStreamPublisher interface {
	PublishMainEvent(userID, eventType string, body any)
}

// ChartHook is invoked after a drive file has been uploaded or deleted
// so the chart subsystem can record the byte / count delta. パッケージ
// 間の循環依存を避けるためinterfaceで受け取る(実装はcore/chart/
// charthook)。
type ChartHook interface {
	OnFileUploaded(file *model.DriveFile)
	OnFileDeleted(file *model.DriveFile)
}

// RoleChecker abstracts moderator-role + policy lookups so Show /
// Upload / Update can honour upstream Misskey's role-policy semantics
// without taking a hard dep on core/role (avoids circular imports).
//
// IsModerator is consumed by Show ("moderators can view any file") and
// Upload (moderators bypass uploadableFileTypes allowlist).
// GetUserPolicies is consumed by Upload (alwaysMarkNsfw / uploadableFileTypes)
// and Update (alwaysMarkNsfw guard against clearing isSensitive). nil
// policies map disables the gate (= test fixture). (#1028)
type RoleChecker interface {
	IsModerator(userID string) bool
	GetUserPolicies(userID string) map[string]any
}

// Service manages drive files and folders.
type Service struct {
	fileRepo            repository.DriveFileRepository
	folderRepo          repository.DriveFolderRepository
	storage             Storage
	idGen               id.Generator
	publisher           StreamingPublisher
	mainStreamPublisher MainStreamPublisher
	chartHook           ChartHook
	imageProcessor      ImageProcessor
	videoProcessor      VideoProcessor
	// Sensitive media detection
	sensitiveDetector SensitiveDetector
	sensitiveCfg      SensitiveConfig
	// Optional moderator bypass for Show. Nil keeps the default "owner-only"
	// guard.
	roleChecker RoleChecker
}

// SetRoleChecker wires a moderator role lookup so Show allows moderators to
// inspect any file (matching upstream Misskey's drive/files/show behaviour).
// Nil keeps the existing owner-only check.
func (s *Service) SetRoleChecker(rc RoleChecker) { s.roleChecker = rc }

// SetSensitiveDetection attaches the sensitive media detector and config.
func (s *Service) SetSensitiveDetection(detector SensitiveDetector, cfg SensitiveConfig) {
	s.sensitiveDetector = detector
	s.sensitiveCfg = cfg
}

// NewService constructs a DriveService.
func NewService(
	fileRepo repository.DriveFileRepository,
	folderRepo repository.DriveFolderRepository,
	storage Storage,
	idGen id.Generator,
) *Service {
	return &Service{
		fileRepo:   fileRepo,
		folderRepo: folderRepo,
		storage:    storage,
		idGen:      idGen,
	}
}

// SetStreamingPublisher attaches a StreamingPublisher invoked best-effort
// after Upload / Update / Delete succeed.
func (s *Service) SetStreamingPublisher(p StreamingPublisher) {
	s.publisher = p
}

// SetMainStreamPublisher attaches a publisher used to emit `driveFileCreated`
// on the uploader's main channel. Optional — nil disables main-stream emit
// but the drive-channel `fileCreated` event remains unaffected.
func (s *Service) SetMainStreamPublisher(p MainStreamPublisher) {
	s.mainStreamPublisher = p
}

// SetChartHook attaches a ChartHook invoked best-effort after a drive
// file has been uploaded or deleted.
func (s *Service) SetChartHook(h ChartHook) {
	s.chartHook = h
}

// SetImageProcessor attaches an ImageProcessor for thumbnail/webpublic
// generation during upload.
func (s *Service) SetImageProcessor(p ImageProcessor) {
	s.imageProcessor = p
}

// SetVideoProcessor attaches a VideoProcessor for video thumbnail
// extraction during upload.
func (s *Service) SetVideoProcessor(p VideoProcessor) {
	s.videoProcessor = p
}

// publishEvent is a tiny best-effort wrapper around publisher.PublishDriveEvent.
func (s *Service) publishEvent(userID, eventType string, f *model.DriveFile) {
	if s.publisher == nil || userID == "" {
		return
	}
	s.publisher.PublishDriveEvent(userID, eventType, f)
}

// UploadInput is the parameter set for Service.Upload.
type UploadInput struct {
	User        *model.User
	Body        []byte
	Name        string
	Comment     *string
	FolderID    *string
	IsSensitive bool
	Force       bool // if true, do not deduplicate by md5 hash
}

// Upload writes a file to storage and creates a drive_file row. もし同じmd5の
// ファイルが既に存在し Force=false なら、既存レコードを返す (deduplication)。
//
// in.User == nil は「system 所有 drive file」を表現する (Misskey TS の
// driveService.uploadFromUrl({user: null, ...}) と等価)。system file は
// 特定 user に紐付かないため md5 dedup / folder 所有権チェック / sensitive
// 検出 / stream publish はいずれも skip する。custom emoji のリモート→
// local コピー (#670) など、誰のものでもないがインスタンスが管理する
// アセット用。
//
// ctx は upload リクエストの context (#749)。sensitive media detection の
// 外部 detector 呼び出しでこの ctx を伝播するため、caller は client が
// 切断したら detector もキャンセルされることを期待できる。
func (s *Service) Upload(ctx context.Context, in UploadInput) (*model.DriveFile, error) {
	// in.Body は []byte なので bytes.Reader 経由のAnalyseFileは失敗しない。
	// AnalyseFile自体は io.Reader を取るがエラー経路はここでは到達しない。
	info, _ := AnalyseFile(bytes.NewReader(in.Body))

	// role policy gate (#1028)。system file (in.User == nil) は policy 対象外。
	// upstream Misskey TS DriveService と同様、moderator は uploadableFileTypes
	// allowlist を bypass し、それ以外の user は allowlist match を要求する。
	// alwaysMarkNsfw policy が true なら IsSensitive を強制 true にし、
	// sensitive detection は実行しても無意味なので skip フラグを立てる。
	var rolePolicyForceSensitive bool
	if in.User != nil && s.roleChecker != nil {
		isModerator := s.roleChecker.IsModerator(in.User.ID)
		policies := s.roleChecker.GetUserPolicies(in.User.ID)
		if !isModerator && policies != nil {
			if !mimeAllowedByPolicy(info.MimeType, policies["uploadableFileTypes"]) {
				return nil, ErrUnallowedFileType
			}
		}
		if policies != nil {
			if v, ok := policies["alwaysMarkNsfw"].(bool); ok && v {
				rolePolicyForceSensitive = true
				in.IsSensitive = true
			}
		}
	}

	if in.User != nil && !in.Force {
		if existing, err := s.fileRepo.FindByMD5(in.User.ID, info.MD5); err == nil {
			return existing, nil
		}
	}

	// folderの所有権チェック (system file は folder を持たない前提なので skip)
	if in.User != nil && in.FolderID != nil {
		folder, err := s.folderRepo.FindByID(*in.FolderID)
		if err != nil {
			return nil, ErrFolderNotFound
		}
		if folder.UserID == nil || *folder.UserID != in.User.ID {
			return nil, ErrAccessDenied
		}
	}

	accessKey, err := newAccessKey()
	if err != nil {
		return nil, err
	}
	url, err := s.storage.Put(accessKey, bytes.NewReader(info.Body))
	if err != nil {
		return nil, err
	}

	// 画像/動画処理 (best-effort)
	var thumbnail, webpublic *ProcessedImage
	var blurhash *string
	var properties datatypes.JSON

	alts := s.generateAlts(in.Body, info.MimeType)
	if alts != nil {
		thumbnail = alts.thumbnail
		webpublic = alts.webpublic
		blurhash = alts.blurhash
		properties = alts.properties
	}

	// サムネイル/webpublic を storage に保存
	var thumbnailURL, webpublicURL *string
	var thumbnailAccessKey, webpublicAccessKey *string
	var webpublicType *string

	if thumbnail != nil {
		tKey, err := newAccessKey()
		if err == nil {
			tURL, err := s.storage.Put(tKey, bytes.NewReader(thumbnail.Data))
			if err == nil {
				thumbnailURL = &tURL
				thumbnailAccessKey = &tKey
			}
		}
	}
	if webpublic != nil {
		wKey, err := newAccessKey()
		if err == nil {
			wURL, err := s.storage.Put(wKey, bytes.NewReader(webpublic.Data))
			if err == nil {
				webpublicURL = &wURL
				webpublicAccessKey = &wKey
				webpublicType = &webpublic.MimeType
			}
		}
	}

	now := time.Now()
	fileID := s.idGen.Generate(now)
	var userIDPtr *string
	var userHostPtr *string
	if in.User != nil {
		uid := in.User.ID
		userIDPtr = &uid
		userHostPtr = in.User.Host
	}
	f := &model.DriveFile{
		ID:                 fileID,
		UserID:             userIDPtr,
		UserHost:           userHostPtr,
		MD5:                info.MD5,
		Name:               in.Name,
		Type:               info.MimeType,
		Size:               info.Size,
		Comment:            in.Comment,
		Blurhash:           blurhash,
		Properties:         properties,
		StoredInternal:     true,
		URL:                url,
		ThumbnailURL:       thumbnailURL,
		WebpublicURL:       webpublicURL,
		WebpublicType:      webpublicType,
		AccessKey:          &accessKey,
		ThumbnailAccessKey: thumbnailAccessKey,
		WebpublicAccessKey: webpublicAccessKey,
		FolderID:           in.FolderID,
		IsSensitive:        in.IsSensitive,
	}

	// Sensitive media detection フック (system file は user 紐付きが無いので skip)
	// alwaysMarkNsfw role policy が true の user は既に IsSensitive=true で
	// 確定しているので detect の意味がない、skip して負荷を減らす (#1028)。
	if !f.IsSensitive && !rolePolicyForceSensitive && in.User != nil {
		f.IsSensitive = s.detectSensitive(ctx, in.User, in.Body, info.MimeType)
	}

	if err := s.fileRepo.Create(f); err != nil {
		// ロールバックとして storage を削除する
		_ = s.storage.Delete(accessKey)
		if thumbnailAccessKey != nil {
			_ = s.storage.Delete(*thumbnailAccessKey)
		}
		if webpublicAccessKey != nil {
			_ = s.storage.Delete(*webpublicAccessKey)
		}
		return nil, err
	}
	// system file (in.User == nil) は誰の UI にも反映する必要が無いので
	// stream publish 系を skip する。publishEvent / mainStreamPublisher は
	// それぞれ userID を必要とする。
	if in.User != nil {
		s.publishEvent(in.User.ID, "fileCreated", f)
		// Misskey本家DriveService.addFileはdrive topicに加えてmainにも
		// driveFileCreatedをemitしてUploader自身のUI(ドロップアップロード等)
		// を即時反映させる(TS: DriveService.ts:665)。
		if s.mainStreamPublisher != nil {
			s.mainStreamPublisher.PublishMainEvent(in.User.ID, "driveFileCreated", entity.PackDriveFile(f, s.idGen))
		}
	}
	// chartHook は user nil でも発火させる: bytes / count は instance-wide
	// stats なので custom emoji 等の system file もインスタンスストレージの
	// 一部として計上したい (uploader UI 反映の publishEvent とは異なる扱い)。
	if s.chartHook != nil {
		s.chartHook.OnFileUploaded(f)
	}
	return f, nil
}

// detectSensitive runs sensitive media detection based on meta config.
// ベストエフォート: detector 未設定やエラー時は false を返す。
//
// ctx は upload リクエストの context を伝播する (#749)。client が早期に
// 切断したら detector 呼び出しもキャンセルされる。
func (s *Service) detectSensitive(ctx context.Context, user *model.User, body []byte, mime string) bool {
	cfg := s.sensitiveCfg

	// mediaSilencedHosts: リモートユーザーのホストが silenced ならば無条件 sensitive
	if user.Host != nil && IsSilencedHost(*user.Host, cfg.SilencedHosts) {
		return true
	}

	if !cfg.SetFlagAutomatically || s.sensitiveDetector == nil {
		return false
	}

	isLocal := user.Host == nil || *user.Host == ""
	if !ShouldDetect(cfg, isLocal) {
		return false
	}

	// 動画チェック: enableForVideos が false なら動画はスキップ
	if IsVideoMIME(mime) && !cfg.EnableForVideos {
		return false
	}

	score, err := s.sensitiveDetector.Detect(ctx, body, mime)
	if err != nil {
		// detector が落ちていても upload 自体は通す best-effort 実装だが、
		// silently 飲み込むと「自動付与が動いていない」ことに operator が
		// 気付けない (#750)。warn ログだけ残す。body 内容は出さない (privacy)。
		slog.Warn("drive: sensitive detector failed", "mime", mime, "err", err)
		return false
	}

	threshold := SensitivityThreshold(cfg.Sensitivity)
	return score >= threshold
}

// generateAltsResult holds the results of image/video processing.
type generateAltsResult struct {
	thumbnail  *ProcessedImage
	webpublic  *ProcessedImage
	blurhash   *string
	properties datatypes.JSON
}

// generateAlts runs image or video processing on the uploaded file.
// processor が nil の場合や処理失敗時は nil を返す (best-effort)。
func (s *Service) generateAlts(body []byte, mimeType string) *generateAltsResult {
	if isMimeImage(mimeType) && s.imageProcessor != nil {
		return s.processImage(body, mimeType)
	}
	if isMimeVideo(mimeType) && s.videoProcessor != nil {
		thumb, _ := s.videoProcessor.GenerateThumbnail(body, mimeType)
		if thumb != nil {
			return &generateAltsResult{thumbnail: thumb}
		}
	}
	return nil
}

// processImage runs all image processing steps (best-effort).
func (s *Service) processImage(body []byte, mimeType string) *generateAltsResult {
	result := &generateAltsResult{}

	// 寸法取得
	w, h, err := s.imageProcessor.GetDimensions(body, mimeType)
	if err == nil && (w > 0 || h > 0) {
		props := map[string]int{"width": w, "height": h}
		if data, err := json.Marshal(props); err == nil {
			result.properties = datatypes.JSON(data)
		}
	}

	// BlurHash
	if hash, err := s.imageProcessor.CalculateBlurhash(body, mimeType); err == nil && hash != "" {
		result.blurhash = &hash
	}

	// サムネイル
	result.thumbnail, _ = s.imageProcessor.GenerateThumbnail(body, mimeType)

	// Webpublic
	result.webpublic, _ = s.imageProcessor.GenerateWebpublic(body, mimeType)

	return result
}

// Show returns the file with id. Owners can read their own files; moderators
// (when a roleChecker is wired) can read any file. Matches upstream Misskey
// drive/files/show.ts semantics so admin moderation surfaces and remote-media
// detail views work without 403.
//
// 注意: write 経路 (Update / Delete) では moderator bypass を意図的に
// 適用しない。他ユーザーの file への moderator 経由の write/delete を
// 許してしまう privilege escalation を避けるため、それらは
// findOwnedFile() を使って owner-only ガードする (#499)。moderator が
// 他ユーザーの file を編集 / 削除する正規経路は admin/drive/* 側に
// 別途存在する。
func (s *Service) Show(user *model.User, id string) (*model.DriveFile, error) {
	if user == nil {
		return nil, errors.New("user is required")
	}
	f, err := s.fileRepo.FindByID(id)
	if err != nil {
		return nil, ErrFileNotFound
	}
	owner := f.UserID != nil && *f.UserID == user.ID
	moderator := s.roleChecker != nil && s.roleChecker.IsModerator(user.ID)
	if !owner && !moderator {
		return nil, ErrAccessDenied
	}
	return f, nil
}

// findOwnedFile is the strict owner-only equivalent of Show, used by write
// paths (Update / Delete). It never honours the moderator bypass so that
// drive write/delete operations can never escalate via moderator role.
func (s *Service) findOwnedFile(user *model.User, id string) (*model.DriveFile, error) {
	if user == nil {
		return nil, errors.New("user is required")
	}
	f, err := s.fileRepo.FindByID(id)
	if err != nil {
		return nil, ErrFileNotFound
	}
	if f.UserID == nil || *f.UserID != user.ID {
		return nil, ErrAccessDenied
	}
	return f, nil
}

// FindByHash returns the user's most recent file with the given md5 hash.
func (s *Service) FindByHash(user *model.User, md5 string) (*model.DriveFile, error) {
	if user == nil {
		return nil, errors.New("user is required")
	}
	f, err := s.fileRepo.FindByMD5(user.ID, md5)
	if err != nil {
		return nil, ErrFileNotFound
	}
	return f, nil
}

// UpdateInput holds the editable fields of a drive file.
type UpdateInput struct {
	Name        *string
	Comment     **string
	FolderID    **string
	IsSensitive *bool
}

// Update applies the non-nil fields to a drive file owned by the user.
// Owner-only: moderator が他ユーザーの file を編集できないよう
// findOwnedFile() を使う (#499)。
func (s *Service) Update(user *model.User, id string, in UpdateInput) (*model.DriveFile, error) {
	f, err := s.findOwnedFile(user, id)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if in.Name != nil {
		fields["name"] = *in.Name
	}
	if in.Comment != nil {
		fields["comment"] = *in.Comment
	}
	if in.FolderID != nil {
		// folderの所有権チェック
		if *in.FolderID != nil {
			folder, err := s.folderRepo.FindByID(**in.FolderID)
			if err != nil {
				return nil, ErrFolderNotFound
			}
			if folder.UserID == nil || *folder.UserID != user.ID {
				return nil, ErrAccessDenied
			}
		}
		fields["folderId"] = *in.FolderID
	}
	if in.IsSensitive != nil {
		// alwaysMarkNsfw role policy が true の user は isSensitive を false
		// に変更できない (upstream DriveService.updateFile と同 logic、#1028)。
		// 自分自身の policy なので file.userId == user.ID 前提で role を見る。
		// moderator bypass はしない (= owner が自分の policy を見るのみ)。
		if !*in.IsSensitive && f.IsSensitive && s.roleChecker != nil {
			policies := s.roleChecker.GetUserPolicies(user.ID)
			if policies != nil {
				if v, ok := policies["alwaysMarkNsfw"].(bool); ok && v {
					return nil, ErrCannotUnmarkSensitive
				}
			}
		}
		fields["isSensitive"] = *in.IsSensitive
	}
	if err := s.fileRepo.Update(f.ID, fields); err != nil {
		return nil, err
	}
	updated, err := s.fileRepo.FindByID(f.ID)
	if err != nil {
		return nil, err
	}
	s.publishEvent(user.ID, "fileUpdated", updated)
	return updated, nil
}

// Delete removes a file from storage and the database.
// Owner-only: moderator が他ユーザーの file を削除できないよう
// findOwnedFile() を使う (#499)。
func (s *Service) Delete(user *model.User, id string) error {
	f, err := s.findOwnedFile(user, id)
	if err != nil {
		return err
	}
	if f.AccessKey != nil {
		if err := s.storage.Delete(*f.AccessKey); err != nil {
			return err
		}
	}
	// サムネイル/webpublic の storage も削除
	if f.ThumbnailAccessKey != nil {
		_ = s.storage.Delete(*f.ThumbnailAccessKey)
	}
	if f.WebpublicAccessKey != nil {
		_ = s.storage.Delete(*f.WebpublicAccessKey)
	}
	if err := s.fileRepo.Delete(f); err != nil {
		return err
	}
	s.publishEvent(user.ID, "fileDeleted", f)
	if s.chartHook != nil {
		s.chartHook.OnFileDeleted(f)
	}
	return nil
}

// CreateFolder creates a new drive folder owned by user.
func (s *Service) CreateFolder(user *model.User, name string, parentID *string) (*model.DriveFolder, error) {
	if user == nil {
		return nil, errors.New("user is required")
	}
	if parentID != nil {
		parent, err := s.folderRepo.FindByID(*parentID)
		if err != nil {
			return nil, ErrFolderNotFound
		}
		if parent.UserID == nil || *parent.UserID != user.ID {
			return nil, ErrAccessDenied
		}
	}
	userID := user.ID
	f := &model.DriveFolder{
		ID:       s.idGen.Generate(time.Now()),
		Name:     name,
		UserID:   &userID,
		ParentID: parentID,
	}
	if err := s.folderRepo.Create(f); err != nil {
		return nil, err
	}
	return f, nil
}

// ShowFolder returns the folder with id, ensuring the user owns it.
func (s *Service) ShowFolder(user *model.User, id string) (*model.DriveFolder, error) {
	if user == nil {
		return nil, errors.New("user is required")
	}
	f, err := s.folderRepo.FindByID(id)
	if err != nil {
		return nil, ErrFolderNotFound
	}
	if f.UserID == nil || *f.UserID != user.ID {
		return nil, ErrAccessDenied
	}
	return f, nil
}

// UpdateFolderInput holds the editable fields of a drive folder.
type UpdateFolderInput struct {
	Name     *string
	ParentID **string
}

// UpdateFolder applies the non-nil fields to a folder owned by user.
func (s *Service) UpdateFolder(user *model.User, id string, in UpdateFolderInput) (*model.DriveFolder, error) {
	f, err := s.ShowFolder(user, id)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if in.Name != nil {
		fields["name"] = *in.Name
	}
	if in.ParentID != nil {
		if *in.ParentID != nil {
			parent, err := s.folderRepo.FindByID(**in.ParentID)
			if err != nil {
				// folders/update では parent が未存在のときに upstream は
				// NO_SUCH_PARENT_FOLDER (#977) を返すので、target 不在の
				// ErrFolderNotFound と区別する。
				return nil, ErrParentFolderNotFound
			}
			if parent.UserID == nil || *parent.UserID != user.ID {
				return nil, ErrAccessDenied
			}
		}
		fields["parentId"] = *in.ParentID
	}
	if err := s.folderRepo.Update(f.ID, fields); err != nil {
		return nil, err
	}
	return s.folderRepo.FindByID(f.ID)
}

// DeleteFolder removes an empty folder owned by user.
func (s *Service) DeleteFolder(user *model.User, id string) error {
	f, err := s.ShowFolder(user, id)
	if err != nil {
		return err
	}
	hasChildren, err := s.folderRepo.HasChildren(f.ID)
	if err != nil {
		return err
	}
	if hasChildren {
		return ErrFolderNotEmpty
	}
	return s.folderRepo.Delete(f)
}

// randReader is the source of randomness for newAccessKey. Tests override
// this to exercise the error path.
var randReader io.Reader = rand.Reader

// newAccessKey returns a random 32-character hex string used as the storage
// access key (and the URL path component for LocalStorage).
func newAccessKey() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(randReader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// mimeAllowedByPolicy reports whether mime matches any pattern in the
// `uploadableFileTypes` role policy. raw は GetUserPolicies map から取り出した
// any 値で、想定は `[]string` または `[]any` (JSON unmarshal 経由)。型不一致や
// 空 / nil は **deny** ではなく upstream 互換のため **allow** に倒す (= policy
// 未設定 / 未集約のときは制限を加えない fail-soft)。
//
// パターン:
//   - "*" / "*/*" → 全許可
//   - "image/*" など末尾 "/*" → prefix match (= "image/" で始まる)
//   - それ以外 → 完全一致
func mimeAllowedByPolicy(mime string, raw any) bool {
	patterns := normalizeMimePatterns(raw)
	// 取り出せなかった (= policy 未集約) ケースは fail-soft で許可。
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		switch {
		case p == "*" || p == "*/*":
			return true
		case strings.HasSuffix(p, "/*"):
			if strings.HasPrefix(mime, p[:len(p)-1]) {
				return true
			}
		default:
			if mime == p {
				return true
			}
		}
	}
	return false
}

// normalizeMimePatterns coerces the `uploadableFileTypes` policy value into
// a []string. Accepts []string (DefaultPolicies), []any (JSON unmarshal),
// or nil. 各 entry は trim され、空文字は除外する (upstream の `type.trim()
// === ” continue` と同 logic)。
func normalizeMimePatterns(raw any) []string {
	switch v := raw.(type) {
	case nil:
		return nil
	case []string:
		out := make([]string, 0, len(v))
		for _, s := range v {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}
