// Package emojiimport implements the Misskey-compatible "admin/emoji/
// import-zip" flow: read a previously-uploaded ZIP from Drive, extract each
// emoji image + meta.json, and register them as local custom emojis.
package emojiimport

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"regexp"
	"time"

	"github.com/shiroha-a/mk/internal/core/drive"
	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// Errors returned by Importer.
var (
	// ErrDriveFileNotFound is returned when the referenced fileId cannot be
	// loaded from Drive.
	ErrDriveFileNotFound = errors.New("drive file not found")
	// ErrUserNotFound is returned when the requesting admin user is absent.
	ErrUserNotFound = errors.New("user not found")
	// ErrInvalidZip is returned when the body cannot be parsed as a ZIP.
	ErrInvalidZip = errors.New("invalid zip archive")
	// ErrMissingMeta is returned when meta.json is not present in the archive.
	ErrMissingMeta = errors.New("meta.json missing in archive")
	// ErrMalformedMeta is returned when meta.json cannot be parsed as JSON.
	ErrMalformedMeta = errors.New("meta.json malformed")
)

// DriveReader fetches a previously-uploaded DriveFile by id and returns its
// raw bytes. 本家 Misskey の DownloadService.downloadUrl 相当。importer は file
// の URL を解決せず byte body だけを必要とする。
type DriveReader interface {
	Fetch(fileID string) (*model.DriveFile, []byte, error)
}

// Deps bundles the dependencies needed by Importer.
type Deps struct {
	UserRepo  repository.UserRepository
	EmojiRepo repository.EmojiRepository
	Drive     DriveReader
	Uploader  *drive.Service
	IDGen     id.Generator
}

// Importer runs the emoji ZIP import process.
type Importer struct {
	deps Deps
	now  func() time.Time
}

// NewImporter constructs an Importer.
func NewImporter(deps Deps) *Importer {
	return &Importer{deps: deps, now: time.Now}
}

// SetNow overrides the clock. Tests only.
func (i *Importer) SetNow(fn func() time.Time) {
	if fn != nil {
		i.now = fn
	}
}

// Result captures per-batch counters surfaced to the caller.
type Result struct {
	Total    int
	Imported int
	Skipped  int
}

// metaDoc mirrors the `meta.json` produced by Misskey's custom-emoji
// export. 本家 ExportCustomEmojisProcessorService が出力する構造を参照した。
// emoji サブオブジェクトの全フィールドを保持するが、互換性のため欠けていても
// 許容する (ゼロ値で扱う)。
type metaDoc struct {
	MetaVersion int          `json:"metaVersion"`
	Host        *string      `json:"host"`
	ExportedAt  string       `json:"exportedAt"`
	Emojis      []metaRecord `json:"emojis"`
}

type metaRecord struct {
	FileName   string    `json:"fileName"`
	Downloaded bool      `json:"downloaded"`
	Emoji      metaEmoji `json:"emoji"`
}

type metaEmoji struct {
	Name        string   `json:"name"`
	Category    *string  `json:"category"`
	Aliases     []string `json:"aliases"`
	License     *string  `json:"license"`
	IsSensitive bool     `json:"isSensitive"`
	LocalOnly   bool     `json:"localOnly"`
}

// validFileName matches Misskey's ImportCustomEmojisProcessorService regex.
// 「`[a-zA-Z0-9_]+?([a-zA-Z0-9\.]+)?`」相当。
var validFileName = regexp.MustCompile(`^[a-zA-Z0-9_]+?([a-zA-Z0-9.]+)?$`)

