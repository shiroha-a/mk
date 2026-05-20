package frontendutil

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/labstack/echo/v4"
)

const frontendBase = "third_party/misskey"

// FrontendDir returns the path to built frontend assets.
// 環境変数 MISSKEY_FRONTEND_DIR で上書き可能。
func FrontendDir() string {
	if v := os.Getenv("MISSKEY_FRONTEND_DIR"); v != "" {
		return v
	}
	return filepath.Join(frontendBase, "built", "_frontend_vite_")
}

// FrontendDistDir returns the path to _frontend_dist_ assets (locales, fonts).
// 環境変数 MISSKEY_FRONTEND_DIST_DIR で上書き可能。
func FrontendDistDir() string {
	if v := os.Getenv("MISSKEY_FRONTEND_DIST_DIR"); v != "" {
		return v
	}
	return filepath.Join(frontendBase, "built", "_frontend_dist_")
}

// SwDistDir returns the path to _sw_dist_ assets (service worker sw.js).
// 環境変数 MISSKEY_SW_DIST_DIR で上書き可能。Frontend build の
// sibling ディレクトリに出力されるのでデフォルトは FrontendDir の親 +
// "_sw_dist_"。
func SwDistDir() string {
	if v := os.Getenv("MISSKEY_SW_DIST_DIR"); v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(FrontendDir()), "_sw_dist_")
}

// ClientAssetsDir returns the path to frontend client assets (game images, etc.).
// 環境変数 MISSKEY_CLIENT_ASSETS_DIR で上書き可能。
func ClientAssetsDir() string {
	if v := os.Getenv("MISSKEY_CLIENT_ASSETS_DIR"); v != "" {
		return v
	}
	return filepath.Join(frontendBase, "packages", "frontend", "assets")
}

// TwemojiDir returns the path to twemoji SVG files.
// 環境変数 MISSKEY_TWEMOJI_DIR で上書き可能。pnpm の hoisted node_modules は
// packages/backend 配下に配置されるため、そちらを参照する。
func TwemojiDir() string {
	if v := os.Getenv("MISSKEY_TWEMOJI_DIR"); v != "" {
		return v
	}
	return filepath.Join(frontendBase, "packages", "backend", "node_modules", "@discordapp", "twemoji", "dist", "svg")
}

// FluentEmojiDir returns the path to fluent-emoji PNG files. upstream Misskey
// TS では `node_modules/@misskey-dev/emoji-assets/built/fluent-emoji` から
// 配信されるが、本リポジトリでは submodule `third_party/misskey/fluent-emojis/
// dist` に同一の asset 集合が同梱されているのでこちらを参照する。frontend
// `ACHIEVEMENT_BADGES` (= 実績バッジ) や notification icon が `/fluent-emoji/
// <hex>.png` の URL で参照する。環境変数 `MISSKEY_FLUENT_EMOJI_DIR` で上書き可能。
func FluentEmojiDir() string {
	if v := os.Getenv("MISSKEY_FLUENT_EMOJI_DIR"); v != "" {
		return v
	}
	return filepath.Join(frontendBase, "fluent-emojis", "dist")
}

// StaticDir returns the path to static assets (icons, splash, favicon etc.).
// 環境変数 MISSKEY_STATIC_DIR で上書き可能。本家 TS では packages/backend/assets
// が /static-assets として配信される。
func StaticDir() string {
	if v := os.Getenv("MISSKEY_STATIC_DIR"); v != "" {
		return v
	}
	return filepath.Join(frontendBase, "packages", "backend", "assets")
}

// RepoAssetsDir returns the path to the top-level repository assets directory
// (ai.png, banner images, etc.).
// 環境変数 MISSKEY_REPO_ASSETS_DIR で上書き可能。
func RepoAssetsDir() string {
	if v := os.Getenv("MISSKEY_REPO_ASSETS_DIR"); v != "" {
		return v
	}
	return filepath.Join(frontendBase, "assets")
}

// ClientEntryInfo holds the Vite entry point script, CSS dependencies, and
// module preloads collected from the manifest by walking the entry's import chain.
type ClientEntryInfo struct {
	Script         string
	CSS            []string
	ModulePreloads []string
}

// DetectClientEntry reads the Vite manifest to find the entry script and all
// CSS files (= entry の直接 CSS + entry が import する全 chunk の CSS を再帰的に
// 集めたもの)。upstream TS の HtmlTemplateService#collectViteAssetFiles と同じ動作。
// Vite は SFC `<style scoped>` を chunk 単位の CSS file に切り出すため、entry が
// 同期 import する chunk の CSS も最初に link しないと、それらの component (例:
// MkCustomEmoji / MkMention) のスタイルが当たらない。ビルド済みアセットが存在し
// ない場合は空値を返す (dev mode)。
func DetectClientEntry() ClientEntryInfo {
	manifestPath := filepath.Join(FrontendDir(), "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return ClientEntryInfo{}
	}
	type manifestChunk struct {
		File    string   `json:"file"`
		IsEntry bool     `json:"isEntry"`
		CSS     []string `json:"css"`
		Imports []string `json:"imports"`
	}
	var manifest map[string]manifestChunk
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ClientEntryInfo{}
	}
	entry, ok := manifest["src/_boot_.ts"]
	if !ok {
		return ClientEntryInfo{}
	}

	seenChunks := map[string]struct{}{}
	seenCSS := map[string]struct{}{}
	var cssFiles []string
	var modulePreloads []string

	addCSS := func(files []string) {
		for _, f := range files {
			if _, dup := seenCSS[f]; dup {
				continue
			}
			seenCSS[f] = struct{}{}
			cssFiles = append(cssFiles, f)
		}
	}
	addCSS(entry.CSS)

	var walk func(imports []string, recursive bool)
	walk = func(imports []string, recursive bool) {
		for _, id := range imports {
			if _, dup := seenChunks[id]; dup {
				continue
			}
			seenChunks[id] = struct{}{}
			chunk, ok := manifest[id]
			if !ok {
				continue
			}
			addCSS(chunk.CSS)
			if len(chunk.Imports) > 0 {
				walk(chunk.Imports, true)
			}
			// modulePreloads は entry が同期 import する 1 hop の chunk だけ
			// (= recursive=false の呼び出し時のみ)。upstream と同じ挙動。
			if !recursive {
				modulePreloads = append(modulePreloads, chunk.File)
			}
		}
	}
	walk(entry.Imports, false)

	return ClientEntryInfo{
		Script:         entry.File,
		CSS:            cssFiles,
		ModulePreloads: modulePreloads,
	}
}

// AssetsHandler returns a handler that tries to serve files from primary dir
// first, then falls back to fallback dir. This avoids Echo's limitation of
// only supporting one handler per route pattern.
func AssetsHandler(primary, fallback string) echo.HandlerFunc {
	return func(c echo.Context) error {
		name := c.Param("*")
		// primaryディレクトリから探す
		fp := filepath.Join(primary, filepath.Clean("/"+name))
		if info, err := os.Stat(fp); err == nil && !info.IsDir() {
			return c.File(fp)
		}
		// fallbackディレクトリから探す
		fp = filepath.Join(fallback, filepath.Clean("/"+name))
		if info, err := os.Stat(fp); err == nil && !info.IsDir() {
			return c.File(fp)
		}
		return echo.ErrNotFound
	}
}
