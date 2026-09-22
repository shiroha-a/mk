package l10n

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSignupConfirm(t *testing.T) {
	subject, lead, link := SignupConfirm("ja", "てすと鯖")
	assert.Equal(t, "アカウントの確認", subject)
	assert.Contains(t, lead, "てすと鯖")
	assert.Equal(t, "登録を完了", link)

	subject, lead, link = SignupConfirm("en", "Test")
	assert.Equal(t, "Confirm your account", subject)
	assert.Contains(t, lead, "Welcome to Test")
	assert.Equal(t, "Complete signup", link)
}

func TestPasswordReset(t *testing.T) {
	subject, lead, link := PasswordReset("ja")
	assert.Equal(t, "パスワードのリセット", subject)
	assert.Contains(t, lead, "以下のリンク")
	assert.Equal(t, "パスワードをリセット", link)

	subject, _, link = PasswordReset("en")
	assert.Equal(t, "Password reset", subject)
	assert.Equal(t, "Reset password", link)
}

func TestVerifyEmail(t *testing.T) {
	subject, lead, link := VerifyEmail("ja")
	assert.Equal(t, "メールアドレスの確認", subject)
	assert.Contains(t, lead, "以下のリンク")
	assert.Equal(t, "メールアドレスを確認", link)

	subject, _, link = VerifyEmail("en")
	assert.Equal(t, "Verify your email", subject)
	assert.Equal(t, "Verify email", link)
}

func TestNewLogin(t *testing.T) {
	subject, body := NewLogin("ja")
	assert.Equal(t, "ログインがありました", subject)
	assert.Contains(t, body, "新しいログインがありました")
	assert.NotContains(t, body, "There is a new login")

	subject, body = NewLogin("en")
	assert.Equal(t, "New login", subject)
	assert.Contains(t, body, "There is a new login")

	subject, body = NewLogin(LangBilingual)
	assert.Contains(t, subject, "New login")
	assert.Contains(t, subject, "ログインがありました")
	assert.Contains(t, body, "There is a new login")
	assert.Contains(t, body, "新しいログインがありました")
}

func TestEmailSettingsLabel(t *testing.T) {
	assert.Equal(t, "メール設定", EmailSettingsLabel("ja"))
	assert.Equal(t, "Email setting", EmailSettingsLabel("en"))
	assert.Equal(t, "Email setting", EmailSettingsLabel(LangBilingual))
}

func TestModeratorInactivityWarning(t *testing.T) {
	subjectJa, bodyJa := ModeratorInactivityWarning("ja", 0, 6)
	assert.Equal(t, "モデレーター不在の通知", subjectJa)
	assert.Contains(t, bodyJa, "6時間")

	_, bodyJa = ModeratorInactivityWarning("ja", 2, 48)
	assert.Contains(t, bodyJa, "2日間")

	subjectEn, bodyEn := ModeratorInactivityWarning("en", 0, 3)
	assert.Equal(t, "Moderator Inactivity Warning", subjectEn)
	assert.Contains(t, bodyEn, "3 hours")

	_, bodyEn = ModeratorInactivityWarning("en", 2, 48)
	assert.Contains(t, bodyEn, "2 days")
}

func TestModeratorInvitationOnlyChanged(t *testing.T) {
	subject, body := ModeratorInvitationOnlyChanged("ja", 7)
	assert.Equal(t, "招待制に変更されました", subject)
	assert.Contains(t, body, "7日間")

	subject, body = ModeratorInvitationOnlyChanged("en", 7)
	assert.Equal(t, "Change to Invitation-Only", subject)
	assert.Contains(t, body, "7 days")

	_, bodyBi := ModeratorInactivityWarning(LangBilingual, 0, 6)
	assert.Contains(t, bodyBi, "6 hours")
	assert.Contains(t, bodyBi, "6時間")

	subject, body = ModeratorInvitationOnlyChanged(LangBilingual, 7)
	assert.Contains(t, subject, "Invitation-Only")
	assert.Contains(t, subject, "招待制")
	assert.Contains(t, body, "7 days")
	assert.Contains(t, body, "7日間")
}
