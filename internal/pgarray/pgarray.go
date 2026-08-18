// Package pgarray provides PostgreSQL array types for database/sql.
//
// github.com/lib/pq の array.go から StringArray / Int64Array と、その解析・
// 生成に必要なヘルパーだけを取り込んだもの (#2628)。
//
// **なぜ取り込んだか。** lib/pq はドライバとしては使っていない
// (sql.Open の driver 名は全て "pgx") が、import すると init() が
// sql.Register("postgres", ...) を実行する。govulncheck は sql.Open の
// driver 名を静的に決められないため登録済みドライバ全てへエッジを張り、
// GO-2026-6173 (pq の pre-protocol error reader) が到達可能と判定される。
// 実行時に選ばれる経路は無いので実害は無いが、修正版が存在しない
// (Fixed in: N/A) ため更新では消せない。配列型は array.go に閉じていて
// 標準ライブラリしか import しないので、必要な部分だけ持ち込んで依存を切った。
//
// 脆弱性は conn.go 側にあり、ここに取り込んだコードとは無関係。
//
// 取り込み元との等価性は lib/pq と突き合わせて確認済み (空配列 / SQL NULL /
// NULL 要素 / 区切り文字・引用符・バックスラッシュ・マルチバイト・制御文字を
// 含む要素 / 壊れたリテラルに対するエラーの出方 / 相互運用、および Fuzz 618 万回)。
// **DB に既に入っているリテラルを読み書きするので挙動を変えないこと。**
//
// 唯一変えたのは error メッセージの prefix (`pq:` → `pgarray:`)。lib/pq を
// 使っていないのに pq を名乗ると誤解を招くため。エラー文字列に依存している
// 呼び出し元は無い。
//
// ---
//
// MIT License
//
// Copyright (c) 2011-2013, 'pq' Contributors. Portions Copyright (c) 2011 Blake Mizerany
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package pgarray

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
)

type Int64Array []int64

// Scan implements the sql.Scanner interface.
func (a *Int64Array) Scan(src any) error {
	switch src := src.(type) {
	case []byte:
		return a.scanBytes(src)
	case string:
		return a.scanBytes([]byte(src))
	case nil:
		*a = nil
		return nil
	}

	return fmt.Errorf("pgarray: cannot convert %T to Int64Array", src)
}

func (a *Int64Array) scanBytes(src []byte) error {
	elems, err := scanLinearArray(src, []byte{','}, "Int64Array")
	if err != nil {
		return err
	}
	if *a != nil && len(elems) == 0 {
		*a = (*a)[:0]
	} else {
		b := make(Int64Array, len(elems))
		for i, v := range elems {
			b[i], err = strconv.ParseInt(string(v), 10, 64)
			if err != nil {
				return fmt.Errorf("pgarray: parsing array element index %d: %w", i, err)
			}
		}
		*a = b
	}
	return nil
}

// Value implements the driver.Valuer interface.
func (a Int64Array) Value() (driver.Value, error) {
	if a == nil {
		return nil, nil
	}

	if n := len(a); n > 0 {
		// There will be at least two curly brackets, N bytes of values,
		// and N-1 bytes of delimiters.
		b := make([]byte, 1, 1+2*n)
		b[0] = '{'

		b = strconv.AppendInt(b, a[0], 10)
		for i := 1; i < n; i++ {
			b = append(b, ',')
			b = strconv.AppendInt(b, a[i], 10)
		}

		return string(append(b, '}')), nil
	}

	return "{}", nil
}

// Int32Array represents a one-dimensional array of the PostgreSQL integer types.
type StringArray []string

// Scan implements the sql.Scanner interface.
func (a *StringArray) Scan(src any) error {
	switch src := src.(type) {
	case []byte:
		return a.scanBytes(src)
	case string:
		return a.scanBytes([]byte(src))
	case nil:
		*a = nil
		return nil
	}

	return fmt.Errorf("pgarray: cannot convert %T to StringArray", src)
}

func (a *StringArray) scanBytes(src []byte) error {
	elems, err := scanLinearArray(src, []byte{','}, "StringArray")
	if err != nil {
		return err
	}
	if *a != nil && len(elems) == 0 {
		*a = (*a)[:0]
	} else {
		b := make(StringArray, len(elems))
		for i, v := range elems {
			if b[i] = string(v); v == nil {
				return fmt.Errorf("pgarray: parsing array element index %d: cannot convert nil to string", i)
			}
		}
		*a = b
	}
	return nil
}

