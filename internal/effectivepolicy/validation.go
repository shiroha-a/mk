// Package effectivepolicy contains the host schema shared by production policy
// resolution and plugin authoring tests.
package effectivepolicy

import (
	"fmt"
	"math"
	"strings"

	"github.com/shiroha-a/mk/plugin"
)

var defaults = map[string]any{
	"gtlAvailable":               true,
	"ltlAvailable":               true,
	"canPublicNote":              true,
	"mentionLimit":               20,
	"canInvite":                  false,
	"inviteLimit":                0,
	"inviteLimitCycle":           10080,
	"inviteExpirationTime":       0,
	"canManageCustomEmojis":      false,
	"canManageAvatarDecorations": false,
	"canSearchNotes":             false,
	"canSearchUsers":             true,
	"canUseTranslator":           true,
	"canHideAds":                 false,
	// upstream Misskey #17121のchannel作成権限。default trueで全員を許可し、
	// adminがrole経由で個別userを絞る。
	"canCreateChannel":       true,
	"driveCapacityMb":        100,
	"maxFileSizeMb":          30,
	"alwaysMarkNsfw":         false,
	"canUpdateBioMedia":      true,
	"pinLimit":               5,
	"antennaLimit":           5,
	"wordMuteLimit":          200,
	"webhookLimit":           3,
	"clipLimit":              10,
	"noteEachClipsLimit":     200,
	"userListLimit":          10,
	"userEachUserListsLimit": 50,
	"rateLimitFactor":        1,
	"avatarDecorationLimit":  1,
	"canImportAntennas":      false,
	"canImportBlocking":      false,
	"canImportFollowing":     false,
	"canImportMuting":        false,
	"canImportUserLists":     false,
	"chatAvailability":       "available",
	"uploadableFileTypes":    []string{"text/*", "application/json", "image/*", "video/*", "audio/*"},
	"noteDraftLimit":         10,
	"scheduledNoteLimit":     1,
	"watermarkAvailable":     true,
	// 分割uploadはmk-go独自。instance側の機能flagがfalseなら、このdefault
	// だけでは有効にならない。
	"canUseChunkedUpload":                true,
	"chunkedUploadMaxConcurrentSessions": 4,
	"chunkedUploadMaxPendingMb":          1024,
}

// Defaults returns a mutable copy of the host's native policy defaults.
// Mutable values are copied as well.
func Defaults() map[string]any {
	result := make(map[string]any, len(defaults))
	for key, value := range defaults {
		if values, ok := value.([]string); ok {
			result[key] = append([]string(nil), values...)
			continue
		}
		result[key] = value
	}
	return result
}

// ValidateRegistration applies the host's startup validation to a provider
// registration.
func ValidateRegistration(reg plugin.EffectivePolicyRegistration) error {
	if err := reg.Validate(); err != nil {
		return err
	}
	for _, key := range reg.Keys {
		if _, ok := defaults[key]; !ok {
			return fmt.Errorf("effectivepolicy: provider は既定外の policy key %q を宣言しています", key)
		}
	}
	return nil
}

// ValidateContributions reports whether contributions satisfy the host's
// native policy schema.
func ValidateContributions(keys []string, contributions []plugin.EffectivePolicyContribution) bool {
	type contributionTie struct {
		key   string
		order int
	}
	seen := make(map[contributionTie]struct{}, len(contributions))
	for _, contribution := range contributions {
		if !declaresKey(keys, contribution.Key) || contribution.Priority < 0 || contribution.Priority > 2 {
			return false
		}
		native, ok := defaults[contribution.Key]
		if !ok {
			return false
		}
		tie := contributionTie{key: contribution.Key, order: contribution.Order}
		if _, duplicate := seen[tie]; duplicate {
			return false
		}
		seen[tie] = struct{}{}
		if !contribution.UseDefault && !valueValid(contribution.Key, native, contribution.Value) {
			return false
		}
	}
	return true
}

func declaresKey(keys []string, key string) bool {
	for _, declared := range keys {
		if declared == key {
			return true
		}
	}
	return false
}

func valueValid(key string, native, value any) bool {
	switch native.(type) {
	case bool:
		_, ok := value.(bool)
		return ok
	case int:
		return numberValid(value)
	case string:
		v, ok := value.(string)
		if !ok {
			return false
		}
		if key == "chatAvailability" {
			return v == "available" || v == "readonly" || v == "unavailable"
		}
		return true
	case []string:
		switch v := value.(type) {
		case []string:
			for _, item := range v {
				if strings.TrimSpace(item) == "" {
					return false
				}
			}
			return true
		case []any:
			for _, item := range v {
				s, ok := item.(string)
				if !ok || strings.TrimSpace(s) == "" {
					return false
				}
			}
			return true
		}
		return false
	default:
		return false
	}
}

func numberValid(value any) bool {
	switch v := value.(type) {
	case int:
		return true
	case int64:
		converted := int(v)
		return int64(converted) == v
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
		minInclusive := float64(math.MinInt)
		maxExclusive := -minInclusive
		return v >= minInclusive && v < maxExclusive
	default:
		return false
	}
}
