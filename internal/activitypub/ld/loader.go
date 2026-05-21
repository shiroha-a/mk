// Package ld provides a JSON-LD pipeline used by LD-Signature verification
// (#1164 Phase D)。`piprate/json-gold` を base に、Misskey TS の JsonLdService
// と互換な compact + URDNA2015 normalize + RsaSignature2017 verify を提供する。
//
// upstream Misskey TS `core/activitypub/JsonLdService.ts` と byte-for-byte に
// canonicalize 結果を一致させるため、`PRELOADED_CONTEXTS` (= AS 2.0 / W3C
// Security v1 / W3C Identity v1) を TypeScript 由来の JSON file としてそのまま
// embed する。これにより HTTP fetch 経由で context document を取得する経路を
// 排除し (DoS amplification + remote 書き換え race の防止)、verify の
// reproducibility を確保する。
package ld

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/piprate/json-gold/ld"
)

//go:embed contexts/identity_v1.json contexts/security_v1.json contexts/activitystreams.json
var contextFS embed.FS

// preloadedContextSources maps the canonical IRI of each preloaded JSON-LD
// context document to its embedded JSON file. The IRIs must match the keys in
// upstream Misskey TS `PRELOADED_CONTEXTS` (= `contexts.ts:573-577`) so that
// canonicalize 結果が TS と一致する。
var preloadedContextSources = map[string]string{
	"https://w3id.org/identity/v1":          "contexts/identity_v1.json",
	"https://w3id.org/security/v1":          "contexts/security_v1.json",
	"https://www.w3.org/ns/activitystreams": "contexts/activitystreams.json",
}

// PreloadedLoader is a JSON-LD DocumentLoader that returns the embedded
// context documents only. Any URL not in the preload set returns an error so
// that LD-Signature verify never silently fetches remote documents (= upstream
// 2026.5.4 hardening freeze() の事前段階に相当)。
//
// 必要に応じて caller が cache miss 時の fallback document loader を inject
// する経路を将来追加する余地はあるが、本 PR では preload only に絞る。
type PreloadedLoader struct {
	once sync.Once
	docs map[string]*ld.RemoteDocument
	err  error
}

// NewPreloadedLoader constructs a DocumentLoader that serves the embedded
// preload context set and refuses any other URL.
func NewPreloadedLoader() *PreloadedLoader {
	return &PreloadedLoader{}
}

func (l *PreloadedLoader) load() error {
	l.once.Do(func() {
		docs := make(map[string]*ld.RemoteDocument, len(preloadedContextSources))
		for iri, path := range preloadedContextSources {
			raw, err := contextFS.ReadFile(path)
			if err != nil {
				l.err = fmt.Errorf("read embedded context %q: %w", path, err)
				return
			}
			var parsed any
			if err := json.Unmarshal(raw, &parsed); err != nil {
				l.err = fmt.Errorf("parse embedded context %q: %w", path, err)
				return
			}
			docs[iri] = &ld.RemoteDocument{
				DocumentURL: iri,
				Document:    parsed,
				ContextURL:  "",
			}
		}
		l.docs = docs
	})
	return l.err
}

// LoadDocument implements the ld.DocumentLoader interface. Returns
// ErrContextNotPreloaded for any URL outside the preload set so caller can
// gate / log / surface the gap.
func (l *PreloadedLoader) LoadDocument(u string) (*ld.RemoteDocument, error) {
	if err := l.load(); err != nil {
		return nil, err
	}
	doc, ok := l.docs[u]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrContextNotPreloaded, u)
	}
	return doc, nil
}

// ErrContextNotPreloaded is returned by PreloadedLoader.LoadDocument when the
// requested URL is not in the embedded preload set. Callers can gate on this
// sentinel to fail-closed verify path (= upstream 2026.5.4 freeze() の事前
// 防衛と等価)。
var ErrContextNotPreloaded = errors.New("jsonld: context not in preloaded set")