// Value implements the driver.Valuer interface.
func (a StringArray) Value() (driver.Value, error) {
	if a == nil {
		return nil, nil
	}

	if n := len(a); n > 0 {
		// There will be at least two curly brackets, 2*N bytes of quotes,
		// and N-1 bytes of delimiters.
		b := make([]byte, 1, 1+3*n)
		b[0] = '{'

		b = appendArrayQuotedBytes(b, []byte(a[0]))
		for i := 1; i < n; i++ {
			b = append(b, ',')
			b = appendArrayQuotedBytes(b, []byte(a[i]))
		}

		return string(append(b, '}')), nil
	}

	return "{}", nil
}

// appendArray appends rv to the buffer, returning the extended buffer and the
// delimiter used between elements.
//
// Returns an error when n <= 0 or rv is not a reflect.Array or reflect.Slice.
func appendArrayQuotedBytes(b, v []byte) []byte {
	b = append(b, '"')
	for {
		i := bytes.IndexAny(v, `"\`)
		if i < 0 {
			b = append(b, v...)
			break
		}
		if i > 0 {
			b = append(b, v[:i]...)
		}
		b = append(b, '\\', v[i])
		v = v[i+1:]
	}
	return append(b, '"')
}

func parseArray(src, del []byte) (dims []int, elems [][]byte, err error) {
	var depth, i int

	if len(src) < 1 || src[0] != '{' {
		return nil, nil, fmt.Errorf("pgarray: unable to parse array; expected %q at offset %d", '{', 0)
	}

Open:
	for i < len(src) {
		switch src[i] {
		case '{':
			depth++
			i++
		case '}':
			elems = make([][]byte, 0)
			goto Close
		default:
			break Open
		}
	}
	dims = make([]int, i)

Element:
	for i < len(src) {
		switch src[i] {
		case '{':
			if depth == len(dims) {
				break Element
			}
			depth++
			dims[depth-1] = 0
			i++
		case '"':
			var elem = []byte{}
			var escape bool
			for i++; i < len(src); i++ {
				if escape {
					elem = append(elem, src[i])
					escape = false
				} else {
					switch src[i] {
					default:
						elem = append(elem, src[i])
					case '\\':
						escape = true
					case '"':
						elems = append(elems, elem)
						i++
						break Element
					}
				}
			}
		default:
			for start := i; i < len(src); i++ {
				if bytes.HasPrefix(src[i:], del) || src[i] == '}' {
					elem := src[start:i]
					if len(elem) == 0 {
						return nil, nil, fmt.Errorf("pgarray: unable to parse array; unexpected %q at offset %d", src[i], i)
					}
					if bytes.Equal(elem, []byte("NULL")) {
						elem = nil
					}
					elems = append(elems, elem)
					break Element
				}
			}
		}
	}

	for i < len(src) {
		if bytes.HasPrefix(src[i:], del) && depth > 0 {
			dims[depth-1]++
			i += len(del)
			goto Element
		} else if src[i] == '}' && depth > 0 {
			dims[depth-1]++
			depth--
			i++
		} else {
			return nil, nil, fmt.Errorf("pgarray: unable to parse array; unexpected %q at offset %d", src[i], i)
		}
	}

Close:
	for i < len(src) {
		if src[i] == '}' && depth > 0 {
			depth--
			i++
		} else {
			return nil, nil, fmt.Errorf("pgarray: unable to parse array; unexpected %q at offset %d", src[i], i)
		}
	}
	if depth > 0 {
		err = fmt.Errorf("pgarray: unable to parse array; expected %q at offset %d", '}', i)
	}
	if err == nil {
		for _, d := range dims {
			if (len(elems) % d) != 0 {
				err = fmt.Errorf("pgarray: multidimensional arrays must have elements with matching dimensions")
			}
		}
	}
	return
}

func scanLinearArray(src, del []byte, typ string) (elems [][]byte, err error) {
	dims, elems, err := parseArray(src, del)
	if err != nil {
		return nil, err
	}
	if len(dims) > 1 {
		return nil, fmt.Errorf("pgarray: cannot convert ARRAY%s to %s", strings.Replace(fmt.Sprint(dims), " ", "][", -1), typ)
	}
	return elems, err
}
