// Package apierr provides Misskey-compatible error response helpers
// shared across all API handlers.
//
// All error responses follow the format:
//
//	{"error": {"message": ..., "code": ..., "id": ...}}
//
// Frequently used errors have canonical UUIDs to avoid drift between handlers.
package apierr

// Error returns a Misskey-compatible error response map.
// The returned map is safe to pass to echo.Context.JSON.
func Error(code, message, id string) map[string]any {
	return map[string]any{
		"error": map[string]any{
			"message": message,
			"code":    code,
			"id":      id,
		},
	}
}

// Canonical UUIDs for frequently-used error codes.
// Any handler returning these codes should use the constants to prevent drift.
// UUIDs are sourced from third_party/misskey/packages/backend/src/server/api/endpoints/
// and must match the upstream Misskey implementation so that clients can identify
// errors by their `id` field.
const (
	UUIDInvalidParam  = "3d81ceae-475f-4600-b2a8-2bc116157532"
	UUIDInternalError = "5d37dbcb-891e-41ca-a3d6-e690c97775ac"
	// UUIDNotFound は Misskey TS 上流に対応する汎用 NOT_FOUND code が存在
	// しない (上流は endpoint 固有 NO_SUCH_* を使う) ため mk-go 固有の
	// 安定 UUID を発番する。frontend 側の i18n には未対応だが、code+id 組
	// が安定するので将来 locale 引きを追加できる (#673 Phase A)。
	UUIDNotFound     = "8e6f5b1d-4f62-4ae0-9d3c-7c8d5b2e9f12"
	UUIDNoSuchNote   = "24fcbfc6-2e37-42b6-8388-c29b3861a08d"
	UUIDNoSuchUser   = "4362f8dc-731f-4ad8-a694-be5a88922a24"
	UUIDAccessDenied = "1fb7cb09-d46a-4fff-b8df-057708cce513"

	// UUIDs for notes/create errors (third_party/misskey/.../endpoints/notes/create.ts).
	UUIDNoSuchRenoteTarget                                         = "b5c90186-4ab0-49c8-9bba-a1f76c282ba4"
	UUIDCannotRenoteToAPureRenote                                  = "fd4cc33e-2a37-48dd-99cc-9b806eb2031a"
	UUIDCannotRenoteDueToVisibility                                = "be9529e9-fe72-4de0-ae43-0b363c4938af"
	UUIDNoSuchReplyTarget                                          = "749ee0f6-d3da-459a-bf02-282e2da4292c"
	UUIDCannotReplyToAnInvisibleNote                               = "b98980fa-3780-406c-a935-b6d0eeee10d1"
	UUIDCannotReplyToAPureRenote                                   = "3ac74a84-8fd5-4bb0-870f-01804f82ce15"
	UUIDCannotReplyToSpecifiedVisibilityNoteWithExtendedVisibility = "ed940410-535c-4d5e-bfa3-af798671e93c"
	UUIDCannotCreateAlreadyExpiredPoll                             = "04da457d-b083-4055-9082-955525eda5a5"
	UUIDNoSuchChannel                                              = "b1653923-5453-4edc-b786-7c4f39bb0bbb"
	UUIDYouHaveBeenBlocked                                         = "b390d7e1-8a5e-46ed-b625-06271cafd3d3"
	UUIDNoSuchFile                                                 = "b6992544-63e7-67f0-fa7f-32444b1b5306"
	UUIDCannotRenoteOutsideOfChannel                               = "33510210-8452-094c-6227-4a6c05d99f00"
	UUIDContainsProhibitedWords                                    = "aa6e01d3-a85c-669d-758a-76aab43af334"
	UUIDContainsTooManyMentions                                    = "4de0363a-3046-481b-9b0f-feff3e211025"

	// UUID for rate limiting (third_party/misskey/.../ApiCallService.ts).
	UUIDRateLimitExceeded = "d5826d14-3982-4d2e-8011-b9e9f02499ef"

	// UUID for users/show (third_party/misskey/.../endpoints/users/show.ts).
	UUIDFailedToResolveRemoteUser = "ef7b9be4-9cba-4e6f-ab41-90ed171c7d3c"

	// UUIDInvalidToken は 2FA token 検証失敗 (i/2fa/{done,register-key,key-done})。
	// upstream は plain `Error('authentication failed')` で UUID 無しなので
	// mk-go 固有の安定 UUID を発番する (#673 Phase B / #698)。
	UUIDInvalidToken = "f0fc0d2f-9805-432e-a69e-9898a4251660"

	// UUIDRegistrationFailed は WebAuthn 登録失敗 (i/2fa/key-done)。
	// upstream は verifyRegistration の throw を ApiError でなく素の Error で
	// 投げる (UUID 無し) ので mk-go 固有 UUID。
	UUIDRegistrationFailed = "1ddd78c8-1c1b-4077-a202-a64b16cebca1"

	// UUIDNoSuchKey は upstream `i/2fa/update-key` の `noSuchKey` UUID。
	// note: upstream の文字列 (`f9c5467f-d492-4d3c-9a8g-a70dacc86512`) は
	// 4 セグメント目に `g` を含んでおり厳密には valid UUID ではないが、
	// frontend / クライアントが文字列マッチで lookup する可能性があるので
	// upstream の typo をそのまま採用する (mk-go 側で勝手に矯正しない)。
	UUIDNoSuchKey = "f9c5467f-d492-4d3c-9a8g-a70dacc86512"

	// UUIDNoSecurityKey は upstream `i/2fa/password-less` の `noKey` UUID。
	// upstream typo (`9a8g`) を保持する理由は UUIDNoSuchKey と同じ。
	UUIDNoSecurityKey = "f9c54d7f-d4c2-4d3c-9a8g-a70daac86512"

	// UUIDNoSuchNoteDraft は upstream `notes/drafts/{update,delete}` 共通の
	// `noSuchNoteDraft` UUID (third_party/misskey/.../notes/drafts/update.ts)。
	// code は upstream に合わせて `NO_SUCH_NOTE_DRAFT` を使う (#688 / #673
	// Phase B)。
	UUIDNoSuchNoteDraft = "49cd6b9d-848e-41ee-b0b9-adaca711a6b1"
)

