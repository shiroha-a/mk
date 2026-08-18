package role

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/plugin"
)

// ErrEffectivePolicyProvider is returned by GetUserPoliciesChecked when a
// registered effective-policy provider fails. Fixed and identifier-free:
// the underlying provider error is never surfaced to the caller.
var ErrEffectivePolicyProvider = errors.New("effective policy provider failed")

const effectivePolicyProviderTimeout = time.Second

type policyProviderRuntime struct {
	token    chan struct{}
	disabled atomic.Bool
}

func newPolicyProviderRuntime() *policyProviderRuntime {
	runtime := &policyProviderRuntime{token: make(chan struct{}, 1)}
	runtime.token <- struct{}{}
	return runtime
}

// policyProvider pairs a plugin name with its validated registration. The
// registration's Keys slice is defensively copied at registration time so the
// stored value is never aliased by the caller.
type policyProvider struct {
	name    string
	reg     plugin.EffectivePolicyRegistration
	runtime *policyProviderRuntime
}

// RegisterEffectivePolicyProvider registers an effective-policy provider under
// name. The registration is validated (plugin.EffectivePolicyRegistration.
// Validate) before being stored — this is a load-bearing contract: an invalid
// registration must never reach the resolution path. Declared keys are also
// required to exist in the native DefaultPolicies set so a provider can only
// contribute to keys the host understands.
//
// Registrations are stored sorted by plugin name and are immutable once
// stored; concurrent resolution takes a defensively-copied snapshot.
func (s *Service) RegisterEffectivePolicyProvider(name string, reg plugin.EffectivePolicyRegistration) error {
	if name == "" {
		return errors.New("role: effective policy provider の名前が空です")
	}
	// ロードベアリング: Validate を必ず呼んでから保存する。
	if err := reg.Validate(); err != nil {
		return err
	}
	defaults := DefaultPolicies()
	for _, k := range reg.Keys {
		if _, ok := defaults[k]; !ok {
			return fmt.Errorf("role: effective policy provider %q は既定外の policy key %q を宣言しています", name, k)
		}
	}
	// 防御的コピー: caller の Keys slice と共有しない (登録後の外部変更が
	// 解決結果に漏れないようにする)。
	keys := make([]string, len(reg.Keys))
	copy(keys, reg.Keys)
	reg.Keys = keys

	s.policyProviderMu.Lock()
	defer s.policyProviderMu.Unlock()
	for _, existing := range s.policyProviders {
		if existing.name == name {
			return errors.New("role: effective policy provider name is already registered")
		}
	}
	providers := make([]policyProvider, 0, len(s.policyProviders)+1)
	providers = append(providers, s.policyProviders...)
	providers = append(providers, policyProvider{name: name, reg: reg, runtime: newPolicyProviderRuntime()})
	sort.Slice(providers, func(i, j int) bool {
		return providers[i].name < providers[j].name
	})
	s.policyProviders = providers
	return nil
}

// snapshotPolicyProviders returns a defensively-copied, name-sorted snapshot
// of the registered providers. The returned slice and its elements are
// read-only to callers.
func (s *Service) snapshotPolicyProviders() []policyProvider {
	s.policyProviderMu.RLock()
	defer s.policyProviderMu.RUnlock()
	if len(s.policyProviders) == 0 {
		return nil
	}
	out := make([]policyProvider, len(s.policyProviders))
	copy(out, s.policyProviders)
	return out
}

// GetUserPoliciesChecked returns the user's effective role policies the same
// way GetUserPolicies does, but also reports whether a registered provider
// failed. On provider failure the returned map uses the native policy result
// for every key declared by that provider, and the error is the fixed
// ErrEffectivePolicyProvider. Provider output is never cached, so a failure
// after a prior success never reuses stale permissive values.
func (s *Service) GetUserPoliciesChecked(userID string) (map[string]any, error) {
	return s.resolvePolicies(userID)
}

