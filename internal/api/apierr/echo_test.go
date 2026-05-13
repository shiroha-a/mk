package apierr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func invoke(t *testing.T, fn func(echo.Context) error) (int, map[string]any) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	_ = fn(c)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return rec.Code, body
}

func TestJSONInvalidParam(t *testing.T) {
	code, body := invoke(t, func(c echo.Context) error { return JSONInvalidParam(c) })
	assert.Equal(t, http.StatusBadRequest, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "INVALID_PARAM", errObj["code"])
	assert.Equal(t, UUIDInvalidParam, errObj["id"])
	assert.Equal(t, "Invalid param.", errObj["message"])
}

func TestJSONInvalidParam_CustomMessage(t *testing.T) {
	code, body := invoke(t, func(c echo.Context) error { return JSONInvalidParam(c, "userId is required.") })
	assert.Equal(t, http.StatusBadRequest, code)
	errObj := body["error"].(map[string]any)
	// UUID は固定 (i18n lookup 用)、message だけ override される
	assert.Equal(t, UUIDInvalidParam, errObj["id"])
	assert.Equal(t, "userId is required.", errObj["message"])
}

func TestJSONInternalError(t *testing.T) {
	code, body := invoke(t, func(c echo.Context) error { return JSONInternalError(c) })
	assert.Equal(t, http.StatusInternalServerError, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "INTERNAL_ERROR", errObj["code"])
	assert.Equal(t, UUIDInternalError, errObj["id"])
	assert.Equal(t, "Internal error.", errObj["message"])
}

func TestJSONInternalError_CustomMessage(t *testing.T) {
	code, body := invoke(t, func(c echo.Context) error { return JSONInternalError(c, "queue not configured.") })
	assert.Equal(t, http.StatusInternalServerError, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, UUIDInternalError, errObj["id"])
	assert.Equal(t, "queue not configured.", errObj["message"])
}

func TestJSONNotFound(t *testing.T) {
	code, body := invoke(t, func(c echo.Context) error { return JSONNotFound(c) })
	assert.Equal(t, http.StatusNotFound, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "NOT_FOUND", errObj["code"])
	assert.Equal(t, UUIDNotFound, errObj["id"])
	assert.Equal(t, "Not found.", errObj["message"])
}

func TestJSONNotFound_CustomMessage(t *testing.T) {
	code, body := invoke(t, func(c echo.Context) error { return JSONNotFound(c, "queue task not found.") })
	assert.Equal(t, http.StatusNotFound, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, UUIDNotFound, errObj["id"])
	assert.Equal(t, "queue task not found.", errObj["message"])
}

func TestJSONNoSuchUser(t *testing.T) {
	code, body := invoke(t, JSONNoSuchUser)
	assert.Equal(t, http.StatusNotFound, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "NO_SUCH_USER", errObj["code"])
	assert.Equal(t, UUIDNoSuchUser, errObj["id"])
}

func TestJSONNoSuchNote(t *testing.T) {
	code, body := invoke(t, JSONNoSuchNote)
	assert.Equal(t, http.StatusNotFound, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "NO_SUCH_NOTE", errObj["code"])
	assert.Equal(t, UUIDNoSuchNote, errObj["id"])
}