// InvalidParam returns a 400 INVALID_PARAM error response. The optional
// msg argument overrides the default "Invalid param." human-readable text;
// the UUID is fixed regardless so frontend i18n lookups remain stable
// (Misskey TS の INVALID_PARAM は単一 UUID で message は dev 向け説明)。
//
// Only msg[0] is used; subsequent values are silently ignored. An empty
// string ("") is treated the same as no argument and falls back to the
// default text — callers cannot suppress the message with "".
func InvalidParam(msg ...string) map[string]any {
	m := "Invalid param."
	if len(msg) > 0 && msg[0] != "" {
		m = msg[0]
	}
	return Error("INVALID_PARAM", m, UUIDInvalidParam)
}

// InternalError returns a 500 INTERNAL_ERROR error response. The optional
// msg argument overrides the default "Internal error." text; the UUID is
// fixed regardless so frontend i18n lookups remain stable.
//
// Only msg[0] is used; subsequent values are silently ignored. An empty
// string ("") falls back to the default text.
func InternalError(msg ...string) map[string]any {
	m := "Internal error."
	if len(msg) > 0 && msg[0] != "" {
		m = msg[0]
	}
	return Error("INTERNAL_ERROR", m, UUIDInternalError)
}

// NotFound returns a 404 NOT_FOUND error response. mk-go 固有の汎用 404
// (Misskey TS には対応する code が無く endpoint 固有 NO_SUCH_* を使う)。
// 該当 endpoint で具体的な NO_SUCH_* helper を新設する余裕が無い時の
// fallback で使う。msg は dev 向け説明として override 可能。
//
// Only msg[0] is used; subsequent values are silently ignored. An empty
// string ("") falls back to the default text.
func NotFound(msg ...string) map[string]any {
	m := "Not found."
	if len(msg) > 0 && msg[0] != "" {
		m = msg[0]
	}
	return Error("NOT_FOUND", m, UUIDNotFound)
}

