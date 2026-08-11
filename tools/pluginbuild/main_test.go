package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/plugin"
)

// writePlugin lays down a plugin directory under root.
func writePlugin(t *testing.T, root, name, modulePath, marker string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	if marker != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, markerFile), []byte(marker), 0o644))
	}
	if modulePath != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
			[]byte("module "+modulePath+"\n\ngo 1.26.5\n"), 0o644))
	}
	return dir
}

func validMarker() string {
	return "name: hello\napiVersion: 1\n"
}

func TestDiscover_FindsPlugins(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "hello", "example.com/hello", validMarker())

	found, err := discover(root, "")
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "example.com/hello", found[0].modulePath)
	assert.Equal(t, "hello", found[0].name)
}

func TestDiscover_MissingDirIsEmpty(t *testing.T) {
	found, err := discover(t.TempDir(), "absent")
	require.NoError(t, err)
	assert.Empty(t, found)
}

// マーカーが無いものは黙って飛ばす。作業用ディレクトリを置いただけで
// ビルドが落ちるのは扱いづらい。
func TestDiscover_SkipsUnmarkedDirectories(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "scratch", "example.com/scratch", "")
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o644))

	found, err := discover(root, "")
	require.NoError(t, err)
	assert.Empty(t, found)
}

// **go.mod が無いものはエラーにする。** 黙って飛ばすと、mk-go と同一モジュール
// のまま internal/ が見える状態 (= 公開面の契約が消えた状態) に気付けない。
func TestDiscover_MissingGoModIsError(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "bad", "", validMarker())

	_, err := discover(root, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "go.mod がありません")
	assert.Contains(t, err.Error(), "独立した Go module")
}

// apiVersion 不一致はコンパイル前に落とす。Go のコンパイルエラーや起動時
// panic だと「プラグインが古い」ことが読み取れない。
func TestDiscover_APIVersionMismatch(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "old", "example.com/old", "name: old\napiVersion: 999\n")

	_, err := discover(root, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apiVersion 999")
	assert.Contains(t, err.Error(), "互換がありません")
}

// apiVersion 未記載も拒否する (0 は APIVersion と一致しない)。
func TestDiscover_MissingAPIVersionIsRejected(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "x", "example.com/x", "name: x\n")

	_, err := discover(root, "")
	require.Error(t, err)
}

func TestDiscover_BrokenMarkerIsError(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "x", "example.com/x", "\tname: [unclosed\n")

	_, err := discover(root, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "解釈できません")
}

// name 未指定ならディレクトリ名を使う。
func TestDiscover_FallsBackToDirName(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "dirname", "example.com/x", "apiVersion: 1\n")

	found, err := discover(root, "")
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "dirname", found[0].name)
}

// **順序が決定的であること。** 環境依存だとビルドの再現性が失われる。
func TestDiscover_IsDeterministicallyOrdered(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"zeta", "alpha", "mid"} {
		writePlugin(t, root, n, "example.com/"+n, validMarker())
	}

	found, err := discover(root, "")
	require.NoError(t, err)
	require.Len(t, found, 3)

	var names []string
	for _, f := range found {
		names = append(names, filepath.Base(f.dir))
	}
	assert.Equal(t, []string{"alpha", "mid", "zeta"}, names)
}

func TestModulePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")

	require.NoError(t, os.WriteFile(path, []byte("module example.com/x\n\ngo 1.26.5\n"), 0o644))
	got, err := modulePath(path)
	require.NoError(t, err)
	assert.Equal(t, "example.com/x", got)

	require.NoError(t, os.WriteFile(path, []byte("go 1.26.5\n"), 0o644))
	_, err = modulePath(path)
	assert.Error(t, err, "module 行が無ければエラー")
}

func TestGoDirective(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go.mod")

	require.NoError(t, os.WriteFile(path, []byte("module x\n\ngo 1.26.5\n"), 0o644))
	got, err := goDirective(path)
	require.NoError(t, err)
	assert.Equal(t, "1.26.5", got)

	require.NoError(t, os.WriteFile(path, []byte("module x\n"), 0o644))
	_, err = goDirective(path)
	assert.Error(t, err)

	_, err = goDirective(filepath.Join(dir, "absent"))
	assert.Error(t, err)
}

// go.work の go directive は本体の go.mod から写す。決め打ちにすると、
// 手元に無い toolchain を要求してビルド全体が止まる。
func TestRenderWorkspace_UsesGivenGoVersion(t *testing.T) {
	out := renderWorkspace([]discovered{{dir: "plugins/hello"}}, "1.26.5")

	assert.Contains(t, out, "go 1.26.5\n")
	assert.Contains(t, out, "\t.\n")
	assert.Contains(t, out, "\t./plugins/hello\n")
	assert.Contains(t, out, "DO NOT EDIT")
}

