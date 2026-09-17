package admin

import (
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/server/middleware"
)

// credentialTakeoverDenied reports whether the caller must not be allowed to
// reset the target's sign-in credentials.
//
// **`admin/reset-password` と `admin/unset-mfa` は「乗っ取れる操作」(#3037)。**
// 前者は新しいパスワードを応答に載せて返し、後者は 2FA を外す。2 つを続けて
// 叩くと、対象アカウントとしてサインインできる状態が完成する。だから
// 「誰を対象にできるか」は、その先の権限で決めなければならない。
//
// 塞ぐのは 3 つ:
//
//   - **system アカウント** (`<kind>.actor`。host が nil で username に `.` を
//     含む) と root。連合の身元そのもので、人がサインインする前提が無い。
//     upstream はこの 2 つを削除から守るのに、資格情報のリセットからは
//     守っていない
//   - **管理者** (実行者本人を除く)。upstream 2026.7.0 が入れた保護で、
//     mk-go も既に持っていた
//   - **他のモデレーター** (実行者が管理者でない場合)。モデレーター同士の
//     相互乗っ取りを止める。モデレーターは凍結・削除・ロール付与といった
//     不可逆な操作を持つので、「同格だから触れてよい」にはならない
//
// **実行者が管理者ならモデレーターには触れる。** インシデント対応で
// モデレーターのアカウントを復旧する経路は残す。
func (h *Handler) credentialTakeoverDenied(c echo.Context, target *model.User) bool {
	if target == nil {
		return false
	}
	// system アカウント / root。**実行者が誰でも塞ぐ** — 人がサインインする
	// 前提の無いアカウントに、サインインできる資格情報を作らない。
	if h.isProtectedAccount(target.ID) || isSystemAccountUser(target) {
		return true
	}
	if h.roleService == nil {
		return false
	}
	me := middleware.GetUser(c)
	if me != nil && me.ID == target.ID {
		// 自分自身のリセットは従来どおり通す。
		return false
	}
	if h.roleService.IsAdministrator(target.ID) {
		return true
	}
	if h.roleService.IsModerator(target.ID) {
		// 実行者が管理者なら触れる (インシデント対応の経路)。
		return me == nil || !h.roleService.IsAdministrator(me.ID)
	}
	return false
}

// isSystemAccountUser reports whether the row is a local system account.
//
// **`isProtectedAccount` と二重に見る。** あちらは userID から引き直すので、
// DB が落ちていると false を返す (`FindByID` の失敗を「保護対象ではない」と
// 扱う)。こちらは呼び出し側が既に引いた行をそのまま見るので、その窓で
// 素通りしない。
func isSystemAccountUser(u *model.User) bool {
	return u != nil && u.Host == nil && strings.Contains(u.Username, ".")
}
