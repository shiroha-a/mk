package role

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shiroha-a/mk/internal/effectivepolicy"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/plugin"
)

// ErrEffectivePolicyProvider is returned by GetUserPoliciesChecked when a
// registered effective-policy provider fails. Fixed and identifier-free:
// the underlying provider error is never surfaced to the caller.
var ErrEffectivePolicyProvider = errors.New("effective policy provider failed")

const effectivePolicyProviderTimeout = time.Second
const defaultEffectivePolicyProviderCacheEntries = 10_000

type policyProviderRuntime struct {
	token     chan struct{}
	disabled  atomic.Bool
	fallbacks atomic.Uint64

	cacheMu      sync.Mutex
	cache        map[policyProviderCacheKey]*list.Element
	cacheLRU     list.List
	cacheEntries int
	flights      map[policyProviderCacheKey]*policyProviderFlight
	userEpoch    map[string]uint64
	globalEpoch  uint64
}

func newPolicyProviderRuntime(cacheEntries int) *policyProviderRuntime {
	if cacheEntries <= 0 {
		cacheEntries = defaultEffectivePolicyProviderCacheEntries
	}
	runtime := &policyProviderRuntime{
		token:        make(chan struct{}, 1),
		cache:        make(map[policyProviderCacheKey]*list.Element),
		cacheEntries: cacheEntries,
		flights:      make(map[policyProviderCacheKey]*policyProviderFlight),
		userEpoch:    make(map[string]uint64),
	}
	runtime.token <- struct{}{}
	return runtime
}

type policyProviderCacheKey struct {
	userID  string
	roleIDs string
}

type policyProviderCacheEntry struct {
	key           policyProviderCacheKey
	contributions []plugin.EffectivePolicyContribution
}

