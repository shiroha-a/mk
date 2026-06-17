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
