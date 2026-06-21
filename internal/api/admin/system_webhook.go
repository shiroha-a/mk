package admin

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/lib/pq"

	"github.com/shiroha-a/mk/internal/api/apierr"
	"github.com/shiroha-a/mk/internal/core/moderationlog"
	"github.com/shiroha-a/mk/internal/entity"
	"github.com/shiroha-a/mk/internal/model"
)

// packSystemWebhook serializes a SystemWebhook into the upstream
// SystemWebhookEntityService.pack shape (SystemWebhookEntityService.ts:33-43):
// the same 9 fields the model carries, but with updatedAt / latestSentAt
// rendered via Date.toISOString() (.000Z) rather than the model's raw time.Time
// (which encoding/json would emit as RFC3339Nano). Returning the model directly
// diverged on those two timestamp fields (#1948-10).
func packSystemWebhook(w *model.SystemWebhook) map[string]any {
	// on は string[] 必須 (non-null)。nil pq.StringArray は null になるため
	// [] へ coalesce する (model 直返し時の pq.StringArray と同じ array 表現を維持)。
	on := []string(w.On)
	if on == nil {
		on = []string{}
	}
	return map[string]any{
		"id":           w.ID,
		"isActive":     w.IsActive,
		"updatedAt":    entity.ISOMillis(w.UpdatedAt),
		"latestSentAt": entity.ISOMillisPtr(w.LatestSentAt),
		"latestStatus": w.LatestStatus,
		"name":         w.Name,
		"on":           on,
		"url":          w.URL,
		"secret":       w.Secret,
	}
}

// systemWebhookEventTypes is the allow-list of system webhook `on` event types,
// mirroring upstream models/SystemWebhook.ts systemWebhookEventTypes (#1542)。
var systemWebhookEventTypes = map[string]bool{
	"abuseReport":                             true,
	"abuseReportResolved":                     true,
	"userCreated":                             true,
	"inactiveModeratorsWarning":               true,
	"inactiveModeratorsInvitationOnlyChanged": true,
}

// validSystemWebhookEvents reports whether every entry of `on` is a known event
// type. 空配列も valid (= required 検証は別途行う)。
func validSystemWebhookEvents(on []string) bool {
	for _, e := range on {
		if !systemWebhookEventTypes[e] {
			return false
		}
	}
	return true
}

// dummySystemWebhookBody builds a representative test payload per system event
// type, matching upstream WebhookTestService の type 別 dummy 相当 (#1542)。
func dummySystemWebhookBody(eventType string) map[string]any {
	dummyUser := map[string]any{
		"id":       "dummy-user-1",
		"username": "dummy",
		"host":     nil,
	}
	switch eventType {
	case "abuseReport", "abuseReportResolved":
		return map[string]any{
			"id":           "dummy-report-1",
			"comment":      "This is a dummy abuse report for testing purposes.",
			"resolved":     eventType == "abuseReportResolved",
			"reporterId":   "dummy-user-1",
			"targetUserId": "dummy-user-2",
			"reporter":     dummyUser,
			"targetUser":   dummyUser,
			"assignee":     nil,
		}
	case "userCreated":
		return dummyUser
	default:
		// inactiveModeratorsWarning / inactiveModeratorsInvitationOnlyChanged 等。
		return map[string]any{}
	}
}

// SystemWebhookCreate handles POST /api/admin/system-webhook/create.
func (h *Handler) SystemWebhookCreate(c echo.Context) error {
	if h.systemWebhookRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		Secret string `json:"secret"`
		// upstream create.ts required:[isActive,name,on,url]。欠落を 400 で弾く
		// ため On/IsActive はポインタで「未送出 (nil)」を判別する (#1542)。
		On       *[]string `json:"on"`
		IsActive *bool     `json:"isActive"`
	}
	if err := c.Bind(&req); err != nil || req.Name == "" || req.URL == "" || req.On == nil || req.IsActive == nil {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("Invalid parameters."))
	}
	// on は systemWebhookEventTypes enum のみ受理する (upstream create.ts)。
	if !validSystemWebhookEvents(*req.On) {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("on contains an unknown event type."))
	}
	sw := &model.SystemWebhook{
		ID:        h.idGen.Generate(time.Now()),
		Name:      req.Name,
		URL:       req.URL,
		Secret:    req.Secret,
		On:        *req.On,
		IsActive:  *req.IsActive,
		UpdatedAt: time.Now(),
	}
	if err := h.systemWebhookRepo.Create(sw); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	h.logModeration(c, moderationlog.LogCreateSystemWebhook, map[string]any{
		"systemWebhookId": sw.ID,
		"webhook":         sw,
	})
	return c.JSON(http.StatusOK, packSystemWebhook(sw))
}

// SystemWebhookDelete handles POST /api/admin/system-webhook/delete.
func (h *Handler) SystemWebhookDelete(c echo.Context) error {
	if h.systemWebhookRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		ID string `json:"id"`
	}
	_ = c.Bind(&req)
	if req.ID == "" {
		return c.NoContent(http.StatusNoContent)
	}
	snapshot, _ := h.systemWebhookRepo.FindByID(req.ID)
	if err := h.systemWebhookRepo.Delete(req.ID); err == nil && snapshot != nil {
		h.logModeration(c, moderationlog.LogDeleteSystemWebhook, map[string]any{
			"systemWebhookId": req.ID,
			"webhook":         snapshot,
		})
	}
	return c.NoContent(http.StatusNoContent)
}