// NoSuchNote returns a 404 NO_SUCH_NOTE error response.
func NoSuchNote() map[string]any {
	return Error("NO_SUCH_NOTE", "No such note.", UUIDNoSuchNote)
}

// NoSuchUser returns a 404 NO_SUCH_USER error response.
func NoSuchUser() map[string]any {
	return Error("NO_SUCH_USER", "No such user.", UUIDNoSuchUser)
}

// NoSuchNoteDraft returns a 404 NO_SUCH_NOTE_DRAFT error response (#688 /
// #673 Phase B). upstream `notes/drafts/{update,delete}` と code / id /
// message を完全一致させる。
func NoSuchNoteDraft() map[string]any {
	return Error("NO_SUCH_NOTE_DRAFT", "No such note draft.", UUIDNoSuchNoteDraft)
}

// AccessDenied returns a 403 ACCESS_DENIED error response.
func AccessDenied() map[string]any {
	return Error("ACCESS_DENIED", "Access denied.", UUIDAccessDenied)
}

// NoSuchRenoteTarget returns a 404 NO_SUCH_RENOTE_TARGET error response.
func NoSuchRenoteTarget() map[string]any {
	return Error("NO_SUCH_RENOTE_TARGET", "No such renote target.", UUIDNoSuchRenoteTarget)
}

// NoSuchReplyTarget returns a 404 NO_SUCH_REPLY_TARGET error response.
func NoSuchReplyTarget() map[string]any {
	return Error("NO_SUCH_REPLY_TARGET", "No such reply target.", UUIDNoSuchReplyTarget)
}

// CannotReplyToAnInvisibleNote returns a 403 CANNOT_REPLY_TO_AN_INVISIBLE_NOTE error response.
func CannotReplyToAnInvisibleNote() map[string]any {
	return Error("CANNOT_REPLY_TO_AN_INVISIBLE_NOTE", "You cannot reply to an invisible Note.", UUIDCannotReplyToAnInvisibleNote)
}

// CannotRenoteDueToVisibility returns a 403 CANNOT_RENOTE_DUE_TO_VISIBILITY error response.
func CannotRenoteDueToVisibility() map[string]any {
	return Error("CANNOT_RENOTE_DUE_TO_VISIBILITY", "You can not Renote due to target visibility.", UUIDCannotRenoteDueToVisibility)
}

// NoSuchChannel returns a 404 NO_SUCH_CHANNEL error response.
func NoSuchChannel() map[string]any {
	return Error("NO_SUCH_CHANNEL", "No such channel.", UUIDNoSuchChannel)
}

// CannotRenoteToAPureRenote returns a 403 CANNOT_RENOTE_TO_A_PURE_RENOTE response.
func CannotRenoteToAPureRenote() map[string]any {
	return Error("CANNOT_RENOTE_TO_A_PURE_RENOTE", "You can not Renote a pure Renote.", UUIDCannotRenoteToAPureRenote)
}

// CannotReplyToAPureRenote returns a 403 CANNOT_REPLY_TO_A_PURE_RENOTE response.
func CannotReplyToAPureRenote() map[string]any {
	return Error("CANNOT_REPLY_TO_A_PURE_RENOTE", "You can not reply to a pure Renote.", UUIDCannotReplyToAPureRenote)
}

// CannotReplyToSpecifiedVisibilityNoteWithExtendedVisibility returns the
// matching 403 response for reply visibility-conflict.
func CannotReplyToSpecifiedVisibilityNoteWithExtendedVisibility() map[string]any {
	return Error(
		"CANNOT_REPLY_TO_SPECIFIED_VISIBILITY_NOTE_WITH_EXTENDED_VISIBILITY",
		"You cannot reply to a specified visibility note with extended visibility.",
		UUIDCannotReplyToSpecifiedVisibilityNoteWithExtendedVisibility,
	)
}

