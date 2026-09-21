package pages

import (
	"net/http"
	"testing"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/stretchr/testify/require"
)

// enum 列が受け付ける値だけを通すこと。
//
// **通さないと 500 になる。** `page_visibility_enum` に無い値はそのまま
// INSERT へ行き、PostgreSQL がクエリごと落とす。認証済みなら誰でも起こせた。
func TestValidPageVisibility(t *testing.T) {
	t.Parallel()

	// 受け入れる値はリテラルで書く (定数を参照すると、集合を広げる変異と
	// 一緒に期待値まで動く)。
	for _, v := range []model.PageVisibility{"", "public", "followers", "specified"} {
		require.True(t, validPageVisibility(v), "%q は受け入れること", v)
	}
	for _, v := range []model.PageVisibility{"private", "home", "bogus", "PUBLIC", "public "} {
		require.False(t, validPageVisibility(v), "%q は拒否すること", v)
	}
}

// model 側の定数と取りこぼしが無いこと。
func TestValidPageVisibilityCoversModelConstants(t *testing.T) {
	t.Parallel()

	for _, v := range []model.PageVisibility{
		model.PageVisibilityPublic,
		model.PageVisibilityFollowers,
		model.PageVisibilitySpecified,
	} {
		require.True(t, validPageVisibility(v),
			"model の定数 %q を拒否している (列が受け付ける値を弾いてはいけない)", v)
	}
}

// ハンドラが検証を通していること。
//
// **これが無いと呼び出しを外す変異を検出できない。** 述語だけのテストは
// 述語が呼ばれているかを見ない。
func TestCreate_RejectsUnknownVisibility(t *testing.T) {
	h, _, _ := newHandler(t)
	c, rec := newReq(t, `{"title":"t","name":"alpha","content":[],"variables":[],"visibility":"private"}`)
	setUser(c, "alice")
	require.NoError(t, h.Create(c))
	require.Equal(t, http.StatusBadRequest, rec.Code,
		"enum 列に無い visibility は 400 で弾くこと (通すと INSERT が落ちて 500 になる)")
}

func TestCreate_AcceptsKnownVisibility(t *testing.T) {
	for _, v := range []string{"public", "followers", "specified"} {
		t.Run(v, func(t *testing.T) {
			h, _, _ := newHandler(t)
			c, rec := newReq(t, `{"title":"t","name":"alpha","content":[],"variables":[],"visibility":"`+v+`"}`)
			setUser(c, "alice")
			require.NoError(t, h.Create(c))
			require.Equal(t, http.StatusOK, rec.Code, "%s は通ること", v)
		})
	}
}

func TestUpdate_RejectsUnknownVisibility(t *testing.T) {
	h, repo, _ := newHandler(t)
	// **実在するページを用意する。** 用意しないと not-found が先に返り、
	// visibility の検証を外しても 400 のままでテストが空振りする
	// (初版で実際にそうなった)。
	repo.Pages["p1"] = &model.Page{ID: "p1", UserID: "alice", Name: "alpha", Title: "t"}
	c, rec := newReq(t, `{"pageId":"p1","visibility":"private"}`)
	setUser(c, "alice")
	require.NoError(t, h.Update(c))
	require.Equal(t, http.StatusBadRequest, rec.Code,
		"update 側も同じ検証を通すこと")

	// 対照: 正しい値なら通ること (400 が別の理由で出ていないことの確認)。
	h2, repo2, _ := newHandler(t)
	repo2.Pages["p1"] = &model.Page{ID: "p1", UserID: "alice", Name: "alpha", Title: "t"}
	c2, rec2 := newReq(t, `{"pageId":"p1","visibility":"followers"}`)
	setUser(c2, "alice")
	require.NoError(t, h2.Update(c2))
	require.Equal(t, http.StatusOK, rec2.Code, "正しい値は通ること")
}