// SystemWebhookList handles POST /api/admin/system-webhook/list.
func (h *Handler) SystemWebhookList(c echo.Context) error {
	if h.systemWebhookRepo == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	rows, err := h.systemWebhookRepo.List()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	if rows == nil {
		return c.JSON(http.StatusOK, []any{})
	}
	out := make([]map[string]any, 0, len(rows))
	for _, w := range rows {
		out = append(out, packSystemWebhook(w))
	}
	return c.JSON(http.StatusOK, out)
}

// SystemWebhookShow handles POST /api/admin/system-webhook/show.
func (h *Handler) SystemWebhookShow(c echo.Context) error {
	if h.systemWebhookRepo == nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	var req struct {
		ID string `json:"id"`
	}
	_ = c.Bind(&req)
	sw, err := h.systemWebhookRepo.FindByID(req.ID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	return c.JSON(http.StatusOK, packSystemWebhook(sw))
}

// SystemWebhookTest handles POST /api/admin/system-webhook/test.
//
// upstream test.ts: webhook 不在なら NO_SUCH_WEBHOOK、override.url/secret を受け、
// 配送 processor 経由で type 別 dummy payload を送る。mk-go も DispatchSystemTest
// で real delivery (queue) を経由させ、header/envelope を本配送と一致させる (#1542)。
func (h *Handler) SystemWebhookTest(c echo.Context) error {
	var req struct {
		WebhookID string `json:"webhookId"`
		Type      string `json:"type"`
		Override  *struct {
			URL    string `json:"url"`
			Secret string `json:"secret"`
		} `json:"override"`
	}
	if err := c.Bind(&req); err != nil || req.WebhookID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("webhookId is required."))
	}
	if h.systemWebhookRepo == nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	sw, err := h.systemWebhookRepo.FindByID(req.WebhookID)
	if err != nil {
		return c.JSON(http.StatusBadRequest, apierr.Error("NO_SUCH_WEBHOOK", "No such webhook.", "0c52149c-e913-18f8-5dc7-74870bfe0cf9"))
	}
	if h.systemWebhookDispatcher == nil {
		// dispatcher 未配線時は no-op (DB lookup の NO_SUCH_WEBHOOK 検証は通す)。
		return c.NoContent(http.StatusNoContent)
	}
	var overrideURL, overrideSecret string
	if req.Override != nil {
		overrideURL, overrideSecret = req.Override.URL, req.Override.Secret
	}
	h.systemWebhookDispatcher.DispatchSystemTest(sw.ID, req.Type, dummySystemWebhookBody(req.Type), overrideURL, overrideSecret)
	return c.NoContent(http.StatusNoContent)
}

// SystemWebhookUpdate handles POST /api/admin/system-webhook/update.
//
// 配送 processor が並行して latestSentAt/latestStatus を書き換えるため、
// FindByID→Save で全列上書きすると配送ステータスを古い値で踏み潰す。partial
// update (UpdateAdminFields) を使い admin 編集可能列のみ触る。
func (h *Handler) SystemWebhookUpdate(c echo.Context) error {
	if h.systemWebhookRepo == nil {
		return c.NoContent(http.StatusNoContent)
	}
	var req struct {
		ID       string    `json:"id"`
		Name     *string   `json:"name"`
		URL      *string   `json:"url"`
		Secret   *string   `json:"secret"`
		On       *[]string `json:"on"`
		IsActive *bool     `json:"isActive"`
	}
	if err := c.Bind(&req); err != nil || req.ID == "" {
		return c.JSON(http.StatusBadRequest, apierr.InvalidParam("Invalid parameters."))
	}
	// upstream update.ts は required:['id','isActive','name','on','url'] だが、
	// mk-go は意図的に partial update (id 以外は省略可) を許す superset 挙動を維持
	// する (#1772 で確認)。frontend MkSystemWebhookEditor は常に全フィールドを送る
	// ため drop-in 互換に影響せず、partial 対応は mk-go の追加の柔軟性。
	// 存在確認 (GORM Updates(map) は 0 行影響でも nil を返すため)
	before, err := h.systemWebhookRepo.FindByID(req.ID)
	if err != nil {
		return c.JSON(http.StatusNotFound, apierr.NotFound())
	}
	fields := map[string]any{"updatedAt": time.Now()}
	if req.Name != nil {
		fields["name"] = *req.Name
	}
	if req.URL != nil {
		fields["url"] = *req.URL
	}
	if req.Secret != nil {
		fields["secret"] = *req.Secret
	}
	if req.On != nil {
		// on は systemWebhookEventTypes enum のみ受理 (upstream update.ts、#1542)。
		if !validSystemWebhookEvents(*req.On) {
			return c.JSON(http.StatusBadRequest, apierr.InvalidParam("on contains an unknown event type."))
		}
		// pq.StringArray でラップしないと GORM Updates(map) が空 string[] を
		// NULL 化して NOT NULL 制約違反になる (#932、#931 / #896 と同 class)。
		fields["on"] = pq.StringArray(*req.On)
	}
	if req.IsActive != nil {
		fields["isActive"] = *req.IsActive
	}
	if err := h.systemWebhookRepo.UpdateAdminFields(req.ID, fields); err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	sw, err := h.systemWebhookRepo.FindByID(req.ID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, apierr.InternalError())
	}
	h.logModeration(c, moderationlog.LogUpdateSystemWebhook, map[string]any{
		"systemWebhookId": req.ID,
		"before":          before,
		"after":           sw,
	})
	return c.JSON(http.StatusOK, packSystemWebhook(sw))
}
