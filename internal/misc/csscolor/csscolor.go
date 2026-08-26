// Package csscolor parses CSS color strings the way tinycolor2 does.
//
// upstream Misskey の `FetchInstanceMetadataService.getThemeColor` は
// `new tinycolor(themeColor)` で検証し、有効なら `toHexString()` (= `#rrggbb`)
// を保存、無効なら null にする。mk-go は長さと NUL しか見ておらず、`red` や
// `rgb(1,2,3)` をそのまま `instance.themeColor` に入れていた (#2726)。
//
// **tinycolor の挙動をそのまま写している。** 受理する形も、範囲外の値の丸め方も
// 独自判断を入れない。`rgb(300,0,0)` が `#ff0000` に、`rgb(-5,0,0)` が
// `#000000` に、`hsl(-60,...)` が (CSS の 300 度ではなく) `#ff0000` になるのは
// tinycolor がそう畳むからで、CSS の仕様ではない。
//
// `names.go` と `vectors_test.go` は**実物の tinycolor2 から生成したもの**で、
// 手で書き足さない。submodule の tinycolor2 を上げたときは
// `internal/misc/csscolor/gen/gen.js` を実行して作り直す:
//
//	cd third_party/misskey/packages/backend/node_modules/tinycolor2
//	node /path/to/mk/internal/misc/csscolor/gen/gen.js /path/to/mk
//
// 生成しなおしたら `go test ./internal/misc/csscolor/` を回す。差分が出たら
// 本 package を tinycolor の新しい挙動に合わせること。
package csscolor

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// tinycolor の matchers (tinycolor.js の `matchers`) をそのまま移したもの。
// **anchor しない。** tinycolor も `new RegExp("rgb" + PERMISSIVE_MATCH3)` を
// そのまま exec するので、`xx rgb(1,2,3)` のような前後のゴミを許す。
const (
	cssNumber  = `[-\+]?\d*\.\d+%?`
	cssInteger = `[-\+]?\d+%?`
	cssUnit    = `(?:` + cssNumber + `)|(?:` + cssInteger + `)`

	permissiveMatch3 = `[\s|\(]+(` + cssUnit + `)[,|\s]+(` + cssUnit + `)[,|\s]+(` + cssUnit + `)\s*\)?`
	permissiveMatch4 = `[\s|\(]+(` + cssUnit + `)[,|\s]+(` + cssUnit + `)[,|\s]+(` + cssUnit + `)[,|\s]+(` + cssUnit + `)\s*\)?`
)

var (
	reRGB  = regexp.MustCompile(`rgb` + permissiveMatch3)
	reRGBA = regexp.MustCompile(`rgba` + permissiveMatch4)
	reHSL  = regexp.MustCompile(`hsl` + permissiveMatch3)
	reHSLA = regexp.MustCompile(`hsla` + permissiveMatch4)
	reHSV  = regexp.MustCompile(`hsv` + permissiveMatch3)
	reHSVA = regexp.MustCompile(`hsva` + permissiveMatch4)

	reHex3 = regexp.MustCompile(`^#?([0-9a-fA-F]{1})([0-9a-fA-F]{1})([0-9a-fA-F]{1})$`)
	reHex4 = regexp.MustCompile(`^#?([0-9a-fA-F]{1})([0-9a-fA-F]{1})([0-9a-fA-F]{1})([0-9a-fA-F]{1})$`)
	reHex6 = regexp.MustCompile(`^#?([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})$`)
	reHex8 = regexp.MustCompile(`^#?([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})$`)
)

// Normalize returns the `#rrggbb` form of a CSS color string, mirroring
// tinycolor2's `c.isValid() ? c.toHexString() : null`.
//
// ok is false for anything tinycolor rejects. alpha は捨てる (`toHexString` も
// 捨てるので `rgba(0,0,0,0)` は `#000000`)。
func Normalize(s string) (hex string, ok bool) {
	r, g, b, ok := parse(s)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("#%02x%02x%02x", round255(r), round255(g), round255(b)), true
}

