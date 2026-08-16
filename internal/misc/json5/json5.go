// Package json5 converts JSON5 documents into plain JSON.
//
// **Misskey のテーマは JSON5 で配布される。** キーが無引用符で文字列が
// シングルクォート、コメント付きのものもある。本家の MetaEntityService.pack は
// これを JSON に直してからクライアントへ返しており (「クライアントの手間を
// 減らすためあらかじめJSONに変換しておく」)、mk-go も同じ形にする必要がある
// (#2583)。
//
// 目的は変換だけなので、値は Go の型に落とさず**そのまま JSON テキストとして
// 書き出す**。数値は表記を保つ必要がある一方 JSON5 固有の書き方 (先頭の `.`、
// 16 進、`+`) は JSON に無いので、そこだけ正規化する。
//
// 実装していない構文に当たったら error を返す。呼び出し側は本家の catch と
// 同じく「テーマ無し」に倒すので、**黙って一部を捨てるより落ちる方が安全**。
package json5

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrSyntax is returned for input this package cannot convert.
var ErrSyntax = errors.New("json5: invalid document")

// ToJSON converts a JSON5 document into equivalent JSON.
//
// 既に JSON なものはそのまま通る (JSON は JSON5 の部分集合)。
func ToJSON(src string) (string, error) {
	p := &parser{src: src}
	p.skipSpace()
	var out strings.Builder
	if err := p.value(&out); err != nil {
		return "", err
	}
	p.skipSpace()
	if p.pos != len(p.src) {
		return "", fmt.Errorf("%w: trailing data at %d", ErrSyntax, p.pos)
	}
	return out.String(), nil
}

type parser struct {
	src string
	pos int
}

func (p *parser) errf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format+" at %d", append(append([]any{ErrSyntax}, args...), p.pos)...)
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) peek() byte {
	if p.eof() {
		return 0
	}
	return p.src[p.pos]
}

// skipSpace consumes whitespace and both comment forms.
//
// **コメントを飛ばすのは空白と同じ扱いにする。** 同梱テーマ (`_dark.json5` 等)
// が行コメントを持っているので、値の前後どこにでも現れうる。
func (p *parser) skipSpace() {
	for !p.eof() {
		c := p.src[p.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f':
			p.pos++
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '/':
			for !p.eof() && p.src[p.pos] != '\n' {
				p.pos++
			}
		case c == '/' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '*':
			p.pos += 2
			for !p.eof() && !strings.HasPrefix(p.src[p.pos:], "*/") {
				p.pos++
			}
			if !p.eof() {
				p.pos += 2
			}
		case c >= utf8.RuneSelf:
			// U+00A0 / U+FEFF 等の unicode 空白。識別子の先頭にもなりうるので
			// 空白と判定できたときだけ進める。
			r, size := utf8.DecodeRuneInString(p.src[p.pos:])
			if !unicode.IsSpace(r) && r != 0xFEFF {
				return
			}
			p.pos += size
		default:
			return
		}
	}
}

func (p *parser) value(out *strings.Builder) error {
	if p.eof() {
		return p.errf("unexpected end of input")
	}
	switch c := p.peek(); {
	case c == '{':
		return p.object(out)
	case c == '[':
		return p.array(out)
	case c == '"' || c == '\'':
		s, err := p.stringLiteral()
		if err != nil {
			return err
		}
		out.WriteString(encodeJSONString(s))
		return nil
	default:
		return p.literal(out)
	}
}

func (p *parser) object(out *strings.Builder) error {
	p.pos++ // '{'
	out.WriteByte('{')
	first := true
	for {
		p.skipSpace()
		if p.eof() {
			return p.errf("unterminated object")
		}
		if p.peek() == '}' {
			p.pos++
			out.WriteByte('}')
			return nil
		}
		if !first {
			out.WriteByte(',')
		}
		first = false

		key, err := p.objectKey()
		if err != nil {
			return err
		}
		out.WriteString(encodeJSONString(key))
		p.skipSpace()
		if p.peek() != ':' {
			return p.errf("expected ':'")
		}
		p.pos++
		out.WriteByte(':')
		p.skipSpace()
		if err := p.value(out); err != nil {
			return err
		}
		p.skipSpace()
		// 末尾カンマは許す (次の周回で '}' を見る)。
		if p.peek() == ',' {
			p.pos++
			continue
		}
		if p.peek() != '}' {
			return p.errf("expected ',' or '}'")
		}
	}
}

func (p *parser) array(out *strings.Builder) error {
	p.pos++ // '['
	out.WriteByte('[')
	first := true
	for {
		p.skipSpace()
		if p.eof() {
			return p.errf("unterminated array")
		}
		if p.peek() == ']' {
			p.pos++
			out.WriteByte(']')
			return nil
		}
		if !first {
			out.WriteByte(',')
		}
		first = false

		if err := p.value(out); err != nil {
			return err
		}
		p.skipSpace()
		if p.peek() == ',' {
			p.pos++
			continue
		}
		if p.peek() != ']' {
			return p.errf("expected ',' or ']'")
		}
	}
}

// objectKey reads a quoted or bare key.
func (p *parser) objectKey() (string, error) {
	if c := p.peek(); c == '"' || c == '\'' {
		return p.stringLiteral()
	}
	start := p.pos
	for !p.eof() {
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		if !isIdentRune(r, p.pos == start) {
			break
		}
		p.pos += size
	}
	if p.pos == start {
		return "", p.errf("expected object key")
	}
	return p.src[start:p.pos], nil
}

