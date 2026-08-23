package ld

import (
	"fmt"
	"sync"

	"github.com/piprate/json-gold/ld"
)

// Processor は piprate/json-gold の JsonLdProcessor + per-verify state を
// 内包した薄い wrapper。compact / normalize / verify を 1 つの processor
// instance 経由で行い、cache cap / freeze (= 2026.5.4 hardening) を per-verify
// で適用する。
//
// upstream Misskey TS `JsonLdService` 互換:
//   - 1 verify あたり 1 Processor instance を作る (cache は activity 単位で reset)
//   - context fetch は PreloadedLoader 経由 (= HTTP fetch なし、現状)
//   - cache 256 entry 上限で overflow → ErrCacheOverflow
//   - Freeze() 以降は ErrCacheFrozen
//   - normalize は URDNA2015 (JSON-LD 1.1 spec)
//
// caller が外から DocumentLoader を inject する経路は今は持たない (= preload
// only 設計、将来 HTTP fetch fallback を入れるなら本 struct に追加)。
type Processor struct {
	jp      *ld.JsonLdProcessor
	preload *PreloadedLoader

	// per-verify state (= upstream JsonLd class instance state を Go 翻訳)。
	mu       sync.Mutex
	cache    map[string]*ld.RemoteDocument
	frozen   bool
	cacheCap int
}

// NewProcessor returns a fresh Processor with default 256 cache cap.
// Inbox 側で 1 activity verify ごとに新規 instance を作るのが推奨運用 (=
// upstream class instance と等価)。
func NewProcessor() *Processor {
	return &Processor{
		jp:       ld.NewJsonLdProcessor(),
		preload:  NewPreloadedLoader(),
		cache:    make(map[string]*ld.RemoteDocument),
		cacheCap: defaultCacheCap,
	}
}

// LoadDocument implements ld.DocumentLoader so Processor itself can be passed
// to JsonLdProcessor.Compact / Normalize. 内部実装は loadDocument に委譲。
func (p *Processor) LoadDocument(u string) (*ld.RemoteDocument, error) {
	return p.loadDocument(u)
}

// loadDocument is the DocumentLoader callback the Processor uses internally.
// Implements:
//   - cache hit → 即返す (HTTP 経路無し)
//   - cache miss + frozen + preload 外 → ErrCacheFrozen
//   - cache miss → PreloadedLoader に委譲、結果を cache に格納
//   - cache size > cacheCap → ErrCacheOverflow
//
// **freeze は preload 済み context を塞がない。** upstream も
// `PRELOADED_CONTEXTS` を frozen 判定より先に見る (JsonLdService.ts getLoader)。
// freeze が塞ぎたいのは remote fetch (SSRF / TOCTOU) で、go:embed 済みの
// context はネットワークに出ないため塞ぐ理由が無い。ここを塞ぐと、verify が
// 後から差し込む `@context: w3id.org/identity/v1` (verify.go) を解決できず、
// **LD-Signature の検証が常に失敗する** (#2680)。verifier は空 cache のまま
// Freeze してから verify するので、cache 経由で救われることもない。
//
// 戻り値 RemoteDocument の DocumentURL / Document は piprate/json-gold が
// JsonLdProcessor の compact / normalize で参照する。
func (p *Processor) loadDocument(u string) (*ld.RemoteDocument, error) {
	p.mu.Lock()
	if d, ok := p.cache[u]; ok {
		p.mu.Unlock()
		return d, nil
	}
	if p.frozen && !isPreloadedContext(u) {
		p.mu.Unlock()
		return nil, ErrCacheFrozen
	}
	p.mu.Unlock()

	doc, err := p.preload.LoadDocument(u)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	// 同時に同じ URL を fetch して 1 つ目が cache 登録した後 race するケースも
	// 想定し、再 check で先勝ち優先 (= upstream Map.set + size check と等価
	// な挙動を deterministic に揃える)。
	if existing, ok := p.cache[u]; ok {
		return existing, nil
	}
	p.cache[u] = doc
	if len(p.cache) > p.cacheCap {
		return nil, fmt.Errorf("%w: size=%d", ErrCacheOverflow, len(p.cache))
	}
	return doc, nil
}

// Compact applies JSON-LD `compact` algorithm against the supplied context.
// 呼び出し側は inbound activity の `@context` を読み込んで、それを context
// 引数として渡す。LD-Signature verify では verify 前に compact を走らせて
// 異なる context shape を canonical 化する。
func (p *Processor) Compact(doc any, context any) (map[string]any, error) {
	opts := ld.NewJsonLdOptions("")
	opts.DocumentLoader = p
	out, err := p.jp.Compact(doc, context, opts)
	if err != nil {
		return nil, fmt.Errorf("jsonld compact: %w", err)
	}
	return out, nil
}

// Normalize applies URDNA2015 canonicalization and returns the n-quads
// string. RsaSignature2017 verify (= 2017 仕様) で document hash の入力に使う。
//
// json-gold の RDF dataset normalize は URDNA2015 のみで、URGNA2012 等の
// legacy algorithm は提供しない (= 受信できる activity の signature
// algorithm は normalizationAlgorithm = URDNA2015 のみ accept)。
func (p *Processor) Normalize(doc any) (string, error) {
	opts := ld.NewJsonLdOptions("")
	opts.DocumentLoader = p
	opts.Algorithm = ld.AlgorithmURDNA2015
	opts.Format = "application/n-quads"
	out, err := p.jp.Normalize(doc, opts)
	if err != nil {
		return "", fmt.Errorf("jsonld normalize: %w", err)
	}
	s, ok := out.(string)
	if !ok {
		return "", fmt.Errorf("jsonld normalize: expected string n-quads, got %T", out)
	}
	return s, nil
}
