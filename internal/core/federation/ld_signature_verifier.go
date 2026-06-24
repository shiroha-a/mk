package federation

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shiroha-a/mk/internal/activitypub/ld"
	"github.com/shiroha-a/mk/internal/repository"
)

// LDSignatureVerifier verifies the optional LD-Signature on an inbound
// activity. mk-go では従来 HTTP Signature のみで認証していたが、relay 経由
// 配送等で original creator を確認する経路で LD-Signature (RsaSignature2017)
// が必要になる。
//
// 本 verifier は activity body の `signature` field を gate に動作する:
//   - signature 無し → skip (nil 返却、HTTP Signature だけで処理続行)
//   - signature 有り + verify pass → nil
//   - signature 有り + verify fail → error (caller は activity を drop する)
//
// upstream Misskey TS 2026.5.4 の InboxProcessorService.process が
// `compact → checkForForbiddenDirectives → freeze → verifyRsaSignature2017`
// の sequence を全 inbound activity に適用するのと同 semantics を提供する。
// canonicalize 中の SSRF / cache amplification / spoofing 攻撃は ld.Processor
// 側の hardening (forbidden directives / cache cap / freeze) で遮断する。
type LDSignatureVerifier struct {
	pubkeyRepo repository.UserPublickeyRepository
}

// NewLDSignatureVerifier returns a verifier wired to the supplied
// user_publickey repository. signature.creator (key URI) から keyId 一致する
// row を引いて PEM を取り出す。
func NewLDSignatureVerifier(pubkeyRepo repository.UserPublickeyRepository) *LDSignatureVerifier {
	return &LDSignatureVerifier{pubkeyRepo: pubkeyRepo}
}

// VerifyIfPresent inspects the activity body and, if it carries a non-empty
// `signature` field, verifies it. Returns nil when:
//   - body has no `signature` field
//   - signature verifies successfully
//
// Returns error when:
//   - body has `signature` but creator / signatureValue missing or malformed
//   - public key cannot be resolved
//   - forbidden directive detected in the activity
//   - RsaSignature2017 verify fails (signature mismatch / key mismatch /
//     unsupported algorithm)
func (v *LDSignatureVerifier) VerifyIfPresent(rawBody []byte) error {
	_, _, err := v.VerifyAndCreator(rawBody)
	return err
}

// VerifyAndCreator behaves like VerifyIfPresent but additionally reports
// whether the activity carried an LD-Signature (`present`) and, on success,
// the verified `signature.creator` key URI. Callers use the creator to
// confirm the LD-Signature actually authenticates the activity's `actor`
// (forwarded-activity authentication, upstream
// `authUser.user.uri !== getApId(activity.actor)` gate).
//
//   - no `signature` field        -> ("", false, nil)
//   - signature present + verify  -> (creator, true, nil)
//   - signature present + invalid -> ("", true, err)
func (v *LDSignatureVerifier) VerifyAndCreator(rawBody []byte) (string, bool, error) {
	if v == nil || v.pubkeyRepo == nil {
		return "", false, nil
	}
	var act map[string]any
	if err := json.Unmarshal(rawBody, &act); err != nil {
		return "", false, fmt.Errorf("ld-sig: body unmarshal: %w", err)
	}
	sigRaw, hasSig := act["signature"]
	if !hasSig || sigRaw == nil {
		return "", false, nil
	}
	sig, ok := sigRaw.(map[string]any)
	if !ok {
		return "", true, errors.New("ld-sig: signature field is not an object")
	}
	creator, _ := sig["creator"].(string)
	if creator == "" {
		return "", true, errors.New("ld-sig: signature.creator missing")
	}
	// per-verify fresh processor (upstream JsonLd class instance と等価)。
	// cache cap / freeze は新規 instance ごとに reset される。
	//
	// 順序は upstream `compact → checkForForbiddenDirectives → freeze →
	// verifyRsaSignature2017` を踏襲する。forbidden directive を pubkey
	// resolve より先に check して、不正 activity は DB lookup のコストを
	// 払う前に reject する。
	//
	// **mk-go の前提**: upstream は compact 後の document に対して forbidden
	// check を実行するが、mk-go は raw activity に直接適用する (= compact を
	// 呼ばない)。これは `ld.PreloadedLoader` が HTTP fetch を一切行わない
	// 設計 (= 3 つの embed context のみ resolve、それ以外は ErrContextNotPreloaded)
	// のため、remote context injection で directive を後付けする攻撃ベクタが
	// 構造的に存在しないことを前提にしている。raw activity 段階の check で
	// 十分かつ upstream より strict (= 攻撃者が compact 内で `@reverse` を意
	// 味変換しようとしても入口で reject される)。
	//
	// 将来 mk-go に HTTP fetch fallback を追加する場合は、compact 後 check
	// に切り替える必要がある (= remote context が後付け directive を inject
	// する経路が成立してしまうため)。その場合は本コメントを更新し、
	// `proc.Compact(act, ...)` を挟んでから check するフローに変更する。
	proc := ld.NewProcessor()
	if err := proc.CheckForForbiddenDirectives(act); err != nil {
		return "", true, err
	}
	proc.Freeze()
	pubkey, err := v.pubkeyRepo.FindByKeyID(creator)
	if err != nil {
		return "", true, fmt.Errorf("ld-sig: public key not found for keyId=%s: %w", creator, err)
	}
	if err := proc.VerifyRsaSignature2017(act, pubkey.KeyPEM); err != nil {
		return "", true, err
	}
	return creator, true, nil
}

// CheckForbiddenDirectivesIfPresent runs only the forbidden-directive hardening of
// the LD-Signature pipeline (no public-key resolution, no RsaSignature2017 verify).
//
// Used by the signer==actor inbound path (#2106 N26) where upstream
// InboxProcessorService skips LD-Signature processing entirely. mk-go keeps the
// JSON-LD term-redefinition defense (CheckForForbiddenDirectives) but, unlike
// VerifyIfPresent, does NOT drop an HTTP-signature-authenticated activity merely
// because its LD-Signature fails to verify (uncached creator key / legacy or
// mismatched LD-sig / normalize 差異). Returns nil when the body carries no
// `signature` field (same scope as VerifyIfPresent).
func (v *LDSignatureVerifier) CheckForbiddenDirectivesIfPresent(rawBody []byte) error {
	if v == nil {
		return nil
	}
	var act map[string]any
	if err := json.Unmarshal(rawBody, &act); err != nil {
		return fmt.Errorf("ld-sig: body unmarshal: %w", err)
	}
	if sigRaw, hasSig := act["signature"]; !hasSig || sigRaw == nil {
		return nil
	}
	return ld.NewProcessor().CheckForForbiddenDirectives(act)
}