// isIdentRune reports whether r may appear in a bare object key.
//
// JSON5 は ECMAScript の IdentifierName を許す。`$` と `_` と unicode 文字、
// 2 文字目以降は数字も。**厳密な ID_Start / ID_Continue には寄せない** —
// ここで落とすと安全側 (null) に倒れるだけで、通しすぎるより害が小さい。
func isIdentRune(r rune, first bool) bool {
	if r == '$' || r == '_' {
		return true
	}
	if unicode.IsLetter(r) {
		return true
	}
	if !first && (unicode.IsDigit(r) || unicode.IsMark(r)) {
		return true
	}
	return false
}

// stringLiteral reads a single- or double-quoted JSON5 string.
func (p *parser) stringLiteral() (string, error) {
	quote := p.src[p.pos]
	p.pos++
	var sb strings.Builder
	for {
		if p.eof() {
			return "", p.errf("unterminated string")
		}
		c := p.src[p.pos]
		switch {
		case c == quote:
			p.pos++
			return sb.String(), nil
		case c == '\\':
			p.pos++
			if err := p.escape(&sb); err != nil {
				return "", err
			}
		case c == '\n' || c == '\r':
			// 生の改行は不可 (継続したいなら `\` が要る)。
			return "", p.errf("unescaped newline in string")
		default:
			sb.WriteByte(c)
			p.pos++
		}
	}
}

func (p *parser) escape(sb *strings.Builder) error {
	if p.eof() {
		return p.errf("unterminated escape")
	}
	c := p.src[p.pos]
	p.pos++
	switch c {
	case '"', '\'', '\\', '/':
		sb.WriteByte(c)
	case 'b':
		sb.WriteByte('\b')
	case 'f':
		sb.WriteByte('\f')
	case 'n':
		sb.WriteByte('\n')
	case 'r':
		sb.WriteByte('\r')
	case 't':
		sb.WriteByte('\t')
	case 'v':
		sb.WriteByte('\v')
	case '0':
		// `\0` は NUL。後ろに数字が続く 8 進エスケープは JSON5 でも不可。
		if !p.eof() && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			return p.errf("octal escape")
		}
		sb.WriteByte(0)
	case 'x':
		return p.hexEscape(sb, 2)
	case 'u':
		return p.hexEscape(sb, 4)
	case '\n':
		// 行継続。何も足さない。
	case '\r':
		if !p.eof() && p.src[p.pos] == '\n' {
			p.pos++
		}
	default:
		// JSON5 は未知のエスケープを「その文字そのもの」にする。
		p.pos--
		r, size := utf8.DecodeRuneInString(p.src[p.pos:])
		sb.WriteRune(r)
		p.pos += size
	}
	return nil
}

func (p *parser) hexEscape(sb *strings.Builder, digits int) error {
	if p.pos+digits > len(p.src) {
		return p.errf("truncated escape")
	}
	v, err := strconv.ParseUint(p.src[p.pos:p.pos+digits], 16, 32)
	if err != nil {
		return p.errf("invalid hex escape")
	}
	p.pos += digits
	// surrogate はそのまま rune にすると U+FFFD に化ける。JSON 側でも
	// `\uXXXX` として書けるので、エスケープのまま通す。
	if v >= 0xD800 && v <= 0xDFFF {
		fmt.Fprintf(sb, "\\u%04X", v)
		return nil
	}
	sb.WriteRune(rune(v))
	return nil
}

// literal reads true / false / null / a number.
func (p *parser) literal(out *strings.Builder) error {
	for _, kw := range []string{"true", "false", "null"} {
		if strings.HasPrefix(p.src[p.pos:], kw) {
			p.pos += len(kw)
			out.WriteString(kw)
			return nil
		}
	}
	return p.number(out)
}

// number reads a JSON5 number and writes it as JSON.
//
// JSON に無い書き方 (`+1`, `.5`, `1.`, `0xFF`, `Infinity`, `NaN`) を正規化する。
// **Infinity / NaN は JSON で表現できない**ので、ここは落とすしかない。
func (p *parser) number(out *strings.Builder) error {
	start := p.pos
	neg := false
	if c := p.peek(); c == '+' || c == '-' {
		neg = c == '-'
		p.pos++
	}
	if strings.HasPrefix(p.src[p.pos:], "Infinity") || strings.HasPrefix(p.src[p.pos:], "NaN") {
		return p.errf("Infinity / NaN cannot be represented in JSON")
	}
	if strings.HasPrefix(p.src[p.pos:], "0x") || strings.HasPrefix(p.src[p.pos:], "0X") {
		p.pos += 2
		hs := p.pos
		for !p.eof() && isHexDigit(p.src[p.pos]) {
			p.pos++
		}
		if hs == p.pos {
			return p.errf("empty hex literal")
		}
		v, err := strconv.ParseUint(p.src[hs:p.pos], 16, 64)
		if err != nil {
			return p.errf("hex literal out of range")
		}
		if neg {
			out.WriteByte('-')
		}
		out.WriteString(strconv.FormatUint(v, 10))
		return nil
	}

	for !p.eof() {
		c := p.src[p.pos]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			p.pos++
			continue
		}
		break
	}
	raw := p.src[start:p.pos]
	if raw == "" || raw == "+" || raw == "-" {
		return p.errf("expected value")
	}
	f, err := strconv.ParseFloat(strings.TrimPrefix(raw, "+"), 64)
	if err != nil {
		return p.errf("invalid number %q", raw)
	}
	// 表記は JSON の範囲に正規化する (`.5` -> `0.5`、`1.` -> `1`)。
	out.WriteString(strconv.FormatFloat(f, 'g', -1, 64))
	return nil
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// encodeJSONString writes s as a JSON string literal.
func encodeJSONString(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&sb, `\u%04X`, r)
				continue
			}
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
