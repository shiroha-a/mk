package federation

import "testing"

// #1820: assertResponseHostMatches は upstream assertActivityMatchesUrl の
// hard requirement (id host == final URL host, id 必須, https downgrade 拒否) を
// 表す。finalURL が空 (最終 URL 不明 = テストダブル) のときは skip する。
func TestAssertResponseHostMatches(t *testing.T) {
	cases := []struct {
		name     string
		finalURL string
		objectID string
		wantErr  bool
	}{
		{"host match", "https://remote.example/users/alice", "https://remote.example/users/alice", false},
		{"host match different path", "https://remote.example/x", "https://remote.example/users/alice", false},
		{"host mismatch (spoof)", "https://evil.example/users/alice", "https://remote.example/users/alice", true},
		{"id missing", "https://remote.example/users/alice", "", true},
		{"https downgrade", "https://remote.example/users/alice", "http://remote.example/users/alice", true},
		{"http final allows http id (no downgrade)", "http://remote.example/x", "http://remote.example/users/alice", false},
		{"empty finalURL skips check", "", "https://remote.example/users/alice", false},
		{"case-insensitive host", "https://Remote.Example/x", "https://remote.example/users/alice", false},
		// #1820 review: upstream URL.host は default port を除去するので :443 と無印は一致。
		{"default https port equivalent", "https://remote.example:443/x", "https://remote.example/users/alice", false},
		{"default http port equivalent", "http://remote.example:80/x", "http://remote.example/users/alice", false},
		{"non-default port mismatch", "https://remote.example:8443/x", "https://remote.example/users/alice", true},
		// #1820 review: 先頭 www. は synonymous subdomain として除去して一致扱い。
		{"www synonymous subdomain", "https://www.remote.example/x", "https://remote.example/users/alice", false},
		{"www both sides", "https://www.remote.example/x", "https://www.remote.example/users/alice", false},
		// #1850: Unicode IDN と punycode の mixed-form は idna 正規化で一致扱い。
		{"IDN unicode final vs punycode id", "https://bücher.example/x", "https://xn--bcher-kva.example/users/a", false},
		{"IDN punycode final vs unicode id", "https://xn--bcher-kva.example/x", "https://bücher.example/users/a", false},
		{"IDN different domain still mismatch", "https://bücher.example/x", "https://xn--nxasmq6b.example/users/a", true},
		// #1850 review: ideographic dot (U+3002) は `.` に畳まれない = victim.example へ
		// なりすませない (spoofing regression guard)。Go idna は Node と異なり安全側。
		{"ideographic dot does not collapse to victim host", "https://victim.example/x", "https://victim.example。evil.example/users/a", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := assertResponseHostMatches(tc.finalURL, tc.objectID)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("expected nil, got %v", err)
			}
		})
	}
}

// #1850: punyHost は idna.ToASCII(lowercase) で Unicode IDN と punycode を同一
// 正規形にし、id↔attributedTo / id↔final-host 比較の mixed-form 誤 reject を防ぐ。
func TestPunyHost(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"bücher.example", "xn--bcher-kva.example"},
		{"xn--bcher-kva.example", "xn--bcher-kva.example"},
		{"Bücher.Example", "xn--bcher-kva.example"}, // 大文字も正規化
		{"REMOTE.EXAMPLE", "remote.example"},        // ASCII は小文字化のみ
		{"remote.example", "remote.example"},        // 既に正規形
	}
	for _, tc := range cases {
		if got := punyHost(tc.in); got != tc.want {
			t.Errorf("punyHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// Unicode と punycode が同一正規形に畳まれる (= 比較で一致する) こと。
	if punyHost("bücher.example") != punyHost("xn--bcher-kva.example") {
		t.Errorf("Unicode と punycode の同一 host が一致しない")
	}
}
