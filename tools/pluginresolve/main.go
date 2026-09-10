// Command pluginresolve clones the plugins named in a build request into
// plugins/<name>/ so that pluginbuild can pick them up.
//
// It exists for the reusable workflow (#2940): operators declare the plugins
// they want as plain lines and CI materialises them before the image build.
//
// Input is one plugin per line, `<name> <url> <ref>` separated by spaces.
// Blank lines and `#` comments are ignored.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// spec is one resolved plugin request.
type spec struct {
	name string
	url  string
	ref  string
}

var (
	// ディレクトリ名になるので小文字に寄せる。大文字小文字を区別しない
	// ファイルシステムで別名が同じディレクトリに落ちるのを避ける。
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	// tag / branch / commit SHA のいずれも受ける。
	refRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "pluginresolve:", err)
		os.Exit(1)
	}
}

// run parses the request and materialises every plugin under plugins/.
//
// flag の解釈まで含めて main から切り出してある。`-dry-run` が本当に clone
// しないことと、そうでないときに clone が走ることをテストで確かめるため。
//
// **out には spec 以外を書かない。** 呼び出し側 (build-with-plugins workflow) は
// `-dry-run` の stdout を機械可読な一覧としてパースするので、人間向けの案内文を
// 混ぜると 1 行目がプラグイン名として解釈される。
//
// この不変条件のうちテストで固定できているのは「0 件のときに out が空」までで、
// **clone 成功時の進捗行の出力先は固定できていない** — 成功パスを通すには
// parseSpecs を通る https の origin が要り、テストからは用意できないため。
func run(args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("pluginresolve", flag.ContinueOnError)
	fs.SetOutput(errOut)
	dryRun := fs.Bool("dry-run", false, "validate only; print the resolved specs")
	root := fs.String("root", ".", "repository root that owns plugins/")
	if err := fs.Parse(args); err != nil {
		return err
	}

	specs, err := parseSpecs(in)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		fmt.Fprintln(errOut, "pluginresolve: 指定されたプラグインはありません")
		return nil
	}

	for _, s := range specs {
		if *dryRun {
			fmt.Fprintf(out, "%s %s %s\n", s.name, s.url, s.ref)
			continue
		}
		if err := clone(*root, s); err != nil {
			return err
		}
		fmt.Fprintf(errOut, "pluginresolve: %s <- %s@%s\n", s.name, s.url, s.ref)
	}
	return nil
}

// parseSpecs reads plugin lines and validates every field.
//
// Validation is not cosmetic: the fields are passed to git as arguments, so a
// value starting with "-" would be taken as an option (`--upload-pack=...` は
// 任意コマンドの実行になる)。先頭を英数字に限ることでその経路を塞ぐ。
func parseSpecs(r io.Reader) ([]spec, error) {
	var specs []spec
	seen := map[string]bool{}

	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		// **コメントの除去は field 分割の後に行う。** 先に行内の `#` 以降を
		// 落とすと、`#` を含む URL や ref が黙って別のものに化け、そのあと
		// 「3 つが要ります」という原因から遠いエラーになる。
		fields := strings.Fields(sc.Text())
		for i, f := range fields {
			if strings.HasPrefix(f, "#") {
				fields = fields[:i]
				break
			}
		}
		if len(fields) == 0 {
			continue
		}

		if len(fields) != 3 {
			return nil, fmt.Errorf("%d 行目: `<name> <url> <ref>` の 3 つが要ります (%d 個でした): %q", line, len(fields), strings.Join(fields, " "))
		}
		s := spec{name: fields[0], url: fields[1], ref: fields[2]}

		if !nameRe.MatchString(s.name) {
			return nil, fmt.Errorf("%d 行目: name %q が不正です (小文字英数字で始まり、英数字 _ - のみ)", line, s.name)
		}
		if seen[s.name] {
			return nil, fmt.Errorf("%d 行目: name %q が重複しています", line, s.name)
		}
		seen[s.name] = true

		if err := validateURL(s.url); err != nil {
			return nil, fmt.Errorf("%d 行目: %w", line, err)
		}
		// ref は必須。既定ブランチへ落とす形は作らない — 作者のアカウントが
		// 侵害されたとき、次のビルドで任意のコードが入る (docs/plugins/operating.md
		// が「特定バージョンを名指しで含める」ことで安全性を担保すると宣言している)。
		if !refRe.MatchString(s.ref) {
			return nil, fmt.Errorf("%d 行目: ref %q が不正です (英数字で始まり、英数字 . _ - / のみ)", line, s.ref)
		}
		if strings.Contains(s.ref, "..") {
			return nil, fmt.Errorf("%d 行目: ref %q に .. を含められません", line, s.ref)
		}

		specs = append(specs, s)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return specs, nil
}

