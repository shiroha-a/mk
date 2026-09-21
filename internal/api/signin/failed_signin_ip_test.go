package signin_test

import (
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 失敗したサインインの IP を `user_ip` に記録しないことを固定する (#3135)。
//
// **これは関連アカウント検索 (#3105) の前提。** あちらの関連度は `user_ip` の
// 観測だけを見るので、失敗ログインの IP がここに入ると**第三者が他人の関連候補を
// 作れてしまう** — 攻撃者が対象アカウントの ID で自分の IP から失敗を繰り返せば、
// その IP が対象の「使用した IP」として記録され、攻撃者自身のアカウントが候補に
// 並ぶ。#3066 が完了条件に挙げているのはこの攻撃面のため。
//
// **いまは構造的にしか守られていない。** `user_ip` へ書くのは `iplog.Service.Record`
// だけで、それを呼ぶのは `RecordSuccessfulSignin` と認証済みリクエスト専用の
// `middleware.RecordClientIP` の 2 箇所。失敗経路 `fail()` は `signins` に
// `success: false` を書くだけで recorder に触らない。**その形を固定する。**
//
// **成功側も同じテストで見る。** 失敗側だけだと、recorder を丸ごと配線し忘れた
// 状態 (= 何も記録されない) が緑で通ってしまう。
func TestSignin_FailedAttemptsDoNotRecordIP(t *testing.T) {
	h, repo := newTestHandler(t)
	user := createTestUser(repo, "testuser", "password123")

	var mu sync.Mutex
	var recorded []string
	h.SetIPRecorder(&mockIPRecorder{fn: func(userID, ip string) {
		mu.Lock()
		defer mu.Unlock()
		recorded = append(recorded, userID)
	}})
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), recorded...)
	}

	t.Run("wrong password", func(t *testing.T) {
		// 利用者は実在するので `fail()` は signins に失敗を書く。**それでも
		// user_ip には触らない**、が守りたい性質。
		rec := doPost(h.Signin, `{"username":"testuser","password":"wrong"}`)
		require.Equal(t, http.StatusForbidden, rec.Code)
		assert.Empty(t, seen(), "失敗したサインインの IP を記録している")
	})

	t.Run("unknown user", func(t *testing.T) {
		rec := doPost(h.Signin, `{"username":"nosuchuser","password":"password123"}`)
		require.NotEqual(t, http.StatusOK, rec.Code)
		assert.Empty(t, seen(), "存在しない利用者のサインイン試行で IP を記録している")
	})

	t.Run("success records", func(t *testing.T) {
		rec := doPost(h.Signin, `{"username":"testuser","password":"password123"}`)
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, []string{user.ID}, seen(), "成功したサインインの IP が記録されていない")
	})
}
