package plugin

import (
	"context"
	"fmt"
	"math"
	"strings"
)

var effectivePolicyDefaults = map[string]any{
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

// EffectivePolicyRequest is the input to an effective policy resolver.
type EffectivePolicyRequest struct {
	// UserID is empty when the user is anonymous.
	UserID string
	// RoleIDs are active role IDs, sorted without duplicates. Anonymous
	// requests receive a non-nil empty slice.
	RoleIDs []string
}

// EffectivePolicyContribution is one policy key's contribution.
type EffectivePolicyContribution struct {
	Key        string
	Priority   int
	UseDefault bool
	Value      any
	// Order distinguishes multiple contributions for the same key and controls
	// their deterministic provider-local order. Each (Key, Order) pair must be
	// unique within one resolver result.
	Order int
}

// EffectivePolicyResolver computes effective policy contributions for a user.
type EffectivePolicyResolver func(context.Context, EffectivePolicyRequest) ([]EffectivePolicyContribution, error)

// EffectivePolicyRegistration declares an effective-policy provider.
type EffectivePolicyRegistration struct {
	Keys    []string
	Resolve EffectivePolicyResolver
}

// Validate reports whether the registration is usable.
func (r EffectivePolicyRegistration) Validate() error {
	if r.Resolve == nil {
		return fmt.Errorf("plugin: EffectivePolicies の Resolve が nil です")
	}
	if len(r.Keys) == 0 {
		return fmt.Errorf("plugin: EffectivePolicies の Keys が空です")
	}
	seen := make(map[string]bool, len(r.Keys))
	for _, key := range r.Keys {
		if key == "" {
			return fmt.Errorf("plugin: EffectivePolicies の Keys に空のキーが含まれています")
		}
		if seen[key] {
			return fmt.Errorf("plugin: EffectivePolicies の Keys に重複があります (%q)", key)
		}
		seen[key] = true
	}
	return nil
}

// EffectivePolicyDefaults returns a mutable copy of the host's native policy
// defaults. Mutable values are copied as well.
func EffectivePolicyDefaults() map[string]any {
	defaults := make(map[string]any, len(effectivePolicyDefaults))
	for key, value := range effectivePolicyDefaults {
		if values, ok := value.([]string); ok {
			defaults[key] = append([]string(nil), values...)
			continue
		}
		defaults[key] = value
	}
	return defaults
}

// ValidateEffectivePolicyRegistration applies the host's startup validation
// to an effective-policy registration.
func ValidateEffectivePolicyRegistration(reg EffectivePolicyRegistration) error {
	if err := reg.Validate(); err != nil {
		return err
	}
	for _, key := range reg.Keys {
		if _, ok := effectivePolicyDefaults[key]; !ok {
			return fmt.Errorf("plugin: EffectivePolicies は既定外の policy key %q を宣言しています", key)
		}
	}
	return nil
}

// ValidateEffectivePolicyContributions reports whether contributions satisfy
// the host's native policy schema.
func ValidateEffectivePolicyContributions(keys []string, contributions []EffectivePolicyContribution) bool {
	type contributionTie struct {
		key   string
		order int
	}
	seen := make(map[contributionTie]struct{}, len(contributions))
	for _, contribution := range contributions {
		if !declaresEffectivePolicyKey(keys, contribution.Key) || contribution.Priority < 0 || contribution.Priority > 2 {
			return false
		}
		native, ok := effectivePolicyDefaults[contribution.Key]
		if !ok {
			return false
		}
		tie := contributionTie{key: contribution.Key, order: contribution.Order}
		if _, duplicate := seen[tie]; duplicate {
			return false
		}
		seen[tie] = struct{}{}
		if !contribution.UseDefault && !effectivePolicyValueValid(contribution.Key, native, contribution.Value) {
			return false
		}
	}
	return true
}

func declaresEffectivePolicyKey(keys []string, key string) bool {
	for _, declared := range keys {
		if declared == key {
			return true
		}
	}
	return false
}

func effectivePolicyValueValid(key string, native, value any) bool {
	switch native.(type) {
	case bool:
		_, ok := value.(bool)
		return ok
	case int:
		return effectivePolicyNumberValid(value)
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

func effectivePolicyNumberValid(value any) bool {
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

// EffectivePolicyInvalidator drops cached policy inputs and successful
// provider output after committed state changes.
type EffectivePolicyInvalidator interface {
	InvalidateUser(context.Context, string) error
	// InvalidateRole is intentionally broader than one role because conditional
	// role holders cannot be enumerated from assignments.
	InvalidateRole(context.Context, string) error
}
