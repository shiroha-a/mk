package ld

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// ErrTooComplex is returned when a document would make URDNA2015
// canonicalization take an unbounded amount of CPU time.
var ErrTooComplex = errors.New("jsonld: document too complex to canonicalize")

const (
	// maxIdenticalBlankNodes bounds how many structurally identical blank
	// nodes a document may contain.
	//
	// URDNA2015 の `hashNDegreeQuads` は**非一意な** blank node の全順列を
	// 列挙する。json-gold にはその作業量を打ち切る機構が無い (upstream が
	// 使う rdf-canonize は `maxWorkFactor = 1` を既定で持つ)。同型の blank
	// node が n 個あると所要時間はおよそ 10 倍ずつ延び、実測では
	// n=6 で 31ms / n=7 で 203ms / n=8 で 2.0 秒だった。inbox の body 上限
	// (64KiB) まで n を増やせるので、上限を置かないと任意の実行時間を
	// 要求できる。
	//
	// **値が 1 つでも違えば非一意にならない** (第 1 段階ハッシュが隣接 quad の
	// 値まで見るため) ので、正当な activity がここに掛かることはまず無い。
	// 添付 16 件・絵文字タグ 128 件がすべて「同じ URL・同じ名前」である、
	// といった形でしか到達しない。
	maxIdenticalBlankNodes = 8

	// maxBlankNodes bounds the total number of blank nodes.
	//
	// 同型判定を抜ける形への保険。upstream の受け入れ上限
	// (添付 16 / 絵文字タグ 128 / プロフィール欄 16) の合計に余裕を持たせた値。
	maxBlankNodes = 512

	// maxComplexityDepth bounds how deep the estimator walks.
	// JSON 自体に循環は無いので、これは病的に深い入力に対する保険。
	maxComplexityDepth = 32
)

// CheckComplexity reports whether doc can be canonicalized in bounded time.
//
// 正規化の**前**に呼ぶこと。判定は入力の JSON 構造に対する近似で、
// URDNA2015 の非一意判定そのものではない。近似で足りるのは、爆発させるには
// 同型の blank node を多数並べる必要があるため (値を変えると一意になり、
// 順列の列挙が起きない)。
func CheckComplexity(doc any) error {
	c := &complexityCounter{groups: make(map[string]int)}
	c.walk(doc, 0)
	if c.total > maxBlankNodes {
		return fmt.Errorf("%w: %d blank nodes (limit %d)", ErrTooComplex, c.total, maxBlankNodes)
	}
	for key, n := range c.groups {
		if n > maxIdenticalBlankNodes {
			return fmt.Errorf("%w: %d identical blank nodes (limit %d, shape %s)",
				ErrTooComplex, n, maxIdenticalBlankNodes, key)
		}
	}
	return nil
}

type complexityCounter struct {
	total  int
	groups map[string]int
}

// walk returns a stable digest of the subtree rooted at v.
//
// `@id` を持つオブジェクトは blank node にならないので数えない。持たない
// ものだけを、部分木の形 (キーと値をすべて含む) でグループ化する。
func (c *complexityCounter) walk(v any, depth int) string {
	if depth > maxComplexityDepth {
		return "…"
	}
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		h := sha256.New()
		for _, k := range keys {
			h.Write([]byte(k))
			h.Write([]byte{0})
			h.Write([]byte(c.walk(t[k], depth+1)))
			h.Write([]byte{0})
		}
		digest := hex.EncodeToString(h.Sum(nil)[:8])
		if !hasIRIIdentity(t) {
			c.total++
			c.groups[digest]++
		}
		return digest
	case []any:
		h := sha256.New()
		for _, e := range t {
			h.Write([]byte(c.walk(e, depth+1)))
			h.Write([]byte{0})
		}
		return hex.EncodeToString(h.Sum(nil)[:8])
	case string:
		return "s:" + t
	case bool:
		return "b:" + strconv.FormatBool(t)
	case float64:
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(int64(t*1e6)))
		return "n:" + hex.EncodeToString(buf[:])
	case nil:
		return "z"
	default:
		return fmt.Sprintf("o:%T", v)
	}
}

// hasIRIIdentity reports whether the node carries an explicit identifier, in
// which case URDNA2015 labels it with that IRI instead of a blank node.
func hasIRIIdentity(m map[string]any) bool {
	for _, k := range []string{"@id", "id"} {
		if s, ok := m[k].(string); ok && s != "" {
			return true
		}
	}
	return false
}
