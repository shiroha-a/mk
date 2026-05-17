package activitypub

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAddContext_Person(t *testing.T) {
	p := &Person{}
	AddContext(p)
	ctx, ok := p.Context.([]any)
	assert.True(t, ok)
	assert.Contains(t, ctx, ContextURL)
	assert.Contains(t, ctx, SecurityContextURL)
}

func TestAddContext_Variants(t *testing.T) {
	cases := []any{
		&Note{},
		&Create{},
		&Follow{},
		&Accept{},
		&Reject{},
		&Undo{},
		&Delete{},
		&Update{},
		&Like{},
		&Announce{},
	}
	for _, c := range cases {
		AddContext(c)
		// 全型で fullContext (AS + Security + MisskeyContext) が設定される
		var ctx any
		switch v := c.(type) {
		case *Note:
			ctx = v.Context
		case *Create:
			ctx = v.Context
		case *Follow:
			ctx = v.Context
		case *Accept:
			ctx = v.Context
		case *Reject:
			ctx = v.Context
		case *Undo:
			ctx = v.Context
		case *Delete:
			ctx = v.Context
		case *Update:
			ctx = v.Context
		case *Like:
			ctx = v.Context
		case *Announce:
			ctx = v.Context
		}
		arr, ok := ctx.([]any)
		assert.True(t, ok)
		assert.Contains(t, arr, ContextURL)
		assert.Contains(t, arr, SecurityContextURL)
	}
}

func TestAddContext_IndependentSlices(t *testing.T) {
	// 異なるオブジェクトが独立したcontextスライスを持つこと
	p := &Person{}
	n := &Note{}
	AddContext(p)
	AddContext(n)
	pCtx := p.Context.([]any)
	nCtx := n.Context.([]any)
	// appendしても互いに影響しない
	pCtx = append(pCtx, "extra")
	assert.Len(t, nCtx, 3)
}

func TestAddContext_NoOpForUnknown(t *testing.T) {
	// 引数の型が switch にない場合はno-op
	type other struct{}
	AddContext(&other{})
}

func TestPersonMarshalsCleanly(t *testing.T) {
	p := &Person{
		Object: Object{ID: "https://example.com/users/u1", Type: "Person"},
	}
	AddContext(p)
	b, err := json.Marshal(p)
	assert.NoError(t, err)
	assert.Contains(t, string(b), "Person")
	assert.Contains(t, string(b), ContextURL)
}

func TestNewMention(t *testing.T) {
	m := NewMention("https://example.com/users/u1", "@u1")
	assert.Equal(t, "Mention", m.Type)
	assert.Equal(t, "https://example.com/users/u1", m.Href)
	assert.Equal(t, "@u1", m.Name)

	// name が空でも factory は Type を必ず埋める
	m2 := NewMention("https://example.com/users/u2", "")
	assert.Equal(t, "Mention", m2.Type)
	assert.Empty(t, m2.Name)

	// JSON 出力時も type フィールドが出ていること
	b, err := json.Marshal(m)
	assert.NoError(t, err)
	assert.Contains(t, string(b), `"type":"Mention"`)
}

func TestIsValidActorType(t *testing.T) {
	cases := []struct {
		typ  string
		want bool
	}{
		{"Person", true},
		{"Service", true},
		{"Group", true},
		{"Organization", true},
		{"Application", true},
		{"Note", false},
		{"Tombstone", false},
		{"", false},
		{"person", false}, // case sensitive (TS と同じ)
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			assert.Equal(t, c.want, IsValidActorType(c.typ))
		})
	}
}

func TestIsBotActorType(t *testing.T) {
	cases := []struct {
		typ  string
		want bool
	}{
		{"Service", true},
		{"Application", true},
		{"Person", false},
		{"Group", false},
		{"Organization", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.typ, func(t *testing.T) {
			assert.Equal(t, c.want, IsBotActorType(c.typ))
		})
	}
}

// upstream Misskey #17275 (= 2026.5.0 fix / triage #1000): alsoKnownAs を
// array でも single string でも受け入れる。Mastodon 等が array で送ってくる
// 一方 Friendica など一部実装は single string で送ってくるため、両方を
// 受信できる必要がある。送信側は常に array (= []string と同じ shape) で emit。
func TestAPStringList_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want APStringList
	}{
		{"empty_input", ``, nil},
		{"null_value", `null`, nil},
		{"array_form", `["a","b"]`, APStringList{"a", "b"}},
		{"empty_array", `[]`, APStringList{}},
		{"single_string", `"https://other.example/users/alice"`, APStringList{"https://other.example/users/alice"}},
		{"leading_whitespace_array", "  \n\t[\"x\"]", APStringList{"x"}},
		{"leading_whitespace_string", "  \"y\"", APStringList{"y"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got APStringList
			err := got.UnmarshalJSON([]byte(tc.in))
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// APStringList が送信側でも array shape を維持することを実証する
// (commit message で「JSON 送出時は []string と同じ array shape で emit する」と
// 表明したコントラクトの test)。受信時 single string が re-export で
// spec 準拠の array に正規化されることもセットで確認する。
func TestAPStringList_MarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		in   APStringList
		want string
	}{
		{"two", APStringList{"a", "b"}, `["a","b"]`},
		{"single", APStringList{"x"}, `["x"]`},
		{"empty", APStringList{}, `[]`},
		{"nil", nil, `null`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.in)
			assert.NoError(t, err)
			assert.Equal(t, tc.want, string(b))
		})
	}
}

