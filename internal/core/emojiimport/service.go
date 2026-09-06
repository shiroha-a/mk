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

// maxImportNameLength は meta.json 中の名前の長さ上限。upstream
// ImportCustomEmojisProcessorService の `MAX_NAME_LENGTH` と同じ。
// これが無いと、DB の列長 (drive_file.name varchar(256) / emoji.name varchar(128))
// で落ちるまでに画像処理と storage 書き込みが 1 件ぶん無駄に走る。
const maxImportNameLength = 255

// validFileName matches Misskey's ImportCustomEmojisProcessorService
// `FILE_NAME_PATTERN` (2026.9.0 で厳格化された)。旧 upstream の
// `^[a-zA-Z0-9_]+?([a-zA-Z0-9.]+)?$` は `a..png` や `a.` を通していた。
var validFileName = regexp.MustCompile(`^[a-zA-Z0-9_]+(\.[a-zA-Z0-9]+)*$`)

// validEmojiName matches Misskey's `EMOJI_NAME_PATTERN`.
var validEmojiName = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// validImportName は upstream の isValidName 相当 (長さ + パターン)。
func validImportName(v string, pattern *regexp.Regexp) bool {
	return len(v) <= maxImportNameLength && pattern.MatchString(v)
}

// Run executes the import for the given user/file. Per-item errors are logged
// and counted as Skipped so that a single malformed entry does not abort the
// batch. 本家と同様「meta.json が壊れていれば job 全体失敗、個別エントリは
// スキップ」の方針。
func (i *Importer) Run(ctx context.Context, userID, fileID string) (*Result, error) {
	if i.deps.Drive == nil || i.deps.Uploader == nil || i.deps.EmojiRepo == nil || i.deps.UserRepo == nil || i.deps.IDGen == nil {
		return nil, errors.New("emoji importer not fully configured")
	}
	user, err := i.deps.UserRepo.FindByID(userID)
	// **DB 障害を not-found に丸めない** (#2799)。`emoji_import` processor は
	// `ErrUserNotFound` で `SkipRetry` を付けるので、丸めると**瞬断中に走った
	// ジョブが retry されず恒久破棄される**。
	if err != nil && !repository.IsNotFound(err) {
		return nil, err
	}
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

	metaBytes, err := readZipEntry(metaFile, maxMetaJSONBytes)
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
		if !validImportName(record.FileName, validFileName) {
			slog.Warn("emoji import: invalid filename", "filename", record.FileName)
			result.Skipped++
			continue
		}
		if !validImportName(record.Emoji.Name, validEmojiName) {
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
		imgBody, err := readZipEntry(entry, maxEmojiImageBytes)
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

// Uncompressed size caps for zip entries, matching upstream Misskey's
// `src/misc/zip.ts` (2026.9.0).
//
// **var にしてあるのはテストから下げるため。** const のままだと、上限が
// 呼び出し側に配線されていることを検証するのに 64MiB の fixture が要る。
var (
	maxMetaJSONBytes   int64 = 64 << 20 // 64MiB
	maxEmojiImageBytes int64 = 32 << 20 // 32MiB
)

// ErrZipEntryTooLarge is returned when a zip entry's uncompressed size exceeds
// the cap for its kind.
var ErrZipEntryTooLarge = errors.New("zip entry exceeds the uncompressed size limit")

// readZipEntry reads an entry body into memory, refusing anything whose
// uncompressed size exceeds maxBytes.
//
// **ヘッダ値と実際に読んだバイト数の両方で見る。** 旧実装は `io.ReadAll` で
// 上限なしに読んでおり、コメントも「絵文字画像は高々数百KB なので全量展開で
// 問題ない」としていたが、**中身を作るのは攻撃者**という前提が抜けていた。
// deflate のゼロ埋めは 1000:1 を超える圧縮率が出るので、Drive の上限に収まる
// ZIP から巨大な単一 allocation を起こせる。
//
// ヘッダ (`UncompressedSize64`) が主。実バイト数の打ち切りは**保険**で、
// Go の `archive/zip` は宣言サイズと実データの不一致を読み出しの時点で
// `zip: not a valid zip file` として弾くので、現状ここには到達しない
// (実測: サイズ欄を偽装した zip は 0 バイトも読めずに失敗する)。
// stdlib の挙動に依存しない形にしておくために両方置く。upstream の zip.ts も
// ヘッダ値と実書き出しバイト数の 2 段構えにしている。
func readZipEntry(f *zip.File, maxBytes int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(maxBytes) {
		return nil, ErrZipEntryTooLarge
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrZipEntryTooLarge
	}
	return data, nil
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