func TestJSONAccessDenied(t *testing.T) {
	code, body := invoke(t, JSONAccessDenied)
	assert.Equal(t, http.StatusForbidden, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ACCESS_DENIED", errObj["code"])
	assert.Equal(t, UUIDAccessDenied, errObj["id"])
}

func TestJSONRestrictedByRole(t *testing.T) {
	code, body := invoke(t, JSONRestrictedByRole)
	assert.Equal(t, http.StatusForbidden, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "RESTRICTED_BY_ROLE", errObj["code"])
	assert.Equal(t, UUIDRestrictedByRole, errObj["id"])
}

func TestJSONLtlDisabled(t *testing.T) {
	code, body := invoke(t, JSONLtlDisabled)
	assert.Equal(t, http.StatusForbidden, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "LTL_DISABLED", errObj["code"])
	assert.Equal(t, UUIDLtlDisabled, errObj["id"])
}

func TestJSONGtlDisabled(t *testing.T) {
	code, body := invoke(t, JSONGtlDisabled)
	assert.Equal(t, http.StatusForbidden, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "GTL_DISABLED", errObj["code"])
	assert.Equal(t, UUIDGtlDisabled, errObj["id"])
}

func TestJSONNoSuchRenoteTarget(t *testing.T) {
	code, body := invoke(t, JSONNoSuchRenoteTarget)
	assert.Equal(t, http.StatusNotFound, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "NO_SUCH_RENOTE_TARGET", errObj["code"])
	assert.Equal(t, UUIDNoSuchRenoteTarget, errObj["id"])
}

func TestJSONNoSuchReplyTarget(t *testing.T) {
	code, body := invoke(t, JSONNoSuchReplyTarget)
	assert.Equal(t, http.StatusNotFound, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "NO_SUCH_REPLY_TARGET", errObj["code"])
	assert.Equal(t, UUIDNoSuchReplyTarget, errObj["id"])
}

func TestJSONCannotReplyToAnInvisibleNote(t *testing.T) {
	code, body := invoke(t, JSONCannotReplyToAnInvisibleNote)
	assert.Equal(t, http.StatusForbidden, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "CANNOT_REPLY_TO_AN_INVISIBLE_NOTE", errObj["code"])
	assert.Equal(t, UUIDCannotReplyToAnInvisibleNote, errObj["id"])
}

func TestJSONCannotRenoteDueToVisibility(t *testing.T) {
	code, body := invoke(t, JSONCannotRenoteDueToVisibility)
	assert.Equal(t, http.StatusForbidden, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "CANNOT_RENOTE_DUE_TO_VISIBILITY", errObj["code"])
	assert.Equal(t, UUIDCannotRenoteDueToVisibility, errObj["id"])
}

func TestJSONNoSuchChannel(t *testing.T) {
	code, body := invoke(t, JSONNoSuchChannel)
	assert.Equal(t, http.StatusNotFound, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "NO_SUCH_CHANNEL", errObj["code"])
	assert.Equal(t, UUIDNoSuchChannel, errObj["id"])
}

// Phase 7-1 follow-up (#254): 新規JSONヘルパーのカバレッジ
func TestJSONCannotRenoteToAPureRenote(t *testing.T) {
	code, body := invoke(t, JSONCannotRenoteToAPureRenote)
	assert.Equal(t, http.StatusForbidden, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "CANNOT_RENOTE_TO_A_PURE_RENOTE", errObj["code"])
}

func TestJSONCannotReplyToAPureRenote(t *testing.T) {
	code, body := invoke(t, JSONCannotReplyToAPureRenote)
	assert.Equal(t, http.StatusForbidden, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "CANNOT_REPLY_TO_A_PURE_RENOTE", errObj["code"])
}

func TestJSONCannotReplyToSpecifiedVisibilityNoteWithExtendedVisibility(t *testing.T) {
	code, body := invoke(t, JSONCannotReplyToSpecifiedVisibilityNoteWithExtendedVisibility)
	assert.Equal(t, http.StatusForbidden, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "CANNOT_REPLY_TO_SPECIFIED_VISIBILITY_NOTE_WITH_EXTENDED_VISIBILITY", errObj["code"])
}

func TestJSONCannotCreateAlreadyExpiredPoll(t *testing.T) {
	code, body := invoke(t, JSONCannotCreateAlreadyExpiredPoll)
	assert.Equal(t, http.StatusBadRequest, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "CANNOT_CREATE_ALREADY_EXPIRED_POLL", errObj["code"])
}

func TestJSONYouHaveBeenBlocked(t *testing.T) {
	code, body := invoke(t, JSONYouHaveBeenBlocked)
	assert.Equal(t, http.StatusForbidden, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "YOU_HAVE_BEEN_BLOCKED", errObj["code"])
}

func TestJSONNoSuchFile(t *testing.T) {
	code, body := invoke(t, JSONNoSuchFile)
	assert.Equal(t, http.StatusBadRequest, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "NO_SUCH_FILE", errObj["code"])
}

func TestJSONCannotRenoteOutsideOfChannel(t *testing.T) {
	code, body := invoke(t, JSONCannotRenoteOutsideOfChannel)
	assert.Equal(t, http.StatusForbidden, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "CANNOT_RENOTE_OUTSIDE_OF_CHANNEL", errObj["code"])
}

func TestJSONContainsProhibitedWords(t *testing.T) {
	code, body := invoke(t, JSONContainsProhibitedWords)
	assert.Equal(t, http.StatusBadRequest, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "CONTAINS_PROHIBITED_WORDS", errObj["code"])
}

func TestJSONContainsTooManyMentions(t *testing.T) {
	code, body := invoke(t, JSONContainsTooManyMentions)
	assert.Equal(t, http.StatusBadRequest, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "CONTAINS_TOO_MANY_MENTIONS", errObj["code"])
}

func TestJSONFailedToResolveRemoteUser(t *testing.T) {
	code, body := invoke(t, JSONFailedToResolveRemoteUser)
	assert.Equal(t, http.StatusNotFound, code)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "FAILED_TO_RESOLVE_REMOTE_USER", errObj["code"])
}
