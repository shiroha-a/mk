// Command pluginbuild discovers plugins under plugins/ and generates the files
// that compile them into mk-go (#2480).
//
// # なぜビルド時なのか
//
// mk-go は CGO_ENABLED=0 + `-tags nodynamic` の完全静的バイナリ (#619) で、
// runtime stage は distroless (#621) のためシェルもインタプリタも持たない。
// Go の `-buildmode=plugin` は cgo と動的リンクを要求するので使えず、外部
// インタプリタの exec もできない。
//
// ビルド時に組み込む方式の利点は、内部の変更でプラグインが壊れたときに
// **ビルドが落ちて即座に分かる**こと。実行時読み込みだと本番で静かに壊れる。
//
// # 生成物
//
//	go.work                           plugins/* をワークスペースに含める
//	cmd/misskey/plugins_generated.go  各プラグインを import して Register する
//
// どちらも gitignore 済み。プラグインが 1 つも無ければ**何も生成しない**ので、
// 素の `go build ./...` はそのまま通る。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/shiroha-a/mk/plugin"
)

// markerFile marks a directory as an intentional plugin.
//
// `go.mod` の有無だけで判定すると、置き忘れた作業用ディレクトリを巻き込んで
// ビルドが落ちる。意図の宣言を別途要求する。
const markerFile = "mk-plugin.yml"

// generatedFile is written into the main package.
const generatedFile = "cmd/misskey/plugins_generated.go"

// manifest is the marker file's content.
type manifest struct {
	// Name is informational; the authoritative name is the Go Definition's.
	Name string `yaml:"name"`
	// APIVersion lets us fail before compiling, with a message that says what
	// is wrong. これが無いと Go のコンパイルエラーか起動時 panic になり、
	// 「プラグインが古い」ことが読み取れない。
	APIVersion int `yaml:"apiVersion"`
}

// discovered is one plugin found under plugins/.
type discovered struct {
	dir        string
	modulePath string
	name       string
}

func main() {
	root := flag.String("root", ".", "リポジトリのルート")
	dir := flag.String("dir", "plugins", "プラグインを探すディレクトリ")
	flag.Parse()

	if err := run(*root, *dir); err != nil {
		fmt.Fprintln(os.Stderr, "pluginbuild:", err)
		os.Exit(1)
	}
}

func run(root, pluginDir string) error {
	found, err := discover(root, pluginDir)
	if err != nil {
		return err
	}

	genPath := filepath.Join(root, generatedFile)
	workPath := filepath.Join(root, "go.work")

	if len(found) == 0 {
		// 前回のビルドの残骸を消す。残すと、消したはずのプラグインが
		// 組み込まれたままになる。
		if err := removeIfExists(genPath); err != nil {
			return err
		}
		if err := removeIfExists(workPath); err != nil {
			return err
		}
		fmt.Println("pluginbuild: プラグインはありません")
		return nil
	}

	goVersion, err := goDirective(filepath.Join(root, "go.mod"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(workPath, []byte(renderWorkspace(found, goVersion)), 0o644); err != nil {
		return fmt.Errorf("go.work を書けません: %w", err)
	}
	if err := os.WriteFile(genPath, []byte(renderRegistration(found)), 0o644); err != nil {
		return fmt.Errorf("%s を書けません: %w", generatedFile, err)
	}

	for _, p := range found {
		fmt.Printf("pluginbuild: %s (%s)\n", p.name, p.modulePath)
	}
	return nil
}

// discover walks root/pluginDir and validates each candidate.
//
// **dir はリポジトリルートからの相対パスで返す。** go.work の `use` は go.work
// の位置からの相対で解決されるので、ルート込みの絶対パスを書くと別の場所で
// ビルドできない成果物になる。
func discover(root, pluginDir string) ([]discovered, error) {
	entries, err := os.ReadDir(filepath.Join(root, pluginDir))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s を読めません: %w", pluginDir, err)
	}

	var found []discovered
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := filepath.Join(pluginDir, e.Name())
		dir := filepath.Join(root, rel)

		// マーカーが無いものは**黙って飛ばす**。作業用ディレクトリを置いた
		// だけでビルドが落ちるのは扱いづらい。
		markerPath := filepath.Join(dir, markerFile)
		raw, err := os.ReadFile(markerPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s を読めません: %w", markerPath, err)
		}

		var m manifest
		if err := yaml.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%s を解釈できません: %w", markerPath, err)
		}
		if m.APIVersion != plugin.APIVersion {
			return nil, fmt.Errorf(
				"%s: apiVersion %d はこの mk-go (apiVersion %d) と互換がありません",
				dir, m.APIVersion, plugin.APIVersion)
		}

		// **go.mod を必須にする。** 無いと mk-go と同一モジュールになり、Go の
		// internal ルール上 internal/ が見えてしまう。plugin/ だけを公開面と
		// する設計 (#2476) がここで静かに崩れる。
		modPath, err := modulePath(filepath.Join(dir, "go.mod"))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}

		name := m.Name
		if name == "" {
			name = e.Name()
		}
		found = append(found, discovered{dir: rel, modulePath: modPath, name: name})
	}

	// ディレクトリ名で並べる。順序が環境依存だとビルドの再現性が失われる。
	sort.Slice(found, func(i, j int) bool { return found[i].dir < found[j].dir })
	return found, nil
}

