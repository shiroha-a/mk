// Package credkey derives the opaque key that identifies which credential a
// streaming connection was authenticated with.
//
// 接続を張った側 (internal/api/streaming) と、失効時に接続を閉じる側
// (internal/stream の revoke publisher) が同じ形式を使わないと一致しない。
// 片側だけ書式を変えると「閉じたつもりで何も閉じない」形で黙って壊れるので、
// 導出をここ 1 箇所に置く。
package credkey

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	nativePrefix = "native:"
	appPrefix    = "app:"
)

// Native returns the key for a native login token (user.token). The raw token
// is hashed so the key can travel over Redis pubsub without leaking a usable
// credential. Empty input yields "".
func Native(token string) string {
	if token == "" {
		return ""
	}
	// 生 token を pubsub に流すと Redis を覗ける者に有効な資格情報を渡すことに
	// なるので、一方向ハッシュにしてから鍵にする。
	sum := sha256.Sum256([]byte(token))
	return nativePrefix + hex.EncodeToString(sum[:])
}

// AccessToken returns the key for an app access token, identified by its row
// id. Empty input yields "".
func AccessToken(tokenID string) string {
	if tokenID == "" {
		return ""
	}
	return appPrefix + tokenID
}