// parse mirrors tinycolor's stringInputToObject + inputToRGB for string input.
// 戻り値は 0-255 の実数 (丸めは呼び出し側)。
func parse(s string) (r, g, b float64, ok bool) {
	color := strings.ToLower(strings.TrimSpace(s))
	if hex, named := names[color]; named {
		color = hex
	} else if color == "transparent" {
		// tinycolor は `{r:0,g:0,b:0,a:0}` を返す。alpha は toHexString で
		// 落ちるので黒になる。
		return 0, 0, 0, true
	}

	if m := reRGB.FindStringSubmatch(color); m != nil {
		return bound01(m[1], 255) * 255, bound01(m[2], 255) * 255, bound01(m[3], 255) * 255, true
	}
	if m := reRGBA.FindStringSubmatch(color); m != nil {
		return bound01(m[1], 255) * 255, bound01(m[2], 255) * 255, bound01(m[3], 255) * 255, true
	}
	if m := reHSL.FindStringSubmatch(color); m != nil {
		r, g, b := hslToRGB(m[1], convertToPercentage(m[2]), convertToPercentage(m[3]))
		return r, g, b, true
	}
	if m := reHSLA.FindStringSubmatch(color); m != nil {
		r, g, b := hslToRGB(m[1], convertToPercentage(m[2]), convertToPercentage(m[3]))
		return r, g, b, true
	}
	if m := reHSV.FindStringSubmatch(color); m != nil {
		r, g, b := hsvToRGB(m[1], convertToPercentage(m[2]), convertToPercentage(m[3]))
		return r, g, b, true
	}
	if m := reHSVA.FindStringSubmatch(color); m != nil {
		r, g, b := hsvToRGB(m[1], convertToPercentage(m[2]), convertToPercentage(m[3]))
		return r, g, b, true
	}
	// hex は長い順。`#abcd` を hex8 より先に hex4 で拾うと `#aabbcc` が
	// `#aabbccdd` に化ける、といった取り違えを防ぐ順序 (tinycolor と同じ)。
	if m := reHex8.FindStringSubmatch(color); m != nil {
		return hexPair(m[1]), hexPair(m[2]), hexPair(m[3]), true
	}
	if m := reHex6.FindStringSubmatch(color); m != nil {
		return hexPair(m[1]), hexPair(m[2]), hexPair(m[3]), true
	}
	if m := reHex4.FindStringSubmatch(color); m != nil {
		return hexPair(m[1] + m[1]), hexPair(m[2] + m[2]), hexPair(m[3] + m[3]), true
	}
	if m := reHex3.FindStringSubmatch(color); m != nil {
		return hexPair(m[1] + m[1]), hexPair(m[2] + m[2]), hexPair(m[3] + m[3]), true
	}
	return 0, 0, 0, false
}

// hexPair parses a 2-digit hex component.
func hexPair(s string) float64 {
	v, _ := strconv.ParseInt(s, 16, 32)
	return float64(v)
}

// round255 clamps to [0,255] and rounds like JS `Math.round` (half up).
// 値は非負なので Go の math.Round (half away from zero) と一致する。
func round255(v float64) int {
	v = math.Min(255, math.Max(0, v))
	return int(math.Round(v))
}

// parseFloatJS mirrors JS parseFloat: 先頭から読める分だけ読む ("50%" → 50)。
// 数字が 1 つも無ければ NaN。
//
// **桁溢れは NaN にしない。** CSS_INTEGER は桁数を縛らないので `rgb(999...9,0,0)`
// (400 桁) が実際に届きうる。JS の parseFloat は Infinity を返し、tinycolor は
// それを 255 に clamp して `#ff0000` にする (実測)。ここで NaN にすると以降の
// 演算がすべて NaN になり、丸めの結果が未定義になる。
func parseFloatJS(s string) float64 {
	end := 0
	seenDigit := false
	seenDot := false
	for i, c := range s {
		if i == 0 && (c == '+' || c == '-') {
			end = i + 1
			continue
		}
		if c >= '0' && c <= '9' {
			seenDigit = true
			end = i + 1
			continue
		}
		if c == '.' && !seenDot {
			seenDot = true
			end = i + 1
			continue
		}
		break
	}
	if !seenDigit {
		return math.NaN()
	}
	// err は範囲外 (= ±Inf) でのみ返る。値はそのまま使う (上のコメント参照)。
	v, _ := strconv.ParseFloat(s[:end], 64)
	return v
}