// import alias は連番。プラグイン名をそのまま識別子にすると、ハイフンを含む
// 名前が Go の識別子として不正になる。
func TestRenderRegistration(t *testing.T) {
	out := renderRegistration([]discovered{
		{modulePath: "example.com/game-info", name: "game-info"},
		{modulePath: "example.com/b", name: "b"},
	})

	assert.Contains(t, out, "package main")
	assert.Contains(t, out, `p0 "example.com/game-info"`)
	assert.Contains(t, out, `p1 "example.com/b"`)
	assert.Contains(t, out, "plugin.Register(p0.Plugin)")
	assert.Contains(t, out, "plugin.Register(p1.Plugin)")
	assert.NotContains(t, out, "game-info.Plugin", "ハイフン入りの識別子を作らない")
}

// --- run ---

// fakeRepo builds a repo root with go.mod and cmd/misskey/.
func fakeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/shiroha-a/mk\n\ngo 1.26.5\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "cmd", "misskey"), 0o755))
	return root
}

func TestRun_GeneratesFiles(t *testing.T) {
	root := fakeRepo(t)
	writePlugin(t, filepath.Join(root, "plugins"), "hello", "example.com/hello", validMarker())

	require.NoError(t, run(root, "plugins"))

	work, err := os.ReadFile(filepath.Join(root, "go.work"))
	require.NoError(t, err)
	assert.Contains(t, string(work), "./plugins/hello")

	gen, err := os.ReadFile(filepath.Join(root, generatedFile))
	require.NoError(t, err)
	assert.Contains(t, string(gen), "example.com/hello")
}

// **プラグインが無ければ前回の生成物を消す。** 残すと、消したはずのプラグインが
// 組み込まれたままになる。
func TestRun_RemovesStaleArtifacts(t *testing.T) {
	root := fakeRepo(t)
	genPath := filepath.Join(root, generatedFile)
	workPath := filepath.Join(root, "go.work")
	require.NoError(t, os.WriteFile(genPath, []byte("stale"), 0o644))
	require.NoError(t, os.WriteFile(workPath, []byte("stale"), 0o644))

	require.NoError(t, run(root, "plugins"))

	assert.NoFileExists(t, genPath)
	assert.NoFileExists(t, workPath)
}

// プラグインが無い状態で 2 回走らせても失敗しない (消すものが無い)。
func TestRun_NoPluginsIsIdempotent(t *testing.T) {
	root := fakeRepo(t)
	require.NoError(t, run(root, "plugins"))
	require.NoError(t, run(root, "plugins"))
}

func TestRun_PropagatesDiscoverError(t *testing.T) {
	root := fakeRepo(t)
	writePlugin(t, filepath.Join(root, "plugins"), "bad", "", validMarker())

	err := run(root, "plugins")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "go.mod")
}

// 生成した registration が現在の APIVersion と整合していること。マーカーの
// apiVersion を検査する側と plugin.APIVersion がずれていないかの歯止め。
// go.mod が読めない (ディレクトリになっている等) 場合もエラーにする。
func TestModulePath_UnreadableIsError(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "go.mod"), 0o755))

	_, err := modulePath(filepath.Join(dir, "go.mod"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "読めません")
}

// マーカーが読めない (ディレクトリになっている) 場合もエラーにする。
// 「マーカーが無い」(= 飛ばす) と区別すること。
func TestDiscover_UnreadableMarkerIsError(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "x")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, markerFile), 0o755))

	_, err := discover(root, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "読めません")
}

// plugins/ がファイルとして存在する場合など、読めない状態はエラーにする
// (「無い」= 正常とは区別する)。
func TestDiscover_UnreadableDirIsError(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "plugins"), []byte("x"), 0o644))

	_, err := discover(root, "plugins")
	require.Error(t, err)
}

// go.mod が無い状態で生成しようとしたらエラーにする。go directive を写せない。
func TestRun_MissingRootGoModIsError(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "cmd", "misskey"), 0o755))
	writePlugin(t, filepath.Join(root, "plugins"), "hello", "example.com/hello", validMarker())

	err := run(root, "plugins")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "go.mod")
}

// 生成先のディレクトリが無ければ書き込みエラーを返す (黙って成功しない)。
func TestRun_UnwritableTargetIsError(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/shiroha-a/mk\n\ngo 1.26.5\n"), 0o644))
	// cmd/misskey/ を作らないので generated file が書けない。
	writePlugin(t, filepath.Join(root, "plugins"), "hello", "example.com/hello", validMarker())

	err := run(root, "plugins")
	require.Error(t, err)
	assert.Contains(t, err.Error(), generatedFile)
}

// 消せない残骸はエラーにする (ディレクトリになっている場合)。
func TestRemoveIfExists_ErrorIsReported(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "d")
	require.NoError(t, os.MkdirAll(filepath.Join(target, "child"), 0o755))

	err := removeIfExists(target)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "消せません")
}

func TestMarkerAPIVersionMatchesPackage(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "x", "example.com/x", "apiVersion: "+strconv.Itoa(plugin.APIVersion)+"\n")

	found, err := discover(root, "")
	require.NoError(t, err)
	assert.Len(t, found, 1)
}
