package csscolor

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// parseFloatJS は JS の parseFloat をなぞる。**regexp が通した CSS unit しか
// 渡らない**ので実運用では digit 無しにならないが、JS の意味をそのまま持たせて
// おく (将来の呼び出しで暗黙に 0 として扱われないように)。
func TestParseFloatJS(t *testing.T) {
	assert.Equal(t, 50.0, parseFloatJS("50%"))
	assert.Equal(t, -60.0, parseFloatJS("-60"))
	assert.Equal(t, 0.5, parseFloatJS(".5"))
	assert.Equal(t, 7.0, parseFloatJS("+7"))
	assert.Equal(t, 1.5, parseFloatJS("1.5.5"), "2 つ目の小数点で止まる")
	assert.True(t, math.IsNaN(parseFloatJS("")))
	assert.True(t, math.IsNaN(parseFloatJS("abc")))
	assert.True(t, math.IsNaN(parseFloatJS("+")))
	assert.True(t, math.IsNaN(parseFloatJS(".")))
	// 桁溢れは Infinity (JS の parseFloat と同じ)。NaN にすると以降が全部
	// NaN になり丸めが未定義になる。
	assert.True(t, math.IsInf(parseFloatJS(strings.Repeat("9", 400)), 1))
	assert.True(t, math.IsInf(parseFloatJS("-"+strings.Repeat("9", 400)), -1))
}

// bound01 の早期 return と clamp。ここが変わると `rgb(255,0,0)` が黒に落ちる。
func TestBound01(t *testing.T) {
	assert.Equal(t, 1.0, bound01("255", 255))
	assert.Equal(t, 0.0, bound01("0", 255))
	assert.Equal(t, 1.0, bound01("300", 255), "clamp してから max 一致で 1")
	assert.Equal(t, 0.0, bound01("-5", 255), "負は 0 に clamp する")
	assert.Equal(t, 1.0, bound01("100%", 255))
	assert.Equal(t, 1.0, bound01("1.0", 255), "小数点を含む 1 は 100% 扱い")
	assert.InDelta(t, 1.0/255.0, bound01("1", 255), 1e-12)
}
