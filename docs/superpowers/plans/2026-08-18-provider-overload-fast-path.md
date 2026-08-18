# Provider Overload and Native Fast Path Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 健全なeffective-policy providerをburstだけで永久disableせず、provider未登録instanceのnative policy fast pathを復元する。

**Architecture:** capacity 1 tokenは維持するが、token取得待ちとresolver実行へ独立した1秒deadlineを割り当てる。token待ちtimeoutはrequest-local fallback、resolver timeoutだけをprocess-lifetime disableとする。provider未登録時は全key集約とfailure snapshotを作らず、既存のdeep-cloned baseとnative early returnを使う。

**Tech Stack:** Go 1.26.6、context、sync/atomic、testify、Linux Docker

## Global Constraints

- Goのbuild、test、formatはLinux Docker内だけで実行する。
- token取得待ちとresolver実行の期限はそれぞれ1秒固定とし、設定項目を追加しない。
- token待ちtimeoutではproviderをdisableせず、そのrequestだけexact native fallbackへ戻す。
- resolver実行timeoutだけprocess再起動までproviderをdisableする。
- contextを無視するresolverの残留goroutineはproviderごとに最大1本にする。
- resolver goroutineだけが実行tokenを返す。deadline以後の完了ではtoken返却前にdisableする。
- provider outputはcacheしない。
- provider未登録時も返却sliceのmutation isolationを維持し、baseのdeep cloneを外さない。
- timeout、panic、errorへplugin名、user/role/policy ID、provider output、panic値、内部errorを含めない。
- commitとpushはnormal操作だけを使い、force-pushしない。

---

### Task 1: Separate token-wait and resolver deadlines

**Files:**
- Modify: `internal/core/role/plugin_policy.go:212-300`
- Modify: `internal/core/role/plugin_policy_test.go:533-593`
- Modify: `internal/core/role/plugin_policy_internal_test.go`

**Interfaces:**
- Preserves: `func invokePolicyProvider(policyProvider, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, bool)`
- Preserves: `func acquirePolicyProviderToken(context.Context, *policyProviderRuntime) bool`
- Produces: request-local token-wait fallback and resolver-only permanent disable

- [ ] **Step 1: Write the failing overload regression test**

Add a test that runs twenty concurrent requests against a healthy 100ms resolver. Some requests may use native fallback after waiting one second, but a post-burst request must invoke the resolver successfully.

```go
func TestEffectivePolicy_TokenWaitTimeoutDoesNotDisableHealthyProvider(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	var calls atomic.Int32
	registerProvider(t, svc, "healthy", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		calls.Add(1)
		time.Sleep(100 * time.Millisecond)
		return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
	})

	start := make(chan struct{})
	var wg sync.WaitGroup
	var failures atomic.Int32
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.GetUserPoliciesChecked("")
			if err != nil {
				failures.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	assert.Positive(t, failures.Load())

	before := calls.Load()
	policies, err := svc.GetUserPoliciesChecked("")
	require.NoError(t, err)
	assert.Equal(t, true, policies["canSearchNotes"])
	assert.Equal(t, before+1, calls.Load())
}
```

- [ ] **Step 2: Write the failing independent-deadline test**

The first request holds the token for 800ms. The second resolver takes 400ms. The second call must succeed because its resolver gets a fresh one-second execution budget after token acquisition.

```go
func TestEffectivePolicy_ResolverGetsFreshDeadlineAfterTokenWait(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	registerProvider(t, svc, "queued", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return nil, nil
		}
		time.Sleep(400 * time.Millisecond)
		return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
	})

	first := make(chan error, 1)
	go func() {
		_, err := svc.GetUserPoliciesChecked("")
		first <- err
	}()
	<-started
	time.AfterFunc(800*time.Millisecond, func() { close(release) })

	policies, err := svc.GetUserPoliciesChecked("")
	require.NoError(t, <-first)
	require.NoError(t, err)
	assert.Equal(t, true, policies["canSearchNotes"])
}
```

- [ ] **Step 3: Run the new tests to verify RED**

Run:

```powershell
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -race -count=1 ./internal/core/role -run 'TokenWaitTimeoutDoesNotDisable|ResolverGetsFreshDeadline'
```

Expected: the post-burst request returns `ErrEffectivePolicyProvider`, and the queued resolver loses the shared original deadline.

- [ ] **Step 4: Split the wait and execution contexts**

