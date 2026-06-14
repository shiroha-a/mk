package i

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/misc/id"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApps(t *testing.T) {
	h, _ := newExtraHandler(t)
	// accessTokenRepo 未配線時は空配列で graceful fallback
	rec := postExtra(h.Apps, `{}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "[]\n", rec.Body.String())
}

// /i/apps は本家 Misskey 互換で access_token + app の JOIN を返す。
// owner-app 経由 (token.appId 指定あり) と raw token (app 無し) の混在を
// テスト。本実装化は #587 で行った (旧実装は常に空配列を返す stub)。
func TestApps_ReturnsTokensWithApp(t *testing.T) {
	h, _ := newExtraHandler(t)
	tokens := testutil.NewMockAccessTokenRepository()
	idGen, _ := id.NewGenerator("aidx")

	// owner-app 経由の token: app メタを app テーブルから引く
	appID := "app1"
	tokens.SetApp(&model.App{
		ID:          appID,
		Name:        "Misskey IDE",
		Description: "OAuth2 demo app",
		Permission:  []string{"read:account"},
	})
	tApp := idGen.Generate(time.Now())
	tokens.Tokens["h1"] = &model.AccessToken{
		ID: tApp, Hash: "h1", UserID: stubUser.ID, AppID: &appID,
	}

	// raw token (app なし): token 自身の name / permission を使う
	tRaw := idGen.Generate(time.Now())
	rawName := "personal token"
	tokens.Tokens["h2"] = &model.AccessToken{
		ID: tRaw, Hash: "h2", UserID: stubUser.ID,
		Name:       &rawName,
		Permission: []string{"write:notes"},
	}

	// 別ユーザーの token は除外
	tokens.Tokens["h3"] = &model.AccessToken{ID: "other", Hash: "h3", UserID: "other"}

	h.SetAccessTokenRepo(tokens)

	rec := postExtra(h.Apps, `{}`, stubUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 2)

	// default sort = id ASC のため、ID 順に並ぶ。
	byID := map[string]map[string]any{}
	for _, e := range got {
		byID[e["id"].(string)] = e
	}
	// owner-app 経由: name は app.name、permission は app.permission
	app := byID[tApp]
	assert.Equal(t, "Misskey IDE", app["name"])
	perm, _ := app["permission"].([]any)
	require.Len(t, perm, 1)
	assert.Equal(t, "read:account", perm[0])
	assert.Equal(t, "OAuth2 demo app", app["description"])

	// raw token: name は token.name、permission は token.permission
	raw := byID[tRaw]
	assert.Equal(t, "personal token", raw["name"])
	perm2, _ := raw["permission"].([]any)
	require.Len(t, perm2, 1)
	assert.Equal(t, "write:notes", perm2[0])
}

// sort=+createdAt は新しい順 (ID DESC)。
func TestApps_SortByCreatedAtDesc(t *testing.T) {
	h, _ := newExtraHandler(t)
	tokens := testutil.NewMockAccessTokenRepository()
	idGen, _ := id.NewGenerator("aidx")
	t1 := idGen.Generate(time.Now())
	t2 := idGen.Generate(time.Now().Add(time.Second))
	tokens.Tokens["h1"] = &model.AccessToken{ID: t1, Hash: "h1", UserID: stubUser.ID}
	tokens.Tokens["h2"] = &model.AccessToken{ID: t2, Hash: "h2", UserID: stubUser.ID}
	h.SetAccessTokenRepo(tokens)

	rec := postExtra(h.Apps, `{"sort":"+createdAt"}`, stubUser)
	require.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 2)
	// 新しい順 = t2 が先
	assert.Equal(t, t2, got[0]["id"])
	assert.Equal(t, t1, got[1]["id"])
}

func TestAuthorizedApps(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusOK, postExtra(h.AuthorizedApps, `{}`, stubUser).Code)
}

func TestRevokeToken(t *testing.T) {
	h, _ := newExtraHandler(t)
	assert.Equal(t, http.StatusNoContent, postExtra(h.RevokeToken, `{}`, stubUser).Code)
}

// --- P4-6 (#166) / #1555: i/authorized-apps は App entity shape を返す ---

// authorized-apps は upstream の App entity ({id=app.id, name, callbackUrl,
// permission, isAuthorized}) を返す。appId NULL の raw/miauth token と他人の
// token は除外される (#1555)。
func TestAuthorizedApps_ReturnsAppEntities(t *testing.T) {
	h, _ := newExtraHandler(t)
	tokens := testutil.NewMockAccessTokenRepository()
	idGen, _ := id.NewGenerator("aidx")

	appID := "app1"
	cb := "https://app.example/callback"
	tokens.SetApp(&model.App{
		ID:          appID,
		Name:        "Misskey IDE",
		CallbackURL: &cb,
		Permission:  []string{"read:account", "write:notes"},
	})
	t1ID := idGen.Generate(time.Now())
	tokens.Tokens["h1"] = &model.AccessToken{ID: t1ID, Hash: "h1", UserID: stubUser.ID, AppID: &appID}
	// appId NULL の raw token は除外される。
	tokens.Tokens["h2"] = &model.AccessToken{ID: idGen.Generate(time.Now()), Hash: "h2", UserID: stubUser.ID}
	// 別ユーザーの token も除外。
	tokens.Tokens["h3"] = &model.AccessToken{ID: "other-token", Hash: "h3", UserID: "other", AppID: &appID}
	h.SetAccessTokenRepo(tokens)

	rec := postExtra(h.AuthorizedApps, `{}`, stubUser)
	assert.Equal(t, http.StatusOK, rec.Code)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	entry := got[0]
	// id は app.id (token.id ではない)。
	assert.Equal(t, appID, entry["id"])
	assert.Equal(t, "Misskey IDE", entry["name"])
	assert.Equal(t, cb, entry["callbackUrl"])
	assert.Equal(t, true, entry["isAuthorized"])
	perm, _ := entry["permission"].([]any)
	require.Len(t, perm, 2)
	assert.Equal(t, "read:account", perm[0])
	// token 専用 field (description/iconUrl/lastUsedAt) は含まない。
	_, hasDesc := entry["description"]
	assert.False(t, hasDesc)
}

// limit / offset / sort=asc が effect する。
func TestAuthorizedApps_Pagination(t *testing.T) {
	h, _ := newExtraHandler(t)
	tokens := testutil.NewMockAccessTokenRepository()
	idGen, _ := id.NewGenerator("aidx")
	a1, a2, a3 := "a1", "a2", "a3"
	for _, a := range []string{a1, a2, a3} {
		tokens.SetApp(&model.App{ID: a, Name: a})
	}
	t1 := idGen.Generate(time.Now())
	t2 := idGen.Generate(time.Now().Add(time.Second))
	t3 := idGen.Generate(time.Now().Add(2 * time.Second))
	tokens.Tokens["h1"] = &model.AccessToken{ID: t1, Hash: "h1", UserID: stubUser.ID, AppID: &a1}
	tokens.Tokens["h2"] = &model.AccessToken{ID: t2, Hash: "h2", UserID: stubUser.ID, AppID: &a2}
	tokens.Tokens["h3"] = &model.AccessToken{ID: t3, Hash: "h3", UserID: stubUser.ID, AppID: &a3}
	h.SetAccessTokenRepo(tokens)

	// limit=2, sort=asc (id 昇順) → t1(a1), t2(a2)。
	rec := postExtra(h.AuthorizedApps, `{"limit":2,"sort":"asc"}`, stubUser)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 2)
	assert.Equal(t, a1, got[0]["id"])
	assert.Equal(t, a2, got[1]["id"])

	// offset=2, sort=asc → t3(a3) のみ。
	rec = postExtra(h.AuthorizedApps, `{"sort":"asc","offset":2}`, stubUser)
	got = nil
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, a3, got[0]["id"])

	// default sort=desc → id 降順 (t3 が先頭)。
	rec = postExtra(h.AuthorizedApps, `{}`, stubUser)
	got = nil
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 3)
	assert.Equal(t, a3, got[0]["id"])
}

// --- P4-6 (#166): i/revoke-token ---

func TestRevokeToken_Owned(t *testing.T) {
	h, _ := newExtraHandler(t)
	tokens := testutil.NewMockAccessTokenRepository()
	tokens.Tokens["h1"] = &model.AccessToken{ID: "t1", Hash: "h1", UserID: stubUser.ID}
	h.SetAccessTokenRepo(tokens)

	rec := postExtra(h.RevokeToken, `{"tokenId":"t1"}`, stubUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	_, err := tokens.FindByID("t1")
	assert.Error(t, err)
}

// #1546: 他人所有の token 指定は upstream と同じく no-op (204)。403 を返すと
// token の存在 / 所有者が leak するため、削除せず 204 を返す。
func TestRevokeToken_ForeignTokenIsNoOp(t *testing.T) {
	h, _ := newExtraHandler(t)
	tokens := testutil.NewMockAccessTokenRepository()
	tokens.Tokens["h2"] = &model.AccessToken{ID: "t2", Hash: "h2", UserID: "other"}
	h.SetAccessTokenRepo(tokens)

	rec := postExtra(h.RevokeToken, `{"tokenId":"t2"}`, stubUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	// 他人の token は削除されない。
	_, err := tokens.FindByID("t2")
	assert.NoError(t, err)
}

func TestRevokeToken_UnknownTokenIsIdempotent(t *testing.T) {
	h, _ := newExtraHandler(t)
	h.SetAccessTokenRepo(testutil.NewMockAccessTokenRepository())
	rec := postExtra(h.RevokeToken, `{"tokenId":"ghost"}`, stubUser)
	// 存在しないなら 204 (idempotent)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRevokeToken_ByTokenHash(t *testing.T) {
	h, _ := newExtraHandler(t)
	repo := testutil.NewMockAccessTokenRepository()
	// SHA-256("mytoken") をハッシュとして登録 (map key = hash)
	hash := "1a17ea3569204d6c4114794ca73fa257457fc0612928c7bf024801659b77dba8"
	repo.Tokens[hash] = &model.AccessToken{ID: "at1", Hash: hash, UserID: stubUser.ID}
	h.SetAccessTokenRepo(repo)
	// 生tokenで失効できる
	rec := postExtra(h.RevokeToken, `{"token":"mytoken"}`, stubUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// TestRevokeToken_ByRawTokenForAppIssuedToken は #913 drift fix の
// regression guard: app/auth flow で発行された access token は
// hash = sha256(token + app.secret) で保存されているため、middleware と
// 同じく raw token 列の OR fallback でしか resolve できない。
// FindByHashOrToken に切替後は raw token で revoke できる。
func TestRevokeToken_ByRawTokenForAppIssuedToken(t *testing.T) {
	h, _ := newExtraHandler(t)
	repo := testutil.NewMockAccessTokenRepository()
	rawToken := "raw_app_xyz"
	// hash は sha256(token + secret) で raw token とは別値。SHA-256(rawToken)
	// だけでは hit しないので、token 列 fallback でのみ resolve できる shape。
	hashWithSecret := "hash_app_with_secret_value"
	repo.Tokens[hashWithSecret] = &model.AccessToken{
		ID:     "at_app_1",
		Token:  rawToken,
		Hash:   hashWithSecret,
		UserID: stubUser.ID,
	}
	h.SetAccessTokenRepo(repo)
	inv := &stubTokenInvalidator{}
	h.SetAuthInvalidator(inv)

	rec := postExtra(h.RevokeToken, `{"token":"`+rawToken+`"}`, stubUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	_, err := repo.FindByID("at_app_1")
	assert.Error(t, err, "token should be deleted after revoke")
	// auth middleware の cache に残ると revoke の効果が遅延する。
	// invalidator が raw token で呼ばれていることを確認 (= drop-in 互換)。
	require.Equal(t, []string{rawToken}, inv.calls)
}

// TestRevokeToken_InvalidatorByTokenIDPath は tokenId 経由の revoke でも
// cache invalidation が走ることを確認 (= miauth 経路 / app 経路の両方で
// stale cache を残さない)。
func TestRevokeToken_InvalidatorByTokenIDPath(t *testing.T) {
	h, _ := newExtraHandler(t)
	repo := testutil.NewMockAccessTokenRepository()
	rawToken := "raw_byid_xyz"
	repo.Tokens["h_byid"] = &model.AccessToken{
		ID:     "at_byid_1",
		Token:  rawToken,
		Hash:   "h_byid",
		UserID: stubUser.ID,
	}
	h.SetAccessTokenRepo(repo)
	inv := &stubTokenInvalidator{}
	h.SetAuthInvalidator(inv)

	rec := postExtra(h.RevokeToken, `{"tokenId":"at_byid_1"}`, stubUser)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, []string{rawToken}, inv.calls)
}

func TestRevokeToken_NoParams(t *testing.T) {
	h, _ := newExtraHandler(t)
	h.SetAccessTokenRepo(testutil.NewMockAccessTokenRepository())
	// tokenId も token も空 → 400
	assert.Equal(t, http.StatusBadRequest, postExtra(h.RevokeToken, `{}`, stubUser).Code)
}
