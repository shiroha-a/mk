package emojiapplication

import (
	"context"

	"github.com/shiroha-a/mk/internal/core/notification"
	"github.com/shiroha-a/mk/internal/model"
)

// InAppNotifier is the subset of the notification service this package needs.
type InAppNotifier interface {
	Create(ctx context.Context, in notification.CreateInput) (*notification.Notification, error)
}

// NewResultNotifier adapts the notification service to ResultNotifier.
//
// **結果そのものは通知に載せない。** 載せるのは applicationId だけで、承認か
// 却下か・却下の理由は読み出し時に申請の行から引き直す (#2868 の abuseReport と
// 同じ形)。理由を通知へ複製すると、申請を消しても文面が Redis に残り続ける。
func NewResultNotifier(n InAppNotifier) ResultNotifier {
	if n == nil {
		return nil
	}
	return &resultNotifier{inner: n}
}

type resultNotifier struct {
	inner InAppNotifier
}

func (r *resultNotifier) NotifyEmojiApplicationProcessed(ctx context.Context, app *model.EmojiApplication) error {
	if app == nil || app.ProcessedByID == nil {
		return nil
	}
	// **NotifierID を渡さない。** 通知サービスは notifier == notifiee を
	// ErrSelfNotification で弾くので、渡すと**自分で自分の申請を処理したときに
	// 通知が出なくなる**。abuseReport はそれで正しい (自分が出した通報の状態は
	// 本人が知っている) が、申請の結果は**相手が決めたこと**で、承認か却下かは
	// 受け取る価値がある。1 人運用のサーバーでは全件が自己処理になるので、
	// 弾くと「機能が動いていない」ように見える。
	//
	// 誰が処理したかは元々通知に出していない (申請者に見せると個人への抗議に
	// 繋がりやすい) ので、落としても失うものが無い。監査は emoji_application の
	// processedById と moderation log が持つ。
	_, err := r.inner.Create(ctx, notification.CreateInput{
		NotifieeID: app.UserID,
		Type:       notification.TypeEmojiApplicationProcessed,
		Extra: map[string]any{
			"applicationId": app.ID,
		},
	})
	return err
}
