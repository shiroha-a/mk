package federation_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/activitypub/ld"

	corefederation "github.com/shiroha-a/mk/internal/core/federation"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLDSignatureVerifier_NoSignature_NoOp(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	v := corefederation.NewLDSignatureVerifier(repo)

	// signature field 無し → skip。HTTP Signature 経由でしか認証していない
	// 既存挙動の activity でも本 verifier は素通しすること (= 後方互換)。
	err := v.VerifyIfPresent([]byte(`{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type": "Note",
		"id": "https://example.com/notes/n1"
	}`))
	require.NoError(t, err)
}

func TestLDSignatureVerifier_ForbiddenDirective_Rejected(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	// pubkey は適当に登録しておく (= forbidden directive で先に落ちて pubkey
	// resolve まで到達しないことを確認したい)。
	v := corefederation.NewLDSignatureVerifier(repo)

	err := v.VerifyIfPresent([]byte(`{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type": "Note",
		"@graph": [{"type": "Note"}],
		"signature": {
			"type": "RsaSignature2017",
			"creator": "https://example.com/users/alice#main-key",
			"signatureValue": "AAAA"
		}
	}`))
	require.Error(t, err)
	// hardening の forbidden directive で reject される (= ld.ErrForbiddenDirective)。
	assert.Contains(t, err.Error(), "forbidden")
}