// validEmojiName matches Misskey's validation for emoji names.
var validEmojiName = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// Run executes the import for the given user/file. Per-item errors are logged
// and counted as Skipped so that a single malformed entry does not abort the
// batch. 本家と同様「meta.json が壊れていれば job 全体失敗、個別エントリは
// スキップ」の方針。
func (i *Importer) Run(ctx context.Context, userID, fileID string) (*Result, error) {
	if i.deps.Drive == nil || i.deps.Uploader == nil || i.deps.EmojiRepo == nil || i.deps.UserRepo == nil || i.deps.IDGen == nil {
		return nil, errors.New("emoji importer not fully configured")
	}
	user, err := i.deps.UserRepo.FindByID(userID)
	if err != nil || user == nil {
		return nil, fmt.Errorf("%w: %s", ErrUserNotFound, userID)
	}

	_, body, err := i.deps.Drive.Fetch(fileID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDriveFileNotFound, err)
	}

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidZip, err)
	}

	// fileName → *zip.File の索引を作っておくと、meta.json の record.fileName から
	// ZIP 内のエントリを O(1) で引ける。
	index := make(map[string]*zip.File, len(zr.File))
	var metaFile *zip.File
	for _, f := range zr.File {
		// ディレクトリエントリ (末尾 `/`) は対象外。`path.Base("meta.json/")` が
		// `meta.json` になるため、ここで弾かないと空のディレクトリを meta.json
		// として採用して ErrMalformedMeta になる。
		if f.FileInfo().IsDir() {
			continue
		}
		base := path.Base(f.Name)
		if base == "meta.json" {
			metaFile = f
			continue
		}
		index[base] = f
	}
	if metaFile == nil {
		return nil, ErrMissingMeta
	}

	metaBytes, err := readZipEntry(metaFile)
	if err != nil {
		return nil, fmt.Errorf("read meta.json: %w", err)
	}
	var meta metaDoc
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedMeta, err)
	}

	result := &Result{Total: len(meta.Emojis)}
	for _, record := range meta.Emojis {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !record.Downloaded {
			result.Skipped++
			continue
		}
		if !validFileName.MatchString(record.FileName) {
			slog.Warn("emoji import: invalid filename", "filename", record.FileName)
			result.Skipped++
			continue
		}
		if !validEmojiName.MatchString(record.Emoji.Name) {
			slog.Warn("emoji import: invalid emoji name", "name", record.Emoji.Name)
			result.Skipped++
			continue
		}

		entry := index[record.FileName]
		if entry == nil {
			slog.Warn("emoji import: missing zip entry", "filename", record.FileName)
			result.Skipped++
			continue
		}
		imgBody, err := readZipEntry(entry)
		if err != nil {
			slog.Warn("emoji import: read entry failed", "filename", record.FileName, "err", err)
			result.Skipped++
			continue
		}

		if err := i.replaceEmoji(ctx, record, imgBody); err != nil {
			slog.Warn("emoji import: replace failed", "name", record.Emoji.Name, "err", err)
			result.Skipped++
			continue
		}
		result.Imported++
	}
	return result, nil
}

// readZipEntry reads an entry body fully into memory. 絵文字画像は高々
// 数百KB なので全量展開で問題ない。
func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

// replaceEmoji deletes any existing local emoji with the same name, uploads the
// image to Drive as a new DriveFile, and creates a fresh emoji row. Run still
// resolves the requesting user up front to fail-fast on missing accounts, but
// the user is *not* propagated here because the resulting drive file is
// system-owned (see Upload comment below for #670 rationale).
func (i *Importer) replaceEmoji(ctx context.Context, record metaRecord, body []byte) error {
	// 既存 local 絵文字 (同名) は上書きのために先に削除する。
	// 本家 Misskey では emojisRepository.delete({ name, host: IsNull() }) 相当。
	if existing, err := i.deps.EmojiRepo.FindByNameAndHost(record.Emoji.Name, nil); err == nil && existing != nil {
		_ = i.deps.EmojiRepo.Delete(existing.ID)
	}

	// emoji import zip で展開された画像は upstream Misskey TS と同じく
	// system 所有 drive file (User: nil) として保存する (#670)。custom emoji
	// はインスタンス管理アセットであり、import を実行した admin 個人の drive
	// に紐付けると ロール変更 / アカウント削除 で巻き込まれて表示が壊れる。
	// User: nil の経路では drive.Service.Upload が md5 dedup を skip するため
	// Force: true は実質 no-op だが、明示性のため残す。
	uploaded, err := i.deps.Uploader.Upload(ctx, drive.UploadInput{
		Body:  body,
		Name:  record.FileName,
		Force: true,
	})
	if err != nil {
		return fmt.Errorf("upload emoji image: %w", err)
	}

	publicURL := uploaded.URL
	if uploaded.WebpublicURL != nil && *uploaded.WebpublicURL != "" {
		publicURL = *uploaded.WebpublicURL
	}
	fileType := uploaded.Type
	if uploaded.WebpublicType != nil && *uploaded.WebpublicType != "" {
		fileType = *uploaded.WebpublicType
	}

	now := i.now()
	aliases := model.StringArray(append([]string(nil), record.Emoji.Aliases...))
	// 不変条件 (#722): emoji.originalUrl は drive_file.url と一致させる。
	// `DriveFileRepository.DeleteOrphans` の cleanup guard は
	// `NOT EXISTS (emoji.originalUrl = drive_file.url ...)` で system 所有
	// emoji 画像を保護しているので、別 URL (webpublic 等) を originalUrl に
	// 入れると cleanup で消える。
	emoji := &model.Emoji{
		ID:          i.deps.IDGen.Generate(now),
		UpdatedAt:   &now,
		Name:        record.Emoji.Name,
		Host:        nil,
		Category:    record.Emoji.Category,
		OriginalURL: uploaded.URL,
		PublicURL:   publicURL,
		Type:        &fileType,
		Aliases:     aliases,
		License:     record.Emoji.License,
		IsSensitive: record.Emoji.IsSensitive,
		LocalOnly:   record.Emoji.LocalOnly,
	}
	if err := i.deps.EmojiRepo.Create(emoji); err != nil {
		return fmt.Errorf("create emoji row: %w", err)
	}
	return nil
}
