package admin_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSendEmail(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	// to/subject/text すべて揃えば 204 (#1539)。
	assert.Equal(t, http.StatusNoContent, doPost(h.SendEmail, `{"to":"a@example.com","subject":"s","text":"t"}`, adminUser).Code)
}

// #1539: subject / text 欠落は upstream required により 400。
func TestSendEmail_MissingSubjectOrText(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	assert.Equal(t, http.StatusBadRequest, doPost(h.SendEmail, `{}`, adminUser).Code)
	assert.Equal(t, http.StatusBadRequest, doPost(h.SendEmail, `{"to":"a@example.com"}`, adminUser).Code)
	assert.Equal(t, http.StatusBadRequest, doPost(h.SendEmail, `{"to":"a@example.com","subject":"s"}`, adminUser).Code)
}