// single string で Unmarshal → Marshal すると array に正規化される roundtrip。
// 受信時の互換性 + 送信時の spec 厳守を 1 ケースで束ねて検証する。
func TestAPStringList_SingleStringNormalizedToArrayOnReexport(t *testing.T) {
	var l APStringList
	err := json.Unmarshal([]byte(`"https://other.example/users/a"`), &l)
	assert.NoError(t, err)
	out, err := json.Marshal(l)
	assert.NoError(t, err)
	assert.Equal(t, `["https://other.example/users/a"]`, string(out))
}

// Person.AlsoKnownAs を Mastodon 互換の array 形式 / Friendica 等の single
// string 形式どちらでも JSON Unmarshal が成功し、[]string として等価な値を
// 取れることを実 Person Unmarshal で end-to-end 検証する。
func TestPerson_AlsoKnownAs_BothForms(t *testing.T) {
	t.Run("array_form", func(t *testing.T) {
		body := []byte(`{"id":"https://example.test/users/alice","alsoKnownAs":["https://old.example/users/a","https://older.example/users/a"]}`)
		var p Person
		err := json.Unmarshal(body, &p)
		assert.NoError(t, err)
		assert.Equal(t, APStringList{"https://old.example/users/a", "https://older.example/users/a"}, p.AlsoKnownAs)
	})
	t.Run("single_string_form", func(t *testing.T) {
		body := []byte(`{"id":"https://example.test/users/alice","alsoKnownAs":"https://old.example/users/a"}`)
		var p Person
		err := json.Unmarshal(body, &p)
		assert.NoError(t, err)
		assert.Equal(t, APStringList{"https://old.example/users/a"}, p.AlsoKnownAs)
	})
	t.Run("absent_field", func(t *testing.T) {
		body := []byte(`{"id":"https://example.test/users/alice"}`)
		var p Person
		err := json.Unmarshal(body, &p)
		assert.NoError(t, err)
		assert.Nil(t, p.AlsoKnownAs)
	})
}

// PR #1110: AS2.0 spec で `icon` / `image` は単一 object または array を
// 許容する (#dfn-icon)。iceshrimp / 一部 Pleroma fork が array 形式で
// multi-resolution icon を expose しており、旧 mk-go は単一 object のみ
// 受理していたため avatar URL が取れず TL の @mention 横にアイコンが
// 表示されない bug があった (#1110)。Image.UnmarshalJSON で両形式を
// 吸収する upstream-compat 修正の regression guard。
func TestImage_UnmarshalJSON_SingleObject(t *testing.T) {
	var img Image
	err := json.Unmarshal([]byte(`{"type":"Image","url":"https://example.com/a.png"}`), &img)
	assert.NoError(t, err)
	assert.Equal(t, "Image", img.Type)
	assert.Equal(t, "https://example.com/a.png", img.URL)
}

func TestImage_UnmarshalJSON_Array_PicksFirstWithURL(t *testing.T) {
	var img Image
	data := []byte(`[
		{"type":"Image","url":""},
		{"type":"Image","url":"https://example.com/large.png"},
		{"type":"Image","url":"https://example.com/small.png"}
	]`)
	err := json.Unmarshal(data, &img)
	assert.NoError(t, err)
	assert.Equal(t, "https://example.com/large.png", img.URL,
		"upstream behavior: take first item with non-empty url")
}

func TestImage_UnmarshalJSON_Array_AllEmptyURLs(t *testing.T) {
	var img Image
	err := json.Unmarshal([]byte(`[{"type":"Image","url":""},{"type":"Image"}]`), &img)
	assert.NoError(t, err)
	assert.Equal(t, "", img.URL, "empty array of empty-url items leaves zero value")
}

func TestImage_UnmarshalJSON_EmptyArray(t *testing.T) {
	var img Image
	err := json.Unmarshal([]byte(`[]`), &img)
	assert.NoError(t, err)
	assert.Equal(t, "", img.URL)
	assert.Equal(t, "", img.Type)
}

func TestImage_UnmarshalJSON_Null(t *testing.T) {
	var img Image
	err := json.Unmarshal([]byte(`null`), &img)
	assert.NoError(t, err)
	assert.Equal(t, "", img.URL)
}

func TestImage_UnmarshalJSON_Malformed(t *testing.T) {
	var img Image
	// 文字列など object/array でない type は単一 object 経路で error を返す。
	err := json.Unmarshal([]byte(`"some-string"`), &img)
	assert.Error(t, err)
}

// 報告経路の end-to-end guard: iceshrimp 風 actor JSON (array 形式 icon /
// image) を Person に unmarshal して avatar/banner URL が正しく抽出される
// ことを確認。
func TestPerson_UnmarshalJSON_ArrayIconAndImage(t *testing.T) {
	body := []byte(`{
		"type": "Person",
		"id": "https://iceshrimp.example/users/alice",
		"inbox": "https://iceshrimp.example/inbox",
		"outbox": "https://iceshrimp.example/users/alice/outbox",
		"followers": "https://iceshrimp.example/users/alice/followers",
		"following": "https://iceshrimp.example/users/alice/following",
		"preferredUsername": "alice",
		"publicKey": {"id":"x","owner":"y","publicKeyPem":"z"},
		"icon": [
			{"type":"Image","url":"https://iceshrimp.example/avatar.png"}
		],
		"image": [
			{"type":"Image","url":"https://iceshrimp.example/banner.png"}
		]
	}`)
	var p Person
	err := json.Unmarshal(body, &p)
	assert.NoError(t, err)
	if assert.NotNil(t, p.Icon) {
		assert.Equal(t, "https://iceshrimp.example/avatar.png", p.Icon.URL)
	}
	if assert.NotNil(t, p.Image) {
		assert.Equal(t, "https://iceshrimp.example/banner.png", p.Image.URL)
	}
}
