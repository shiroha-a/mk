package admin

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/misc/smtp"
)

// SendEmail handles POST /api/admin/send-email.
func (h *Handler) SendEmail(c echo.Context) error {
	var req struct {
		To      string `json:"to"`
		Subject string `json:"subject"`
		Text    string `json:"text"`
	}
	if err := c.Bind(&req); err != nil || req.To == "" {
		return c.NoContent(http.StatusNoContent)
	}
	// SMTP送信
	if h.metaRepo != nil {
		m, err := h.metaRepo.Fetch()
		if err == nil && m.EnableEmail && m.SmtpHost != nil && m.Email != nil {
			port := 587
			if m.SmtpPort != nil {
				port = *m.SmtpPort
			}
			go smtp.SendWithOptions(*m.SmtpHost, port, m.SmtpUser, m.SmtpPass, *m.Email, req.To, req.Subject, req.Text, smtp.Options{ProxyURL: h.smtpProxyURL, Secure: m.SmtpSecure})
		}
	}
	return c.NoContent(http.StatusNoContent)
}
