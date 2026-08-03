package server

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/labstack/echo/v4"

	coredrive "github.com/shiroha-a/mk/internal/core/drive"
	"github.com/shiroha-a/mk/internal/model"
)

// filesDriveLookup is the minimal interface filesHandler needs to decide
// whether a request maps to a locally-stored drive_file row. Implemented
// by repository.DriveFileRepository; abstracted here so handler tests can
// stub without standing up a real DB.
type filesDriveLookup interface {
	FindByAnyAccessKey(accessKey string) (*model.DriveFile, error)
}

// filesHandler serves `GET /files/:accessKey`.
//
// `useObjectStorage=true` な instance では primary が S3Storage に切り替わるが、
// Misskey TS から S3 移行する前にアップロードされた drive_file 行は
// `storedInternal=true` のままローカル FS に残っており、TS upstream の
// FileServerService も storedInternal を見て storage を切り替える。同じ挙動に
// 合わせるため、DB を引いて storedInternal=true なら local を、それ以外
// (= 行が無い・ false / lookup 自体が unwired) は primary を使う (#1414)。
//
// lookup == nil もしくは DB error 時は primary に倒す。
//
// primary が現時点でローカルなら local と同じ FS を指すので storedInternal 判定は
// 無意味であり、ホットパスの DB クエリを省く。backend は admin 設定で動的に
// 切り替わる (#2315) ため、この判定は配線時ではなくリクエストごとに行う。
//
// MIME type はファイル内容の先頭から自動判定し、`http.ServeContent` で
// Range / If-Modified-Since / Content-Length 対応の正しい応答を返す。
//
// 旧実装は echo の `c.Stream` + `io.MultiReader` で chunked transfer に
// 倒していたが、Cloudflare Tunnel 経由のデプロイで bigger-than-buffer
// (33KB 程度) なファイルが truncate される問題があった (#730)。明示
// Content-Length + Cache-Control: no-transform でこれを回避する。
func filesHandler(lookup filesDriveLookup, primary, local coredrive.Storage) echo.HandlerFunc {
	return func(c echo.Context) error {
		key := c.Param("accessKey")
		storage := primary
		if lookup != nil && local != nil && !coredrive.StorageIsLocal(primary) {
			if f, err := lookup.FindByAnyAccessKey(key); err == nil && f.StoredInternal {
				storage = local
			}
		}
		body, err := storage.Get(key)
		if err != nil {
			return c.NoContent(http.StatusNotFound)
		}
		defer body.Close()

		// LocalStorage は *os.File を返すので io.ReadSeeker として扱える。
		// 互換性のため type assertion で seekable かを確認し、非対応 (将来
		// S3 等の non-seekable storage を増やした場合) なら全 body を memory
		// に読み込む fallback にフォールバックする。
		// modtime も *os.File なら Stat() から取得して If-Modified-Since の
		// 304 経路を有効にする。それ以外は零値で http.ServeContent が
		// Last-Modified を出さない (Cache-Control: immutable で代替)。
		var modtime time.Time
		seeker, ok := body.(io.ReadSeeker)
		if ok {
			if f, isFile := body.(*os.File); isFile {
				if st, err := f.Stat(); err == nil {
					modtime = st.ModTime()
				}
			}
		} else {
			data, rerr := io.ReadAll(body)
			if rerr != nil {
				return c.NoContent(http.StatusInternalServerError)
			}
			seeker = bytes.NewReader(data)
		}

		buf := make([]byte, coredrive.MIMESniffLen)
		n, _ := seeker.Read(buf)
		contentType := coredrive.DetectMIME(buf[:n])
		// #2106 H3: sniff した MIME を browser-safe allowlist に通し、非該当
		// (text/html, image/svg+xml, text/xml 等) は application/octet-stream に
		// 矯正する。これを欠くと任意アップロードが file origin から active content
		// として実行され stored XSS になる (upstream FileServerUtils.getSafeContentType)。
		contentType = coredrive.BrowserSafeContentType(contentType)
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return c.NoContent(http.StatusInternalServerError)
		}

		// `Cache-Control: no-transform` で Cloudflare Polish 等 CDN 中間層
		// による画像再エンコードを抑止する (#730)。Polish は animated WebP
		// を別 size で出力するため UDS デプロイ環境で「絵文字がチカチカ
		// する + サイズが切れる」現象を起こす。`immutable` で長期 cache 化、
		// `no-transform` で binary 1:1 配信を契約する。
		c.Response().Header().Set("Cache-Control", "max-age=31536000, immutable, no-transform")
		// #2106 H3: upstream FileServerService と同じ防御 header。CSP で file origin
		// からの script/object 実行を封じ、nosniff で MIME sniffing を止め、
		// Content-Disposition: inline で download 名を制御する (octet-stream と多層防御)。
		c.Response().Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'")
		c.Response().Header().Set("X-Content-Type-Options", "nosniff")
		c.Response().Header().Set("Content-Disposition", "inline")
		c.Response().Header().Set(echo.HeaderContentType, contentType)
		http.ServeContent(c.Response(), c.Request(), key, modtime, seeker)
		return nil
	}
}