// goDirective reads the `go` line out of a go.mod.
func goDirective(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s を読めません: %w", path, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "go "); ok {
			if v := strings.TrimSpace(rest); v != "" {
				return v, nil
			}
		}
	}
	return "", fmt.Errorf("%s に go directive がありません", path)
}

// modulePath reads the module line out of a go.mod.
func modulePath(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("go.mod がありません (プラグインは独立した Go module である必要があります)")
	}
	if err != nil {
		return "", fmt.Errorf("go.mod を読めません: %w", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			if p := strings.TrimSpace(rest); p != "" {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("go.mod に module 行がありません")
}

// renderWorkspace builds go.work.
//
// go directive は本体の go.mod から写す。**決め打ちにしない**: go.work の方が
// 新しい版を要求すると Go はその toolchain を取りに行き、無ければ
// 「toolchain not available」でビルド全体が止まる。本体と同じ版なら必ず手元に
// ある。
func renderWorkspace(found []discovered, goVersion string) string {
	var b strings.Builder
	b.WriteString("// Code generated by tools/pluginbuild. DO NOT EDIT.\n\n")
	b.WriteString("go " + goVersion + "\n\nuse (\n\t.\n")
	for _, p := range found {
		// go.work は常に / 区切り。
		b.WriteString("\t./" + filepath.ToSlash(p.dir) + "\n")
	}
	b.WriteString(")\n")
	return b.String()
}

// renderRegistration builds the generated main-package file.
//
// import alias は連番にする。プラグイン名をそのまま識別子に使うと、ハイフンを
// 含む名前 (game-info) が Go の識別子として不正になる。
func renderRegistration(found []discovered) string {
	var b strings.Builder
	b.WriteString("// Code generated by tools/pluginbuild. DO NOT EDIT.\n\n")
	b.WriteString("package main\n\nimport (\n")
	b.WriteString("\t\"github.com/shiroha-a/mk/plugin\"\n\n")
	for i, p := range found {
		fmt.Fprintf(&b, "\tp%d %q\n", i, p.modulePath)
	}
	b.WriteString(")\n\nfunc init() {\n")
	for i := range found {
		fmt.Fprintf(&b, "\tplugin.Register(p%d.Plugin)\n", i)
	}
	b.WriteString("}\n")
	return b.String()
}

// removeIfExists deletes path when present.
func removeIfExists(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%s を消せません: %w", path, err)
	}
	return nil
}