// resolvePolicies computes effective policies for userID, invoking registered
// providers after native roles are resolved and before server caps are
// applied. The error is non-nil only when a provider failed, in which case
// the returned map has the failed providers' declared keys restored to their
// native policy results.
func (s *Service) resolvePolicies(userID string) (map[string]any, error) {
	// applyMetaBasePolicies が base を mutate するため共有 cache ではなく clone を使う。
	base := DefaultPoliciesClone()
	s.applyMetaBasePolicies(base)

	roles := []*model.Role{}
	if userID != "" {
		var err error
		roles, err = s.GetUserRoles(userID)
		if err != nil {
			// role 取得失敗時は base policies で fallback (upstream は throw するが、
			// mk-go は fail-soft で gate を default 値に倒す)。
			return s.applyServerCaps(base), nil
		}
	}
	roleOverrides := make([]map[string]rolePolicyOverride, 0, len(roles))
	for _, r := range roles {
		if r == nil || len(r.Policies) == 0 {
			roleOverrides = append(roleOverrides, nil)
			continue
		}
		roleOverrides = append(roleOverrides, parseRolePolicies(r.Policies))
	}

	out := make(map[string]any, len(base))
	for key, baseVal := range base {
		out[key] = computePolicy(key, baseVal, roleOverrides, nil)
	}
	native := make(map[string]any, len(out))
	for key, value := range out {
		native[key] = clonePolicyValue(value)
	}

	providers := s.snapshotPolicyProviders()
	if len(providers) == 0 {
		return s.applyServerCaps(out), nil
	}

	// provider には現在 active な native RoleID のみを、ソート + clone して渡す。
	roleIDs := activeRoleIDs(roles)

	// key -> provider contribution entries (同一 priority cascade に参加させる)。
	contribs := make(map[string][]policyEntry)
	// 失敗したproviderの宣言keyはplugin貢献をすべて破棄してnative結果へ戻す。
	failed := make(map[string]bool)

	for _, p := range providers {
		providerRoleIDs := make([]string, len(roleIDs))
		copy(providerRoleIDs, roleIDs)
		res, ok := invokePolicyProvider(p, plugin.EffectivePolicyRequest{UserID: userID, RoleIDs: providerRoleIDs})
		if !ok || !validatePolicyContributions(p.reg.Keys, base, res) {
			// provider の失敗は logging しない (identifier / 値を露出させない)。
			for _, k := range p.reg.Keys {
				failed[k] = true
			}
			continue
		}
		res = append([]plugin.EffectivePolicyContribution(nil), res...)
		sort.Slice(res, func(i, j int) bool { return lessPolicyContribution(res[i], res[j]) })
		for _, c := range res {
			baseVal := base[c.Key]
			value := c.Value
			if c.UseDefault {
				value = baseVal
			}
			value = clonePolicyValue(value)
			contribs[c.Key] = append(contribs[c.Key], policyEntry{priority: c.Priority, value: value})
		}
	}

	for key, entries := range contribs {
		out[key] = computePolicy(key, base[key], roleOverrides, entries)
	}

	for key := range failed {
		if value, ok := native[key]; ok {
			out[key] = clonePolicyValue(value)
		}
	}

	out = s.applyServerCaps(out)
	if len(failed) > 0 {
		return out, ErrEffectivePolicyProvider
	}
	return out, nil
}

type policyProviderResult struct {
	contributions []plugin.EffectivePolicyContribution
	ok            bool
	completedAt   time.Time
}

func invokePolicyProvider(provider policyProvider, req plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, bool) {
	if provider.runtime.disabled.Load() {
		return nil, false
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), effectivePolicyProviderTimeout)
	if !acquirePolicyProviderToken(waitCtx, provider.runtime) {
		cancelWait()
		return nil, false
	}
	cancelWait()
	if provider.runtime.disabled.Load() {
		provider.runtime.token <- struct{}{}
		return nil, false
	}

	resolveCtx, cancelResolve := context.WithTimeout(context.Background(), effectivePolicyProviderTimeout)
	defer cancelResolve()

	result := make(chan policyProviderResult, 1)
	go func() {
		out := policyProviderResult{}
		defer func() {
			if recover() != nil {
				out = policyProviderResult{}
			}
			out.completedAt = time.Now()
			if deadline, ok := resolveCtx.Deadline(); ok && !out.completedAt.Before(deadline) {
				provider.runtime.disabled.Store(true)
			}
			result <- out
			provider.runtime.token <- struct{}{}
		}()
		contributions, err := provider.reg.Resolve(resolveCtx, req)
		out = policyProviderResult{contributions: contributions, ok: err == nil}
	}()

	out, completedBeforeDeadline := receivePolicyProviderResult(resolveCtx, result)
	if !completedBeforeDeadline {
		provider.runtime.disabled.Store(true)
		return nil, false
	}
	return out.contributions, out.ok
}

