package federation

import (
	"errors"

	"github.com/shiroha-a/mk/internal/activitypub"
	corereaction "github.com/shiroha-a/mk/internal/core/reaction"
)

// isPermanentResolveError reports whether err represents a permanent failure
// during remote ActivityPub object / actor resolution. callers in this
// package use it to decide between "best-effort skip" (= log warn and return
// nil, swallowing the activity) and "transient failure" (= return err to put
// the activity back on the retry queue).
//
// permanent と分類する error:
//
//   - HTTP 4xx (401 / 403 / 404 / 410) — `*activitypub.StatusError`
//     mk-go が follower で無い follower-only note / 削除済 / authorized
//     fetch 拒否 のいずれも、retry しても解消しない構造的失敗。
//   - `ErrInvalidActor` / `ErrInvalidNote` — 取得は成功したが対側が返した
//     JSON が parse 不能。schema 不正で retry でも直らない。
//   - `corereaction.ErrNoteNotVisible` — note 解決後の visibility 違反。
//     mk-go の policy 上 reaction を作れないので、activity 全体は ack
//     して終わる方が筋。
//
// transient と分類する (false 返す) error: 5xx / network error / timeout
// 等、対側の一過性障害。retry サイクルに乗せる。`nil` も false を返す
// (= 「成功」を「permanent fail」と誤判定しないため。
func isPermanentResolveError(err error) bool {
	if err == nil {
		return false
	}
	var statusErr *activitypub.StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case 401, 403, 404, 410:
			return true
		}
		return false
	}
	if errors.Is(err, ErrInvalidActor) || errors.Is(err, ErrInvalidNote) {
		return true
	}
	if errors.Is(err, corereaction.ErrNoteNotVisible) {
		return true
	}
	return false
}