// bound01 mirrors tinycolor's bound01: 入力を 0..1 に畳む。
//
// **独自に整理しない。** 順序と早期 return がそのまま出力に効く:
//   - 先に `[0, max]` へ clamp するので、負の hue は 0 (= 赤) になる。
//     `hsl(-60,...)` が CSS の 300 度ではなく 0 度になるのはこのため
//   - `Math.abs(n - max) < 0.000001` の早期 return が無いと、`rgb(255,..)` が
//     `255 % 255 = 0` で黒に落ちる
func bound01(n string, max float64) float64 {
	value := n
	// isOnePointZero: "1.0" のような **小数点を含む** 1 は 100% 扱い。
	if strings.Contains(n, ".") && parseFloatJS(n) == 1 {
		value = "100%"
	}
	processPercent := strings.Contains(value, "%")

	v := math.Min(max, math.Max(0, parseFloatJS(value)))
	if processPercent {
		// JS の `parseInt(n * max, 10)` は小数を切り捨てる (0 方向)。
		v = math.Trunc(v*max) / 100
	}
	if math.Abs(v-max) < 0.000001 {
		return 1
	}
	return math.Mod(v, max) / max
}

// convertToPercentage mirrors tinycolor's convertToPercentage: 1 以下の値は
// 割合表記とみなして "N%" に直す。
//
// JS は文字列を数値へ暗黙変換して比較するので、"100%" のような数値化できない
// 文字列は NaN <= 1 = false になり、そのまま返る。
func convertToPercentage(n string) string {
	v, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
	if err != nil || math.IsNaN(v) {
		return n
	}
	if v <= 1 {
		return jsNumberString(v*100) + "%"
	}
	return n
}

// jsNumberString formats a float the way JS `String(number)` does for the
// values convertToPercentage produces (整数なら小数点を出さない)。
func jsNumberString(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// hslToRGB mirrors tinycolor's hslToRgb. 戻り値は 0-255 の実数。
func hslToRGB(hRaw, sRaw, lRaw string) (float64, float64, float64) {
	h := bound01(hRaw, 360)
	s := bound01(sRaw, 100)
	l := bound01(lRaw, 100)

	if s == 0 {
		return l * 255, l * 255, l * 255
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	return hue2rgb(p, q, h+1.0/3.0) * 255, hue2rgb(p, q, h) * 255, hue2rgb(p, q, h-1.0/3.0) * 255
}

func hue2rgb(p, q, t float64) float64 {
	if t < 0 {
		t++
	}
	if t > 1 {
		t--
	}
	switch {
	case t < 1.0/6.0:
		return p + (q-p)*6*t
	case t < 1.0/2.0:
		return q
	case t < 2.0/3.0:
		return p + (q-p)*(2.0/3.0-t)*6
	default:
		return p
	}
}

// hsvToRGB mirrors tinycolor's hsvToRgb. 戻り値は 0-255 の実数。
func hsvToRGB(hRaw, sRaw, vRaw string) (float64, float64, float64) {
	h := bound01(hRaw, 360) * 6
	s := bound01(sRaw, 100)
	v := bound01(vRaw, 100)

	i := math.Floor(h)
	f := h - i
	p := v * (1 - s)
	q := v * (1 - f*s)
	t := v * (1 - (1-f)*s)
	// bound01 が [0,1] を返すので h は [0,6]、i は [0,6] に収まる。
	// `i % 6` は負にならない。
	mod := int(math.Mod(i, 6))
	r := [6]float64{v, q, p, p, t, v}[mod]
	g := [6]float64{t, v, v, q, p, p}[mod]
	b := [6]float64{p, p, t, v, v, q}[mod]
	return r * 255, g * 255, b * 255
}

// Names returns every colour name tinycolor accepts. テスト用。
func Names() []string {
	out := make([]string, 0, len(names))
	for k := range names {
		out = append(out, k)
	}
	return out
}