func TestLDSignatureVerifier_MissingCreator_Rejected(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	v := corefederation.NewLDSignatureVerifier(repo)

	err := v.VerifyIfPresent([]byte(`{
		"type": "Note",
		"signature": {
			"type": "RsaSignature2017",
			"signatureValue": "AAAA"
		}
	}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creator missing")
}

// signature field がオブジェクトでない (例: 文字列) 場合は reject。present=true
// を報告しつつ error を返す (VerifyAndCompact 経路)。
func TestLDSignatureVerifier_SignatureNotObject_Rejected(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	v := corefederation.NewLDSignatureVerifier(repo)

	verified, present, err := v.VerifyAndCompact([]byte(`{"type":"Note","signature":"not-an-object"}`))
	require.Error(t, err)
	assert.True(t, present, "signature field exists, so present must be true")
	assert.Empty(t, verified.Creator)
	assert.Nil(t, verified.Body)
	assert.Contains(t, err.Error(), "not an object")
}

func TestLDSignatureVerifier_UnknownCreator_Rejected(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	v := corefederation.NewLDSignatureVerifier(repo)

	err := v.VerifyIfPresent([]byte(`{
		"type": "Note",
		"signature": {
			"type": "RsaSignature2017",
			"creator": "https://example.com/users/unknown#main-key",
			"signatureValue": "AAAA"
		}
	}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "public key not found")
}

func TestLDSignatureVerifier_BadSignatureValueRejected(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	// 本物の RSA キーを生成して登録するが、signatureValue は invalid base64
	// なので verify は必ず失敗する shape。
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	keyID := "https://example.com/users/alice#main-key"
	repo.Keys["alice"] = &model.UserPublickey{
		UserID: "alice",
		KeyID:  keyID,
		KeyPEM: pubPEM,
	}

	v := corefederation.NewLDSignatureVerifier(repo)
	err = v.VerifyIfPresent([]byte(`{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type": "Note",
		"id": "https://example.com/notes/n1",
		"signature": {
			"type": "RsaSignature2017",
			"creator": "https://example.com/users/alice#main-key",
			"signatureValue": "AAAA"
		}
	}`))
	require.Error(t, err)
	// rsa.VerifyPKCS1v15 経由の verify mismatch。
	assert.NotContains(t, err.Error(), "public key not found",
		"public key は resolve できた状態で signature verify で fail することを確認")
}

func TestLDSignatureVerifier_NilRepo_NoOp(t *testing.T) {
	// 防御的: pubkeyRepo nil でも panic せず素通し (= verify 経路を skip)。
	v := corefederation.NewLDSignatureVerifier(nil)
	err := v.VerifyIfPresent([]byte(`{"type":"Note"}`))
	require.NoError(t, err)
}

// LDSignatureVerifier interface 互換性 (= inbox processor 経由で wire される
// shape) を最低限 confirm する。
func TestLDSignatureVerifier_InterfaceShape(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	v := corefederation.NewLDSignatureVerifier(repo)
	// メソッド存在 (compile time check)
	_ = v.VerifyIfPresent
	assert.NotNil(t, v, "verifier must be non-nil")
}

// #2106 N26: CheckForbiddenDirectivesIfPresent は signature 無しを素通しする。
func TestLDSignatureVerifier_CheckForbidden_NoSignature(t *testing.T) {
	v := corefederation.NewLDSignatureVerifier(testutil.NewMockUserPublickeyRepository())
	require.NoError(t, v.CheckForbiddenDirectivesIfPresent([]byte(`{"type":"Note","id":"https://example.com/n1"}`)))
}

// #2106 N26: forbidden directive は CheckForbiddenDirectivesIfPresent でも reject される。
func TestLDSignatureVerifier_CheckForbidden_ForbiddenDirective(t *testing.T) {
	v := corefederation.NewLDSignatureVerifier(testutil.NewMockUserPublickeyRepository())
	err := v.CheckForbiddenDirectivesIfPresent([]byte(`{
		"type":"Note","@graph":[{"type":"Note"}],
		"signature":{"type":"RsaSignature2017","creator":"https://example.com/users/alice#main-key","signatureValue":"AAAA"}
	}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "forbidden")
}

// #2106 N26: forbidden directive が無ければ、creator 鍵が未解決でも (= VerifyIfPresent なら
// verify 失敗で drop する状況でも) CheckForbiddenDirectivesIfPresent は nil を返す。
func TestLDSignatureVerifier_CheckForbidden_UnresolvableKeyStillPasses(t *testing.T) {
	v := corefederation.NewLDSignatureVerifier(testutil.NewMockUserPublickeyRepository())
	body := []byte(`{
		"@context":"https://www.w3.org/ns/activitystreams","type":"Note",
		"signature":{"type":"RsaSignature2017","creator":"https://example.com/users/alice#main-key","signatureValue":"AAAA"}
	}`)
	require.Error(t, v.VerifyIfPresent(body), "VerifyIfPresent は creator 鍵未解決で error")
	require.NoError(t, v.CheckForbiddenDirectivesIfPresent(body), "forbidden 無し + 鍵不要なので nil")
}

// #2106 N26: 不正 JSON は CheckForbiddenDirectivesIfPresent でも error。
func TestLDSignatureVerifier_CheckForbidden_BadJSON(t *testing.T) {
	v := corefederation.NewLDSignatureVerifier(testutil.NewMockUserPublickeyRepository())
	require.Error(t, v.CheckForbiddenDirectivesIfPresent([]byte(`{not json`)))
}

// #2680: **正しい署名が通ることを確認する。**
//
// これまで本ファイルには test 関数が 12 本あったが、**署名検証が成功する経路を
// 通るものが 1 本も無かった** (拒否を確認するものが 9 本、nil を返すことを
// 確認するものが 3 本 — いずれも verify 本体に到達しないか、失敗を期待する)。
// そのため `loadDocument` が preload まで freeze で塞いで LD-Signature 検証が
// 常に失敗するようになっても、バグも「拒否」を返す以上テストは緑のままだった。
//
// verifier は `NewProcessor()` の空 cache のまま `Freeze()` してから verify する
// ので、本テストは production と同じ順序を通る。
func TestLDSignatureVerifier_ValidSignatureAccepted(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))
	privPEM := string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv),
	}))

	keyID := "https://example.com/users/alice#main-key"
	repo := testutil.NewMockUserPublickeyRepository()
	repo.Keys["alice"] = &model.UserPublickey{UserID: "alice", KeyID: keyID, KeyPEM: pubPEM}

	signed, err := ld.NewProcessor().SignRsaSignature2017(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"type":     "Note",
		"id":       "https://example.com/notes/n1",
		"content":  "hello",
	}, privPEM, keyID, time.Now())
	require.NoError(t, err)

	body, err := json.Marshal(signed)
	require.NoError(t, err)

	require.NoError(t, corefederation.NewLDSignatureVerifier(repo).VerifyIfPresent(body),
		"正しい LD-Signature が検証を通らない")
}

// #2680: **verifier が Freeze すること自体を固定する。**
//
// この PR は「freeze は維持したまま preload だけ通す」ものなので、freeze が
// 外れると主旨が失われる。ところが `proc.Freeze()` を消しても他のテストは
// すべて緑のままだった。context 問題に当たった人が Freeze() を消して緑にする、
// という直し方を塞ぐ。
//
// 現状 mk-go に remote fetch 経路は無いので即時の security 影響は無いが、
// fetch fallback を足したときに freeze が唯一の防壁になる。
func TestLDSignatureVerifier_NonPreloadedContextRejectedByFreeze(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	pubPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}))

	keyID := "https://example.com/users/alice#main-key"
	repo := testutil.NewMockUserPublickeyRepository()
	repo.Keys["alice"] = &model.UserPublickey{UserID: "alice", KeyID: keyID, KeyPEM: pubPEM}

	// preload に無い remote context を参照する activity。署名の正否によらず、
	// context を引けない時点で reject されなければならない。
	body := []byte(`{
		"@context": ["https://www.w3.org/ns/activitystreams", "https://litepub.social/litepub/context.jsonld"],
		"type": "Create",
		"id": "https://example.com/notes/n1/activity",
		"signature": {
			"type": "RsaSignature2017",
			"creator": "https://example.com/users/alice#main-key",
			"signatureValue": "AAAA"
		}
	}`)

	err = corefederation.NewLDSignatureVerifier(repo).VerifyIfPresent(body)
	require.Error(t, err, "preload 外の context は freeze で弾かれること")
	assert.True(t, errors.Is(err, ld.ErrCacheFrozen),
		"freeze による拒否であること (got %v)", err)
}

// #3121: 鍵が引けなかったのを「鍵が無い」に潰さない。潰すと呼び出し側が
// LD-Signature の検証失敗として activity を drop するので、DB 障害のあいだ
// 届いた転送 activity がまるごと失われる。
func TestLDSignatureVerifier_KeyLookupFailureIsLookupUnavailable(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	boom := errors.New("connection refused")
	repo.FindByKeyIDErr = boom
	v := corefederation.NewLDSignatureVerifier(repo)

	body := []byte(`{
		"type": "Note",
		"signature": {
			"type": "RsaSignature2017",
			"creator": "https://example.com/users/alice#main-key",
			"signatureValue": "AAAA"
		}
	}`)
	err := v.VerifyIfPresent(body)
	require.ErrorIs(t, err, corefederation.ErrLookupUnavailable, "DB 障害を「鍵が無い」に潰している")

	// **逆向き。** not-found は従来どおり「鍵が無い」として drop する。
	repo.FindByKeyIDErr = nil
	err = v.VerifyIfPresent(body)
	require.Error(t, err)
	assert.NotErrorIs(t, err, corefederation.ErrLookupUnavailable)
	assert.Contains(t, err.Error(), "public key not found")
}

// ldTestKey registers a fresh RSA key for alice and returns its private PEM.
func ldTestKey(t *testing.T, repo *testutil.MockUserPublickeyRepository) (keyID, privPEM string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	keyID = "https://example.com/users/alice#main-key"
	repo.Keys["alice"] = &model.UserPublickey{
		UserID: "alice", KeyID: keyID,
		KeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})),
	}
	privPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}))
	return keyID, privPEM
}

// **署名が覆っていないキーは compact 後の Body に短い名前で残らない。**
// Mastodon の context は `_misskey_content` を定義しないので、AS2 の
// `@vocab: "_:"` で blank node IRI になり署名に含まれない。署名は通るが、
// Body の object に `_misskey_content` キーがあってはならない。
func TestLDSignatureVerifier_CompactDropsUnsignedKeys(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	keyID, privPEM := ldTestKey(t, repo)

	signed, err := ld.NewProcessor().SignRsaSignature2017(map[string]any{
		"@context": []any{"https://www.w3.org/ns/activitystreams", map[string]any{"sensitive": "as:sensitive"}},
		"id":       "https://example.com/notes/n1/activity",
		"type":     "Create",
		"actor":    "https://example.com/users/alice",
		"to":       []any{"https://www.w3.org/ns/activitystreams#Public"},
		"object": map[string]any{
			"id":        "https://example.com/notes/n1",
			"type":      "Note",
			"content":   "<p>signed</p>",
			"sensitive": false,
		},
	}, privPEM, keyID, time.Now())
	require.NoError(t, err)
	signed["object"].(map[string]any)["_misskey_content"] = "forged"
	signed["object"].(map[string]any)["quoteUrl"] = "https://evil.example/notes/x"
	body, err := json.Marshal(signed)
	require.NoError(t, err)

	verified, present, err := corefederation.NewLDSignatureVerifier(repo).VerifyAndCompact(body)
	require.NoError(t, err, "署名外のキーを足しても LD-Signature 自体は通る (だから compact が要る)")
	require.True(t, present)
	assert.Equal(t, keyID, verified.Creator)

	var got map[string]any
	require.NoError(t, json.Unmarshal(verified.Body, &got))
	obj, ok := got["object"].(map[string]any)
	require.True(t, ok, "object が残っていない: %s", verified.Body)
	assert.NotContains(t, obj, "_misskey_content", "署名外の _misskey_content が残っている")
	assert.NotContains(t, obj, "quoteUrl", "署名外の quoteUrl が残っている")
	assert.Equal(t, "<p>signed</p>", obj["content"])
	assert.Equal(t, false, obj["sensitive"])
	assert.Equal(t, "as:Public", got["to"], "upstream と同じ compact 形になること")
	assert.Equal(t, "https://example.com/users/alice", got["actor"])
	sig, ok := got["signature"].(map[string]any)
	require.True(t, ok, "signature が付け直されていない")
	assert.Equal(t, keyID, sig["creator"])
}

// 署名した語彙 (context で定義された `_misskey_content`) は compact 後も残る。
func TestLDSignatureVerifier_CompactKeepsSignedExtensionKeys(t *testing.T) {
	repo := testutil.NewMockUserPublickeyRepository()
	keyID, privPEM := ldTestKey(t, repo)

	signed, err := ld.NewProcessor().SignRsaSignature2017(map[string]any{
		"@context": []any{
			"https://www.w3.org/ns/activitystreams",
			map[string]any{"misskey": "https://misskey-hub.net/ns#", "_misskey_content": "misskey:_misskey_content"},
		},
		"id":               "https://example.com/notes/n1",
		"type":             "Note",
		"_misskey_content": "signed **mfm**",
	}, privPEM, keyID, time.Now())
	require.NoError(t, err)
	body, err := json.Marshal(signed)
	require.NoError(t, err)

	verified, _, err := corefederation.NewLDSignatureVerifier(repo).VerifyAndCompact(body)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(verified.Body, &got))
	assert.Equal(t, "signed **mfm**", got["_misskey_content"])
}