type policyProviderFlight struct {
	done          chan struct{}
	contributions []plugin.EffectivePolicyContribution
	ok            bool
	userEpoch     uint64
	globalEpoch   uint64
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
	if err := effectivepolicy.ValidateRegistration(reg); err != nil {
		return err
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
	providers = append(providers, policyProvider{name: name, reg: reg, runtime: newPolicyProviderRuntime(s.effectivePolicyProviderCacheEntries)})
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
// ErrEffectivePolicyProvider. Successful provider output is stored in a
// bounded per-provider LRU; plugins must explicitly invalidate affected inputs
// after committed state changes.
func (s *Service) GetUserPoliciesChecked(userID string) (map[string]any, error) {
	return s.resolvePolicies(userID)
}

// resolvePolicies computes effective policies for userID, invoking registered
// providers after native roles are resolved and before server caps are
// applied. Provider failures return ErrEffectivePolicyProvider with the failed
// providers' keys restored to native results. Native role-input failures return
// their wrapped repository error without invoking providers.
func (s *Service) resolvePolicies(userID string) (map[string]any, error) {
	providers := s.snapshotPolicyProviders()
	// applyMetaBasePolicies が base を mutate するため共有 cache ではなく clone を使う。
	base := DefaultPoliciesClone()
	s.applyMetaBasePolicies(base)
	if userID == "" && len(providers) == 0 {
		return s.applyServerCaps(base), nil
	}

	roles := []*model.Role{}
	if userID != "" {
		var err error
		roles, err = s.GetUserRoles(userID)
		if err != nil {
			return s.applyServerCaps(base), fmt.Errorf("role: effective policy inputs: %w", err)
		}
	}
	if len(roles) == 0 && len(providers) == 0 {
		return s.applyServerCaps(base), nil
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
	if len(providers) == 0 {
		return s.applyServerCaps(out), nil
	}
	native := make(map[string]any, len(out))
	for key, value := range out {
		native[key] = clonePolicyValue(value)
	}

	// provider には現在 active な native RoleID のみを、ソート + clone して渡す。
	roleIDs := activeRoleIDs(roles)

	// key -> provider contribution entries (同一 priority cascade に参加させる)。
	contribs := make(map[string][]policyEntry)
	// 失敗したproviderの宣言keyはplugin貢献をすべて破棄してnative結果へ戻す。
	failed := make(map[string]bool)

	type resolvedProvider struct {
		contributions []plugin.EffectivePolicyContribution
		ok            bool
	}
	resolved := make([]resolvedProvider, len(providers))
	var providersWG sync.WaitGroup
	for i, p := range providers {
		providersWG.Add(1)
		go func() {
			defer providersWG.Done()
			providerRoleIDs := make([]string, len(roleIDs))
			copy(providerRoleIDs, roleIDs)
			resolved[i].contributions, resolved[i].ok = resolvePolicyProviderCached(
				p,
				plugin.EffectivePolicyRequest{UserID: userID, RoleIDs: providerRoleIDs},
			)
		}()
	}
	providersWG.Wait()

	for i, p := range providers {
		res, ok := resolved[i].contributions, resolved[i].ok
		if !ok {
			recordPolicyProviderFallback(p.runtime)
			for _, k := range p.reg.Keys {
				failed[k] = true
			}
			continue
		}
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

func recordPolicyProviderFallback(runtime *policyProviderRuntime) {
	failures := runtime.fallbacks.Add(1)
	// 1, 2, 4, 8...回だけ記録し、恒常障害でもlog volumeを有界に近づける。
	if failures&(failures-1) == 0 {
		slog.Warn("effective policy provider fallback", "failures", failures)
	}
}

func resolvePolicyProviderCached(provider policyProvider, req plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, bool) {
	if provider.runtime.disabled.Load() {
		return nil, false
	}
	key := policyProviderCacheKey{userID: req.UserID, roleIDs: encodePolicyProviderRoleIDs(req.RoleIDs)}

	var flight *policyProviderFlight
	for {
		provider.runtime.cacheMu.Lock()
		if provider.runtime.disabled.Load() {
			provider.runtime.cacheMu.Unlock()
			return nil, false
		}
		if cached, ok := provider.runtime.cacheGet(key); ok {
			provider.runtime.cacheMu.Unlock()
			return clonePolicyContributions(cached), true
		}
		if existing := provider.runtime.flights[key]; existing != nil && policyProviderFlightIsCurrent(provider.runtime, req.UserID, existing) {
			provider.runtime.cacheMu.Unlock()
			contributions, ok, _ := waitPolicyProviderFlight(existing, true)
			return contributions, ok
		}
		flight = &policyProviderFlight{
			done:        make(chan struct{}),
			userEpoch:   provider.runtime.userEpoch[req.UserID],
			globalEpoch: provider.runtime.globalEpoch,
		}
		provider.runtime.flights[key] = flight
		provider.runtime.cacheMu.Unlock()
		break
	}

	contributions, ok := invokePolicyProvider(provider, req, key, flight)
	if ok {
		ok = effectivepolicy.ValidateContributions(provider.reg.Keys, contributions)
	}
	if ok {
		contributions = clonePolicyContributions(contributions)
		sort.Slice(contributions, func(i, j int) bool {
			return lessPolicyContribution(contributions[i], contributions[j])
		})
	}

	provider.runtime.cacheMu.Lock()
	flight.contributions = clonePolicyContributions(contributions)
	flight.ok = ok
	if ok && !provider.runtime.disabled.Load() &&
		provider.runtime.globalEpoch == flight.globalEpoch &&
		provider.runtime.userEpoch[req.UserID] == flight.userEpoch {
		provider.runtime.cachePut(key, contributions)
	}
	if provider.runtime.flights[key] == flight {
		delete(provider.runtime.flights, key)
	}
	if !policyProviderHasUserFlight(provider.runtime, req.UserID) {
		delete(provider.runtime.userEpoch, req.UserID)
	}
	close(flight.done)
	provider.runtime.cacheMu.Unlock()
	return clonePolicyContributions(contributions), ok
}

// policyProviderFlightIsCurrent reports whether flight belongs to the current
// invalidation generation. The caller must hold runtime.cacheMu.
func policyProviderFlightIsCurrent(runtime *policyProviderRuntime, userID string, flight *policyProviderFlight) bool {
	return runtime.globalEpoch == flight.globalEpoch && runtime.userEpoch[userID] == flight.userEpoch
}

func policyProviderFlightOwnsCurrent(runtime *policyProviderRuntime, key policyProviderCacheKey, userID string, flight *policyProviderFlight) bool {
	runtime.cacheMu.Lock()
	defer runtime.cacheMu.Unlock()
	return runtime.flights[key] == flight && policyProviderFlightIsCurrent(runtime, userID, flight)
}

func waitPolicyProviderFlight(flight *policyProviderFlight, current bool) ([]plugin.EffectivePolicyContribution, bool, bool) {
	<-flight.done
	if !current {
		return nil, false, false
	}
	return clonePolicyContributions(flight.contributions), flight.ok, true
}

// cacheGet returns the immutable cached snapshot and updates recency. The
// caller must hold runtime.cacheMu.
func (runtime *policyProviderRuntime) cacheGet(key policyProviderCacheKey) ([]plugin.EffectivePolicyContribution, bool) {
	element := runtime.cache[key]
	if element == nil {
		return nil, false
	}
	runtime.cacheLRU.MoveToFront(element)
	return element.Value.(*policyProviderCacheEntry).contributions, true
}

// cachePut stores a cloned snapshot and enforces the configured LRU limit. The
// caller must hold runtime.cacheMu.
func (runtime *policyProviderRuntime) cachePut(key policyProviderCacheKey, contributions []plugin.EffectivePolicyContribution) {
	cloned := clonePolicyContributions(contributions)
	if element := runtime.cache[key]; element != nil {
		entry := element.Value.(*policyProviderCacheEntry)
		entry.contributions = cloned
		runtime.cacheLRU.MoveToFront(element)
		return
	}
	element := runtime.cacheLRU.PushFront(&policyProviderCacheEntry{key: key, contributions: cloned})
	runtime.cache[key] = element
	runtime.trimCache()
}

// trimCache evicts least-recently-used entries. The caller must hold cacheMu.
func (runtime *policyProviderRuntime) trimCache() {
	for runtime.cacheLRU.Len() > runtime.cacheEntries {
		element := runtime.cacheLRU.Back()
		entry := element.Value.(*policyProviderCacheEntry)
		delete(runtime.cache, entry.key)
		runtime.cacheLRU.Remove(element)
	}
}

// cacheDeleteUser removes every role-input variant for userID. The caller must
// hold runtime.cacheMu.
func (runtime *policyProviderRuntime) cacheDeleteUser(userID string) {
	for key, element := range runtime.cache {
		if key.userID != userID {
			continue
		}
		delete(runtime.cache, key)
		runtime.cacheLRU.Remove(element)
	}
}

// cacheClear resets both LRU indexes. The caller must hold runtime.cacheMu.
func (runtime *policyProviderRuntime) cacheClear() {
	clear(runtime.cache)
	runtime.cacheLRU.Init()
}

func policyProviderHasUserFlight(runtime *policyProviderRuntime, userID string) bool {
	for key := range runtime.flights {
		if key.userID == userID {
			return true
		}
	}
	return false
}

func encodePolicyProviderRoleIDs(roleIDs []string) string {
	var encoded strings.Builder
	for _, roleID := range roleIDs {
		encoded.WriteString(strconv.Itoa(len(roleID)))
		encoded.WriteByte(':')
		encoded.WriteString(roleID)
	}
	return encoded.String()
}

func clonePolicyContributions(contributions []plugin.EffectivePolicyContribution) []plugin.EffectivePolicyContribution {
	if contributions == nil {
		return nil
	}
	cloned := make([]plugin.EffectivePolicyContribution, len(contributions))
	copy(cloned, contributions)
	for i := range cloned {
		if cloned[i].UseDefault {
			cloned[i].Value = nil
			continue
		}
		cloned[i].Value = clonePolicyValue(cloned[i].Value)
	}
	return cloned
}

type policyProviderResult struct {
	contributions []plugin.EffectivePolicyContribution
	ok            bool
	completedAt   time.Time
}

func invokePolicyProvider(provider policyProvider, req plugin.EffectivePolicyRequest, key policyProviderCacheKey, flight *policyProviderFlight) ([]plugin.EffectivePolicyContribution, bool) {
	if provider.runtime.disabled.Load() {
		return nil, false
	}

	waitCtx, cancelWait := context.WithTimeout(context.Background(), effectivePolicyProviderTimeout)
	if !acquireEnabledPolicyProviderToken(waitCtx, provider.runtime) {
		cancelWait()
		return nil, false
	}
	cancelWait()
	if !policyProviderFlightOwnsCurrent(provider.runtime, key, req.UserID, flight) {
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
			finishPolicyProviderInvocation(resolveCtx, provider.runtime, result, out)
		}()
		contributions, err := provider.reg.Resolve(resolveCtx, req)
		out = policyProviderResult{contributions: contributions, ok: err == nil}
	}()

	out, completedBeforeDeadline := receivePolicyProviderResult(resolveCtx, result)
	if !completedBeforeDeadline {
		disablePolicyProvider(provider.runtime)
		return nil, false
	}
	return out.contributions, out.ok
}

func acquireEnabledPolicyProviderToken(ctx context.Context, runtime *policyProviderRuntime) bool {
	if !acquirePolicyProviderToken(ctx, runtime) {
		return false
	}
	if runtime.disabled.Load() {
		runtime.token <- struct{}{}
		return false
	}
	return true
}

func finishPolicyProviderInvocation(ctx context.Context, runtime *policyProviderRuntime, result chan<- policyProviderResult, out policyProviderResult) {
	out.completedAt = time.Now()
	if deadline, ok := ctx.Deadline(); ok && !out.completedAt.Before(deadline) {
		disablePolicyProvider(runtime)
	}
	result <- out
	runtime.token <- struct{}{}
}

func disablePolicyProvider(runtime *policyProviderRuntime) {
	runtime.cacheMu.Lock()
	disabled := runtime.disabled.CompareAndSwap(false, true)
	if disabled {
		runtime.globalEpoch++
		runtime.cacheClear()
	}
	runtime.cacheMu.Unlock()
	if disabled {
		slog.Warn("effective policy provider disabled after timeout")
	}
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

// InvalidateUser drops the cached policy inputs for a single user. Task 5's
// adapter bridges InvalidateRole to InvalidateRolePolicies to satisfy
// plugin.EffectivePolicyInvalidator without changing this service's API.
func (s *Service) InvalidateUser(_ context.Context, userID string) error {
	s.invalidateUserPolicyCaches(userID)
	return nil
}

func (s *Service) invalidateUserPolicyCaches(userID string) {
	s.InvalidateUserRoleCache(userID)
	if userID == "" {
		return
	}
	for _, provider := range s.snapshotPolicyProviders() {
		provider.runtime.cacheMu.Lock()
		if policyProviderHasUserFlight(provider.runtime, userID) {
			provider.runtime.userEpoch[userID]++
		} else {
			delete(provider.runtime.userEpoch, userID)
		}
		provider.runtime.cacheDeleteUser(userID)
		provider.runtime.cacheMu.Unlock()
	}
}

// InvalidateRolePolicies drops all cached role and policy inputs. Conditional
// role membership cannot be enumerated from persisted assignments, so role
// invalidation is deliberately broader than the supplied role identifier.
func (s *Service) InvalidateRolePolicies(_ context.Context, roleID string) error {
	s.invalidateRolePolicyCaches(roleID)
	return nil
}

func (s *Service) invalidateRolePolicyCaches(roleID string) {
	if roleID == "" {
		return
	}
	s.InvalidateAllRoleCaches()
	for _, provider := range s.snapshotPolicyProviders() {
		provider.runtime.cacheMu.Lock()
		provider.runtime.globalEpoch++
		provider.runtime.cacheClear()
		clear(provider.runtime.userEpoch)
		provider.runtime.cacheMu.Unlock()
	}
}
