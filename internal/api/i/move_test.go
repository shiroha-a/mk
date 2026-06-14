package i

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMoveAccount(t *testing.T) {
	h, _ := newExtraHandler(t)
	// moveToAccount 未指定は 400 (#1546: password は任意)。
	assert.Equal(t, http.StatusBadRequest, postExtra(h.Move, `{}`, stubUser).Code)
	// password を指定したが profile 未登録 → ACCESS_DENIED。
	assert.Equal(t, http.StatusForbidden, postExtra(h.Move, `{"moveToAccount":"https://x","password":"pw"}`, stubUser).Code)
}
