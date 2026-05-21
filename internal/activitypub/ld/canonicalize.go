package ld

import (
	"fmt"

	"github.com/piprate/json-gold/ld"
)

// Processor は piprate/json-gold の JsonLdProcessor + PreloadedLoader を
// 内包した薄い wrapper。compact / normalize / signature helper の上層から
// 呼び出して LD-Signature verify を組み立てる。
//
// upstream Misskey TS `JsonLdService` 互換の挙動を提供することが目的:
//   - context fetch は PreloadedLoader 経由 (HTTP fetch 禁止)
//   - normalize algorithm は URDNA2015 (RFC8785 ベース、JSON-LD 1.1 spec)
type Processor struct {
	jp     *ld.JsonLdProcessor
	loader *PreloadedLoader
}

// NewProcessor returns a Processor wired to the preload-only DocumentLoader.
func NewProcessor() *Processor {
	return &Processor{
		jp:     ld.NewJsonLdProcessor(),
		loader: NewPreloadedLoader(),
	}
}

// Compact applies JSON-LD `compact` algorithm against the supplied context.
// 呼び出し側は inbound activity の `@context` を読み込んで、それを context
// 引数として渡す。LD-Signature verify では verify 前に compact を走らせて
// 異なる context shape を canonical 化する。
func (p *Processor) Compact(doc any, context any) (map[string]any, error) {
	opts := ld.NewJsonLdOptions("")
	opts.DocumentLoader = p.loader
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
	opts.DocumentLoader = p.loader
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
