package entitycompat

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// 配布する compose の全サービスにログの上限があること (#2828)。
//
// **既定の json-file はローテーションしない。** 指定を忘れたサービスが 1 つでも
// あると、そのコンテナのログだけが増え続けてディスクを埋める。運用者は自分で
// 気付いて足すしかないので、配布物の側で担保する。
//
// **サービスを足したときが危ない。** anchor (`*default-logging`) を書き忘れても
// compose は通るし、起動もするので、埋まるまで気付けない。
func TestComposeServicesHaveLogLimits(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{
		"docker-compose.yml",
		"docker-compose.image.yml",
		"compose.uds.yaml.example",
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			var doc struct {
				Services map[string]struct {
					Logging *struct {
						Driver  string            `yaml:"driver"`
						Options map[string]string `yaml:"options"`
					} `yaml:"logging"`
				} `yaml:"services"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			if len(doc.Services) == 0 {
				// **空なら落とす。** 書式が変わってサービスを 1 つも拾えなく
				// なると、検査していないのに緑になる。
				t.Fatalf("%s から service を 1 つも読めない (書式が変わった?)", name)
			}

			for svc, def := range doc.Services {
				if def.Logging == nil {
					t.Errorf("%s の service %q に logging が無い。"+
						"`logging: *default-logging` を足すこと (#2828)", name, svc)
					continue
				}
				if def.Logging.Options["max-size"] == "" {
					t.Errorf("%s の service %q に max-size が無い。"+
						"json-file はローテーションしないので、上限が要る", name, svc)
				}
				if def.Logging.Options["max-file"] == "" {
					t.Errorf("%s の service %q に max-file が無い", name, svc)
				}
			}
		})
	}
}