// CannotCreateAlreadyExpiredPoll returns a 400 CANNOT_CREATE_ALREADY_EXPIRED_POLL response.
func CannotCreateAlreadyExpiredPoll() map[string]any {
	return Error("CANNOT_CREATE_ALREADY_EXPIRED_POLL", "Poll is already expired.", UUIDCannotCreateAlreadyExpiredPoll)
}

// YouHaveBeenBlocked returns a 403 YOU_HAVE_BEEN_BLOCKED response.
func YouHaveBeenBlocked() map[string]any {
	return Error("YOU_HAVE_BEEN_BLOCKED", "You have been blocked by this user.", UUIDYouHaveBeenBlocked)
}

// NoSuchFile returns a 400 NO_SUCH_FILE response.
func NoSuchFile() map[string]any {
	return Error("NO_SUCH_FILE", "Some files are not found.", UUIDNoSuchFile)
}

// CannotRenoteOutsideOfChannel returns a 403 CANNOT_RENOTE_OUTSIDE_OF_CHANNEL response.
func CannotRenoteOutsideOfChannel() map[string]any {
	return Error("CANNOT_RENOTE_OUTSIDE_OF_CHANNEL", "Cannot renote outside of channel.", UUIDCannotRenoteOutsideOfChannel)
}

// ContainsProhibitedWords returns a 400 CONTAINS_PROHIBITED_WORDS response.
func ContainsProhibitedWords() map[string]any {
	return Error("CONTAINS_PROHIBITED_WORDS", "Cannot post because it contains prohibited words.", UUIDContainsProhibitedWords)
}

// ContainsTooManyMentions returns a 400 CONTAINS_TOO_MANY_MENTIONS response.
func ContainsTooManyMentions() map[string]any {
	return Error("CONTAINS_TOO_MANY_MENTIONS", "Cannot post because it exceeds the allowed number of mentions.", UUIDContainsTooManyMentions)
}

// FailedToResolveRemoteUser returns a 404 FAILED_TO_RESOLVE_REMOTE_USER response.
func FailedToResolveRemoteUser() map[string]any {
	return Error("FAILED_TO_RESOLVE_REMOTE_USER", "Failed to resolve remote user.", UUIDFailedToResolveRemoteUser)
}

// RateLimitExceeded returns a 429 RATE_LIMIT_EXCEEDED error response.
func RateLimitExceeded() map[string]any {
	return Error("RATE_LIMIT_EXCEEDED", "Rate limit exceeded. Please try again later.", UUIDRateLimitExceeded)
}

// InvalidToken returns a 403 INVALID_TOKEN error response. Used by 2FA flows
// when the supplied TOTP / backup code does not authenticate the user.
func InvalidToken() map[string]any {
	return Error("INVALID_TOKEN", "Invalid token.", UUIDInvalidToken)
}

// RegistrationFailed returns a 403 REGISTRATION_FAILED response. Used by
// /api/i/2fa/key-done when the WebAuthn attestation does not verify.
func RegistrationFailed() map[string]any {
	return Error("REGISTRATION_FAILED", "Failed to finish registration.", UUIDRegistrationFailed)
}

// NoSuchKey returns a 404 NO_SUCH_KEY error response. Used by 2FA security
// key management endpoints when the credentialId is not owned by the caller.
func NoSuchKey() map[string]any {
	return Error("NO_SUCH_KEY", "No such key.", UUIDNoSuchKey)
}

// NoSecurityKey returns a 400 NO_SECURITY_KEY response. Used by
// /api/i/2fa/password-less when the user has no security key registered yet.
// Upstream uses the singular `NO_SECURITY_KEY` code (not plural).
func NoSecurityKey() map[string]any {
	return Error("NO_SECURITY_KEY", "No security key.", UUIDNoSecurityKey)
}
