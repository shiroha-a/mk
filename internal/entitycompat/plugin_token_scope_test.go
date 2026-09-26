package entitycompat

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// buildWithPluginsWorkflow is the reusable workflow that clones private plugins.
const buildWithPluginsWorkflow = ".github/workflows/build-with-plugins.yml"

// TestPluginTokenIsScopedToCloneStep checks that the private-plugin token is
// written to the global git config only inside the step that clones, and is
// removed by an EXIT trap in that same step.
//
// **token を `~/.gitconfig` に残したまま後続へ進ませない。** 以前は token を
// 書く step と clone する step が分かれていて、書いたまま消していなかった。
// 後続の `go run ./tools/pluginbuild` や `pnpm install` (依存パッケージの
// lifecycle script が走る) からも読めたうえ、呼び出し元の token は他の
// private repository にも届く。
//
// 見るのは 3 点: (a) `PLUGIN_TOKEN` を受け取る step が 1 つだけ、(b) その
// step が clone (`tools/pluginresolve`) を実行する、(c) 同じ step で EXIT の
// trap が `--unset-all` する。trap にしてあるのは、clone が失敗して
// `set -e` で抜けたときも消すため。
func TestPluginTokenIsScopedToCloneStep(t *testing.T) {
	steps := workflowSteps(t, readRepoFile(t, buildWithPluginsWorkflow))

	var tokenSteps []workflowStep
	for _, s := range steps {
		if strings.Contains(s.Run, "PLUGIN_TOKEN") || stepEnvMentions(s, "plugin_token") {
			tokenSteps = append(tokenSteps, s)
		}
	}
	require.Lenf(t, tokenSteps, 1,
		"%s で plugin_token を扱う step は clone する 1 つだけにすること", buildWithPluginsWorkflow)

	s := tokenSteps[0]
	assert.Containsf(t, s.Run, "tools/pluginresolve",
		"step %q は token を設定するのに clone しない。設定と clone を同じ step に置くこと", s.Name)
	assert.Truef(t, hasUnsetTrap(s.Run),
		"step %q は token を git config に書くが、EXIT の trap で `git config --global --unset-all` していない", s.Name)
}

// workflowStep is the subset of a workflow step this gate reads.
type workflowStep struct {
	Name string            `yaml:"name"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

// workflowSteps returns every step of every job in a workflow file.
func workflowSteps(t *testing.T, body string) []workflowStep {
	t.Helper()
	var wf struct {
		Jobs map[string]struct {
			Steps []workflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(body), &wf))
	var steps []workflowStep
	for _, j := range wf.Jobs {
		steps = append(steps, j.Steps...)
	}
	require.NotEmpty(t, steps, "workflow から step を 1 つも読めない")
	return steps
}

// stepEnvMentions reports whether any env value of s references needle.
func stepEnvMentions(s workflowStep, needle string) bool {
	for _, v := range s.Env {
		if strings.Contains(v, needle) {
			return true
		}
	}
	return false
}

// hasUnsetTrap reports whether script installs an EXIT trap that removes a
// global git config entry.
func hasUnsetTrap(script string) bool {
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "trap ") &&
			strings.Contains(line, "git config --global --unset-all") &&
			strings.HasSuffix(line, " EXIT") {
			return true
		}
	}
	return false
}

// TestHasUnsetTrap pins the trap detector with synthetic scripts.
func TestHasUnsetTrap(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   bool
	}{
		{"exit trap", `trap 'git config --global --unset-all "$key"' EXIT`, true},
		{"indented", "  trap 'rc=$?; git config --global --unset-all \"$key\" || rc=1; exit \"$rc\"' EXIT\n", true},
		{"no trap", `git config --global --unset-all "$key"`, false},
		{"trap on ERR only", `trap 'git config --global --unset-all "$key"' ERR`, false},
		{"trap without unset", `trap 'echo done' EXIT`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, hasUnsetTrap(tt.script))
		})
	}
}