// validateURL rejects anything that is not a plain https URL.
//
// https 以外を受けないのは、ssh (`git@`) が CI に秘密鍵を置くことを要求し、
// file:// や ext:: が runner のローカル資産やコマンド実行に届くため。private
// repository は workflow 側の `url.<...>.insteadOf` で token を差し込む。
func validateURL(u string) error {
	const scheme = "https://"
	if !strings.HasPrefix(u, scheme) {
		return fmt.Errorf("url %q が不正です (https:// で始まる必要があります)", u)
	}

	// **印字可能な ASCII だけ通す。** バイト単位で見るので非 ASCII と不正な
	// UTF-8 の両方が落ちる。ここを緩めると、レビュー画面では github.com に
	// 見えるのに別ホストから取る URL を通してしまう — キリル文字の homograph、
	// ゼロ幅スペース、RTL override はどれも目視で区別できない。取ってきた
	// プラグインはサーバーと同じ権限で動くので、選択の明示性が唯一の防壁になる。
	for _, b := range []byte(u) {
		if b < 0x21 || b > 0x7e {
			return fmt.Errorf("url %q に空白・制御文字・非 ASCII が含まれています", u)
		}
	}

	host := strings.TrimPrefix(u, scheme)
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return fmt.Errorf("url %q にホストがありません", u)
	}
	// **userinfo を弾く。** `https://user:token@host/` を許すと、token が
	// この行の出力にそのまま載る。`plugins` は普通の input なので GitHub の
	// secret マスクが効かず、Actions のログに平文で残る。private repository は
	// workflow 側の url.<...>.insteadOf で差し込む。
	if strings.ContainsRune(host, '@') {
		return fmt.Errorf("url %q に userinfo (user:pass@) を含められません", u)
	}
	return nil
}

// clone materialises one plugin at the requested ref.
//
// `git clone --branch` は tag と branch しか受けないので、commit SHA でも
// 固定できるよう fetch + checkout に分けてある。
func clone(root string, s spec) error {
	dir := filepath.Join(root, "plugins", s.name)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("plugins/%s は既に存在します", s.name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// `--` は option 終端の明示。**fetch 側だけがテストで守られている**
	// (`TestClone_RefIsNotTreatedAsGitOption`) — `remote add` 側は url が
	// `-` で始まらない限り挙動が変わらないので、外しても落ちる入力を
	// parseSpecs 経由でも直接呼び出しでも構成できない。対称性のために残す。
	steps := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", "--", s.url},
		{"fetch", "--quiet", "--depth", "1", "origin", "--", s.ref},
		{"checkout", "--quiet", "FETCH_HEAD"},
	}
	for _, args := range steps {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			// 中途半端なディレクトリを残さない。残すと次の実行が
			// 「既に存在します」で落ち、原因から遠い症状になる。
			os.RemoveAll(dir)
			return fmt.Errorf("git %s (%s): %w", args[0], s.name, err)
		}
	}
	// clone した .git は image に要らない (.dockerignore は plugins/*/.git を
	// 落とさない)。残すとビルドコンテキストが膨らむだけ。
	return os.RemoveAll(filepath.Join(dir, ".git"))
}
