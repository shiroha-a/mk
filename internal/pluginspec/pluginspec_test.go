package pluginspec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleSurface(t *testing.T) []string {
	t.Helper()
	got, err := Surface("testdata/sample")
	require.NoError(t, err)
	return got
}

func TestSurface_IncludesExported(t *testing.T) {
	got := sampleSurface(t)

	for _, want := range []string{
		"const Version",
		"type Thing struct",
		"  field Thing.Name string",
		"type Doer interface",
		"  method Doer.Do(context.Context, int) (string, error)",
		"type Callback func",
		"func Exported(string, string, ...int) (string, error)",
		"func (Thing) Method() string",
	} {
		assert.Containsf(t, got, want, "%q が公開面に含まれる", want)
	}
}

// 非公開のものは一切出さない。出すと「公開面が変わっていない」判定が
// 内部変更で揺れる。
func TestSurface_ExcludesUnexported(t *testing.T) {
	joined := strings.Join(sampleSurface(t), "\n")

	for _, unwanted := range []string{"hidden", "secret", "unexported", "internalThing"} {
		assert.NotContainsf(t, joined, unwanted, "%q は公開面に出さない", unwanted)
	}
}

// **_test.go は対象外。** テスト用のヘルパーを公開面として数えると、
// テストを足すたびに gate が落ちる。
func TestSurface_ExcludesTestFiles(t *testing.T) {
	assert.NotContains(t, strings.Join(sampleSurface(t), "\n"), "ShouldNotAppear")
}

// 引数名は記録しない。名前を変えただけで差分が出ると、意味のない更新が
// 増えて gate が形骸化する。
func TestSurface_IgnoresParameterNames(t *testing.T) {
	joined := strings.Join(sampleSurface(t), "\n")
	assert.Contains(t, joined, "func Exported(string, string, ...int)")
	assert.NotContains(t, joined, "a string")
}

// グループ化した引数 (`a, b string`) が 2 つに展開されること。1 つに潰すと
// 引数の増減を見逃す。
func TestSurface_ExpandsGroupedParameters(t *testing.T) {
	joined := strings.Join(sampleSurface(t), "\n")
	assert.Contains(t, joined, "type Callback func")
	assert.Contains(t, joined, "func Exported(string, string, ...int)")
}

func TestSurface_IsSorted(t *testing.T) {
	got := sampleSurface(t)
	require.NotEmpty(t, got)
	for i := 1; i < len(got); i++ {
		assert.LessOrEqual(t, got[i-1], got[i], "並びが決定的であること")
	}
}

func TestSurface_MissingDirIsError(t *testing.T) {
	_, err := Surface("testdata/absent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "解析できません")
}

func TestRender(t *testing.T) {
	out := Render([]string{"const A", "func B()"})

	assert.True(t, strings.HasPrefix(out, "# Code generated"))
	assert.Contains(t, out, "DO NOT EDIT")
	assert.Contains(t, out, "const A\n")
	assert.Contains(t, out, "func B()\n")
	assert.True(t, strings.HasSuffix(out, "\n"))
}

// 実際の plugin/ を解析できること。golden との一致は
// internal/entitycompat の TestPluginSurfaceDrift が見る。
func TestSurface_RealPluginPackage(t *testing.T) {
	got, err := Surface("../../" + DefaultDir)
	require.NoError(t, err)
	assert.Contains(t, got, "const APIVersion")
	assert.Contains(t, got, "func Register(Definition)")
}

// **plugintest も追跡対象に入っていること。** 公開パッケージなので export が
// 増えれば semver の対象になる。テスト用だからと外すと、そこだけ黙って育つ。
func TestTrackedDirs_IncludesPluginTest(t *testing.T) {
	assert.Contains(t, TrackedDirs, "plugin")
	assert.Contains(t, TrackedDirs, "plugin/plugintest")
}

func TestSurfaceAll_PrefixesByPackage(t *testing.T) {
	got, err := SurfaceAll("../..", TrackedDirs)
	require.NoError(t, err)

	assert.Contains(t, got, "plugin: const APIVersion")
	assert.Contains(t, got, "plugin/plugintest: func New(*testing.T) *Harness")

	// 並びが決定的であること。
	for i := 1; i < len(got); i++ {
		assert.LessOrEqual(t, got[i-1], got[i])
	}
}

func TestSurfaceAll_PropagatesError(t *testing.T) {
	_, err := SurfaceAll("../..", []string{"absent-package"})
	require.Error(t, err)
}
