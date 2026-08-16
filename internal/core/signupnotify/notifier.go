// Package signupnotify tells applicants the outcome of approval-based
// registration (#2554 / #2557).
//
// **通知でしかない。** 申請者は登録ページに戻れば自分で状態を確認できるので
// (#2556)、DM が届かなくても登録は完了できる。ここでの失敗が審査そのものを
// 巻き戻すことは無い。
package signupnotify

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/shiroha-a/mk/internal/core/note"
	"github.com/shiroha-a/mk/internal/model"
)

// BotProvider hands out the account the notification is sent from.
type BotProvider interface {
	Ensure() (*model.User, error)
}

// ActorResolver turns a remote actor URI into a local user row.
type ActorResolver interface {
	ResolveActor(uri string) (*model.User, error)
}

// NoteCreator writes the direct message.
type NoteCreator interface {
	Create(in note.CreateInput) (*model.Note, error)
}

// Notifier sends the approval / rejection direct messages.
type Notifier struct {
	bot       BotProvider
	resolver  ActorResolver
	notes     NoteCreator
	publicURL string
}

// NewNotifier constructs a Notifier.
func NewNotifier(bot BotProvider, resolver ActorResolver, notes NoteCreator, publicURL string) *Notifier {
	return &Notifier{bot: bot, resolver: resolver, notes: notes, publicURL: publicURL}
}

// NotifyApproved tells the applicant their application was approved.
//
// **別 goroutine で送る。** 連絡先の解決は相手サーバーへの HTTP を伴うので、
// 同期にすると相手が落ちているときに管理者の承認操作がタイムアウトまで固まり、
// 失敗したように見える。
func (n *Notifier) NotifyApproved(app *model.SignupApplication) {
	go n.sendLogged(app, true)
}

// NotifyRejected tells the applicant their application was rejected.
func (n *Notifier) NotifyRejected(app *model.SignupApplication) {
	go n.sendLogged(app, false)
}

func (n *Notifier) sendLogged(app *model.SignupApplication, approved bool) {
	if err := n.send(app, approved); err != nil {
		slog.Warn("signupnotify: failed to notify applicant",
			"applicationId", app.ID, "host", app.ContactHost, "approved", approved, "err", err)
	}
}

// send resolves the contact and posts the direct message.
func (n *Notifier) send(app *model.SignupApplication, approved bool) error {
	if n.bot == nil || n.resolver == nil || n.notes == nil {
		return errors.New("signupnotify: not wired")
	}
	if app == nil || app.ContactHost == "" || app.ContactRemoteID == "" {
		return errors.New("signupnotify: incomplete contact")
	}

	bot, err := n.bot.Ensure()
	if err != nil {
		return fmt.Errorf("signupnotify: bot account: %w", err)
	}

	// **actor URI は Misskey の規約から組み立てる** (`${url}/users/${id}`,
	// UserEntityService.genLocalUserUri)。連絡先は MiAuth を通した相手なので
	// Misskey 系であることが確定しており、`check` の応答は相手のローカル
	// ユーザーなので uri が null で、これ以外に辿る手がかりが無い。
	uri := "https://" + app.ContactHost + "/users/" + app.ContactRemoteID
	target, err := n.resolver.ResolveActor(uri)
	if err != nil {
		return fmt.Errorf("signupnotify: resolve contact: %w", err)
	}
	// 解決結果が本当にその host のユーザーかを確かめる。**別 host の行が返って
	// きたら送らない** — 誤った相手に承認の通知が飛ぶ。
	if target == nil || target.Host == nil || !strings.EqualFold(*target.Host, app.ContactHost) {
		return fmt.Errorf("signupnotify: resolved contact is not on %s", app.ContactHost)
	}

	text := n.text(app, target, approved)
	visibility := model.NoteVisibilitySpecified
	if _, err := n.notes.Create(note.CreateInput{
		User:           bot,
		Text:           &text,
		Visibility:     visibility,
		VisibleUserIDs: []string{target.ID},
	}); err != nil {
		return fmt.Errorf("signupnotify: create note: %w", err)
	}
	return nil
}

// text builds the message body.
//
// **秘密は載せない。** 登録 URL も招待コードも要らない — 申請者は登録ページで
// MiAuth を通せば続きから進める (#2556)。却下の理由も書かない (審査基準を
// 推測されるため)。
func (n *Notifier) text(app *model.SignupApplication, target *model.User, approved bool) string {
	acct := "@" + target.Username
	if target.Host != nil {
		acct += "@" + *target.Host
	}
	var b strings.Builder
	b.WriteString(acct)
	b.WriteString("\n")
	if approved {
		b.WriteString("登録の申請が承認されました。\n")
		if url := n.applicationURL(); url != "" {
			b.WriteString(url)
			b.WriteString(" を開き、同じアカウントで認証すると登録に進めます。\n")
		}
	} else {
		b.WriteString("登録の申請は承認されませんでした。\n")
	}
	return b.String()
}

func (n *Notifier) applicationURL() string {
	if n.publicURL == "" {
		return ""
	}
	return strings.TrimSuffix(n.publicURL, "/") + "/signup-application"
}