func acquirePolicyProviderToken(ctx context.Context, runtime *policyProviderRuntime) bool {
	if policyProviderDeadlineExceeded(ctx) {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-runtime.token:
		if policyProviderDeadlineExceeded(ctx) {
			runtime.token <- struct{}{}
			return false
		}
		return true
	}
}

func receivePolicyProviderResult(ctx context.Context, result <-chan policyProviderResult) (policyProviderResult, bool) {
	accept := func(out policyProviderResult) (policyProviderResult, bool) {
		deadline, hasDeadline := ctx.Deadline()
		if hasDeadline {
			return out, out.completedAt.Before(deadline)
		}
		return out, ctx.Err() == nil
	}

	select {
	case out := <-result:
		return accept(out)
	default:
	}
	select {
	case out := <-result:
		return accept(out)
	case <-ctx.Done():
		select {
		case out := <-result:
			return accept(out)
		default:
			return policyProviderResult{}, false
		}
	}
}

func policyProviderDeadlineExceeded(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	return ok && !time.Now().Before(deadline)
}

func validatePolicyContributions(keys []string, base map[string]any, contributions []plugin.EffectivePolicyContribution) bool {
	type contributionTie struct {
		key   string
		order int
	}
	seen := make(map[contributionTie]struct{}, len(contributions))
	for _, c := range contributions {
		if !declaresKey(keys, c.Key) || c.Priority < 0 || c.Priority > 2 {
			return false
		}
		baseVal, ok := base[c.Key]
		if !ok {
			return false
		}
		tie := contributionTie{key: c.Key, order: c.Order}
		if _, duplicate := seen[tie]; duplicate {
			return false
		}
		seen[tie] = struct{}{}
		if !c.UseDefault && !policyValueValid(c.Key, baseVal, c.Value) {
			return false
		}
	}
	return true
}

func lessPolicyContribution(a, b plugin.EffectivePolicyContribution) bool {
	if a.Order != b.Order {
		return a.Order < b.Order
	}
	if a.Key != b.Key {
		return a.Key < b.Key
	}
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.UseDefault != b.UseDefault {
		return !a.UseDefault
	}
	return false
}

// activeRoleIDs extracts the currently active role IDs from the resolved
// roles, dedupes and sorts them, returning a fresh cloned slice. The returned
// slice is never aliased by the caller (providers may mutate their copy).
func activeRoleIDs(roles []*model.Role) []string {
	if len(roles) == 0 {
		return []string{}
	}
	set := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		if r == nil || r.ID == "" {
			continue
		}
		set[r.ID] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// declaresKey reports whether key is present in the provider's declared Keys.
func declaresKey(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

// policyValueValid reports whether value is a valid provider value for key.
func policyValueValid(key string, native, value any) bool {
	switch native.(type) {
	case bool:
		_, ok := value.(bool)
		return ok
	case int:
		_, ok := providerHostInt(value)
		return ok
	case int64:
		switch v := value.(type) {
		case int, int64:
			return true
		case float64:
			return isFiniteAndInRange(v, math.MinInt64, math.MaxInt64) && v == math.Trunc(v)
		}
		return false
	case float64:
		switch v := value.(type) {
		case int, int64:
			return true
		case float64:
			return !math.IsNaN(v) && !math.IsInf(v, 0)
		}
		return false
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

// providerHostInt converts the public numeric representations accepted for an
// integer-native policy without routing typed integers through float64.
func providerHostInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		converted := int(v)
		return converted, int64(converted) == v
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		return safelyConvertFloatToHostInt(v)
	default:
		return 0, false
	}
}

// InvalidateUser drops the cached policy inputs for a single user. Task 5's
// adapter bridges InvalidateRole to InvalidateRolePolicies to satisfy
// plugin.EffectivePolicyInvalidator without changing this service's API.
func (s *Service) InvalidateUser(_ context.Context, userID string) error {
	s.InvalidateUserRoleCache(userID)
	return nil
}

// InvalidateRolePolicies drops all cached role and policy inputs. Conditional
// role membership cannot be enumerated from persisted assignments, so role
// invalidation is deliberately broader than the supplied role identifier.
func (s *Service) InvalidateRolePolicies(_ context.Context, roleID string) error {
	if roleID == "" {
		return nil
	}
	s.InvalidateAllRoleCaches()
	return nil
}