In `invokePolicyProvider`, acquire the token with a wait context. A failed acquisition returns failure without changing `disabled`. Cancel that context immediately after acquisition, recheck `disabled`, then create the resolver execution context.

```go
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
```

Pass `resolveCtx` to `provider.reg.Resolve` and `receivePolicyProviderResult`.

- [ ] **Step 5: Disable before returning a late resolver's token**

In the resolver goroutine defer, capture completion once. If it is not before the resolver context deadline, set `disabled` before sending the result and returning the token.

```go
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
```

Keep the caller-side disable when `receivePolicyProviderResult` reports completion at or after the execution deadline. Both writes are idempotent.

- [ ] **Step 6: Run timeout and provider tests to verify GREEN**

Run:

```powershell
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -race -count=1 ./internal/core/role -run 'EffectivePolicy|AcquirePolicyProviderToken|ReceivePolicyProviderResult'
```

Expected: PASS.

- [ ] **Step 7: Commit Task 1**

```powershell
git add -- internal/core/role/plugin_policy.go internal/core/role/plugin_policy_test.go internal/core/role/plugin_policy_internal_test.go
git commit -m "Fix policy: token待ちtimeoutを一時fallbackにする"
```

### Task 2: Restore the provider-free native fast path

**Files:**
- Modify: `internal/core/role/plugin_policy.go:119-203`
- Modify: `internal/core/role/plugin_policy_test.go`
- Verify: `internal/core/role/role_service_test.go:1032-1044`

**Interfaces:**
- Preserves: `func (s *Service) GetUserPolicies(string) map[string]any`
- Preserves: `func (s *Service) GetUserPoliciesChecked(string) (map[string]any, error)`
- Preserves: native slice mutation isolation

- [ ] **Step 1: Add the failing anonymous allocation regression test**

```go
func TestEffectivePolicy_NoProviderAnonymousFastPathAllocations(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	cloneAllocs := testing.AllocsPerRun(100, func() {
		_ = role.DefaultPoliciesClone()
	})
	policyAllocs := testing.AllocsPerRun(100, func() {
		_ = svc.GetUserPolicies("")
	})
	assert.Equal(t, cloneAllocs, policyAllocs)
}
```

- [ ] **Step 2: Add a reproducible benchmark**

```go
func BenchmarkEffectivePolicy_NoProviderAnonymous(b *testing.B) {
	roleRepo := testutil.NewMockRoleRepository()
	assignRepo := testutil.NewMockRoleAssignmentRepository(roleRepo)
	metaRepo := testutil.NewMockMetaRepository()
	idGen, err := id.NewGenerator("aidx")
	require.NoError(b, err)
	svc := role.NewService(roleRepo, assignRepo, metaRepo, idGen)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = svc.GetUserPolicies("")
	}
}
```

- [ ] **Step 3: Verify the allocation test is RED and record the benchmark**

Run:

```powershell
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -count=1 ./internal/core/role -run TestEffectivePolicy_NoProviderAnonymousFastPathAllocations
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -run '^$' -bench BenchmarkEffectivePolicy_NoProviderAnonymous -benchmem -count=5 ./internal/core/role
```

Expected: allocation assertion FAILS because `out` and `native` maps are both allocated before the provider-empty check.

- [ ] **Step 4: Move provider detection before native aggregation**

Snapshot providers before choosing early returns, but retain `DefaultPoliciesClone` for return-value isolation.

```go
	providers := s.snapshotPolicyProviders()
	base := DefaultPoliciesClone()
	s.applyMetaBasePolicies(base)

	if userID == "" && len(providers) == 0 {
		return s.applyServerCaps(base), nil
	}
```

After role lookup and before `roleOverrides` allocation:

```go
	if len(roles) == 0 && len(providers) == 0 {
		return s.applyServerCaps(base), nil
	}
```

After computing `out`, return before the failure snapshot when no providers exist:

```go
	if len(providers) == 0 {
		return s.applyServerCaps(out), nil
	}
	native := make(map[string]any, len(out))
	for key, value := range out {
		native[key] = clonePolicyValue(value)
	}
```

Do not replace `DefaultPoliciesClone` with `maps.Clone`; the existing native slice mutation test requires a deep base copy.

- [ ] **Step 5: Verify behavior, isolation, allocations, and benchmark**

Run:

