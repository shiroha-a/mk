package plugin

import (
	"context"
	"encoding/json"
	"fmt"
)

// API calls mk-go's own REST endpoints from inside the process.
//
// # なぜ独自の抽象を作らないか
//
// mk-go の REST API は Misskey 互換の維持が最優先方針であるため、既に安定した
// 第三者向け契約になっている。これを再利用すると、
//
//   - 新しい API 設計がほぼ不要で、Misskey の API ドキュメントがそのまま使える
//   - **可視性判定・権限・レート制限が同じコード経路で自動的に効く**
//
// リポジトリを直接触らせるとこの 3 つを全部迂回できてしまう (例えばノートの
// 可視性判定はアプリケーション側にあり DB には無いので、素直に SELECT すると
// 非公開ノートが混ざる)。
type API interface {
	// Anonymous calls as an unauthenticated client. 公開データのみ見える。
	Anonymous() Caller

	// AsUser calls as the given local user, exactly as if that user had made
	// the request: 可視性もレート制限もその利用者のものが適用される。
	//
	// 管理操作が必要なら管理者の ID を渡す。**「すべてを迂回する」経路は
	// 用意していない** — 何が起きるか定義しづらく、事故ったときに追えない。
	// 必要になった時点で、具体的な用途とともに追加を検討する。
	AsUser(userID string) Caller
}

// Caller performs one API call.
type Caller interface {
	// Call invokes endpoint (e.g. "notes/show") with params and returns the
	// raw JSON response.
	//
	// 生の JSON を返すのは、レスポンスの型を mk-go 側で固定しないため。
	// Misskey の応答は upstream 追従で項目が増減するので、構造体で受けると
	// そちらが契約になってしまう。
	Call(ctx context.Context, endpoint string, params any) (json.RawMessage, error)
}

// APIError is returned when an endpoint responds with a non-2xx status.
type APIError struct {
	// Endpoint is the called endpoint, e.g. "notes/show".
	Endpoint string
	// Status is the HTTP status code.
	Status int
	// Body is the raw error response, useful for reading Misskey's error code.
	Body json.RawMessage
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("plugin: API %s が status %d を返しました: %s", e.Endpoint, e.Status, e.Body)
}