```powershell
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -race -count=1 ./internal/core/role -run 'NoProviderAnonymousFastPathAllocations|NativeSliceMutationIsIsolated|EffectivePolicy'
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -run '^$' -bench BenchmarkEffectivePolicy_NoProviderAnonymous -benchmem -count=5 ./internal/core/role
```

Expected: tests PASS; benchmark no longer includes full native aggregation or the second deep-cloned map.

- [ ] **Step 6: Commit Task 2**

```powershell
git add -- internal/core/role/plugin_policy.go internal/core/role/plugin_policy_test.go
git commit -m "Fix policy: provider未登録fast pathを復元する"
```

### Task 3: Update contracts and verify PR #2619

**Files:**
- Modify: `docs/plugins/authoring.md:197`
- Verify: `docs/design/effective-policy-provider-timeout.md`
- Update: Issue #2608 and PR #2619

**Interfaces:**
- Consumes: Task 1 overload behavior and Task 2 fast path
- Produces: public authoring contract and review response

- [ ] **Step 1: Update authoring documentation**

Replace the combined-deadline paragraph with:

```markdown
resolverの実行token取得待ちとresolver実行の期限はそれぞれ1秒。tokenを期限内に取得できないrequestはnative fallbackへ戻るが、providerは無効化されない。token取得後にresolver専用の新しい1秒deadlineが始まり、この実行期限を超えたproviderだけがprocess再起動まで無効化される。resolverへ渡されたcontextをStorage I/Oにも必ず渡すこと。contextを無視する処理はhostから強制終了できないが、hostは同じproviderの実行をcapacity 1に制限するため、timeout後に残留するresolver goroutineはproviderごとに最大1本になる。
```

- [ ] **Step 2: Run final verification with fresh PostgreSQL**

Create a disposable PostgreSQL 18 container:

```powershell
docker network create mk-policy-overload-verify
docker run -d --name mk-policy-overload-postgres --network mk-policy-overload-verify -e POSTGRES_DB=misskey_test -e POSTGRES_USER=mk -e POSTGRES_PASSWORD=mk postgres:18-alpine
$deadline = (Get-Date).AddSeconds(60); do { docker exec mk-policy-overload-postgres pg_isready -U mk -d misskey_test; if ($LASTEXITCODE -eq 0) { break }; Start-Sleep -Seconds 1 } while ((Get-Date) -lt $deadline)
```

Run:

```powershell
docker run --rm --user 65532:65532 --network mk-policy-overload-verify -e GOCACHE=/tmp/gocache -e TEST_DB_HOST=mk-policy-overload-postgres -e TEST_DB_PORT=5432 -e TEST_DB_NAME=misskey_test -e TEST_DB_USER=mk -e TEST_DB_PASS=mk -e TEST_DB_SSLMODE=disable -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod:ro -w /src golang:1.26.6-bookworm go test -race -count=1 -timeout 15m ./plugin/... ./internal/pluginspec ./internal/entitycompat ./internal/safemath ./internal/misc/id ./internal/core/role ./internal/api/invite ./internal/api/i ./internal/core/drive ./internal/server/middleware ./internal/server
docker run --rm -e GOARCH=386 -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -count=1 ./internal/safemath ./internal/core/role
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm sh -c "go vet ./... && go build ./..."
docker run --rm -v "${PWD}:/src:ro" -w /src golang:1.26.6-bookworm sh -c 'test -z "$(gofmt -s -d .)"'
git diff --check
```

Remove resources:

```powershell
docker rm -f mk-policy-overload-postgres
docker network rm mk-policy-overload-verify
```

Expected: all affected tests and static checks PASS; no disposable resources remain.

- [ ] **Step 3: Request final code review**

Review overload behavior, separate deadlines, token ownership, late completion disable ordering, maximum residual goroutine count, provider-free allocations, native slice isolation, and fallback scope. Fix every Critical or Important finding and rerun affected tests.

- [ ] **Step 4: Commit docs**

```powershell
git add -- docs/plugins/authoring.md
git commit -m "Docs plugin: overload時のfallback契約を追記する"
```

- [ ] **Step 5: Update Issue and PR**

Update #2608 to include separate token/resolver deadlines, request-local token fallback, provider-free fast path, and checked completion items. Update #2619 body and reply to the latest maintainer comment with commit hashes, benchmark before/after results, focused verification, and the reason deep base cloning remains required by native slice mutation isolation.

- [ ] **Step 6: Push normally**

```powershell
git push origin feature/plugin-effective-policy-provider
```

Expected: local and remote branch heads match without force-push.
