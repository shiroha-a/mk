# Effective-policy Provider Timeout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Hangするeffective-policy resolverをproviderごと最大1 goroutineに封じ、1秒でnative policyへfallbackして再起動まで無効化する。

**Architecture:** 各provider runtimeにcapacity 1のtoken channelとatomic disabled flagを持たせる。呼び出しは1秒のdeadline内でtoken取得とresolver完了を待ち、timeout時はproviderを永久disableして既存failure fallbackへ合流する。duplicate provider名は登録時に拒否し、safemathのNaN境界も決定的にする。

**Tech Stack:** Go 1.26.6、context、sync/atomic、testify、Linux Docker

## Global Constraints

- Goのbuild/test/formatはLinux Docker内だけで実行し、Windows Goを使わない。
- provider timeoutは1秒固定で、設定項目を追加しない。
- timeout後のproviderはprocess再起動まで無効化し、自動retryしない。
- resolver goroutineをhard preemptionしない。
- timeout、panic、errorへplugin名、user/role/policy ID、output、panic値、内部errorを含めない。
- failed providerのdeclared keyは既存exact native snapshot fallbackへ合流する。
- contextを無視するresolverの残留goroutineはproviderごと最大1本にする。
- plugin outputはcacheしない。

---

### Task 1: Bound resolver execution and disable timed-out providers

**Files:**
- Modify: `internal/core/role/plugin_policy.go`
- Modify: `internal/core/role/plugin_policy_test.go`

**Interfaces:**
- Consumes: `plugin.EffectivePolicyResolver`
- Preserves: `func (s *Service) GetUserPoliciesChecked(string) (map[string]any, error)`
- Produces: provider-local runtime token and disabled state
- Preserves: `ErrEffectivePolicyProvider` and exact native fallback

- [ ] **Step 1: Write the failing cooperative-timeout test**

Add a test that registers a provider whose resolver waits for `ctx.Done()` and returns `ctx.Err()`. Call `GetUserPoliciesChecked("")`, assert it returns within two seconds, returns `ErrEffectivePolicyProvider`, and preserves the native value for the declared key.

```go
func TestEffectivePolicy_CooperativeTimeoutFallsBackToNative(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	registerProvider(t, svc, "slow", []string{"canSearchNotes"}, func(ctx context.Context, _ plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	started := time.Now()
	policies, err := svc.GetUserPoliciesChecked("")
	require.ErrorIs(t, err, role.ErrEffectivePolicyProvider)
	assert.Less(t, time.Since(started), 2*time.Second)
	assert.Equal(t, false, policies["canSearchNotes"])
}
```

- [ ] **Step 2: Write the failing non-cooperative/concurrency test**

Register one resolver that increments an atomic counter and blocks on a channel without reading its context. Start eight concurrent policy resolutions, assert all return within two seconds through the failure fallback, assert the resolver started exactly once, then call once more and assert it returns promptly without incrementing the counter. Close the blocking channel in cleanup.

```go
func TestEffectivePolicy_TimeoutDisablesProviderAndBoundsHang(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	var calls atomic.Int32
	registerProvider(t, svc, "hung", []string{"canSearchNotes"}, func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		calls.Add(1)
		<-block
		return nil, nil
	})

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			policies, err := svc.GetUserPoliciesChecked("u1")
			assert.ErrorIs(t, err, role.ErrEffectivePolicyProvider)
			assert.Equal(t, false, policies["canSearchNotes"])
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("policy resolution did not time out")
	}
	assert.Equal(t, int32(1), calls.Load())

	started := time.Now()
	_, err := svc.GetUserPoliciesChecked("u2")
	assert.ErrorIs(t, err, role.ErrEffectivePolicyProvider)
	assert.Less(t, time.Since(started), 250*time.Millisecond)
	assert.Equal(t, int32(1), calls.Load())
}
```

- [ ] **Step 3: Run the timeout tests to verify RED**

Run:

```powershell
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -race -count=1 ./internal/core/role -run 'CooperativeTimeout|TimeoutDisablesProvider'
```

Expected: cooperative test blocks past the test deadline or non-cooperative test reaches its two-second failure because no timeout runtime exists.

- [ ] **Step 4: Implement provider runtime state**

In `plugin_policy.go`, add:

```go
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

type policyProvider struct {
	name    string
	reg     plugin.EffectivePolicyRegistration
	runtime *policyProviderRuntime
}
```

Create the runtime when registration succeeds. Snapshot copies keep the runtime pointer so disabled state and token remain provider-local and shared across requests.

- [ ] **Step 5: Implement bounded invocation**

Change `invokePolicyProvider` to accept a `policyProvider`. Use a one-element buffered result channel. Acquire the token under the same one-second context, recheck disabled after acquisition, and only return the token from the resolver goroutine.

```go
type policyProviderResult struct {
	contributions []plugin.EffectivePolicyContribution
	ok            bool
}

func invokePolicyProvider(provider policyProvider, req plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, bool) {
	if provider.runtime.disabled.Load() {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), effectivePolicyProviderTimeout)
	defer cancel()

	select {
	case <-ctx.Done():
		provider.runtime.disabled.Store(true)
		return nil, false
	case <-provider.runtime.token:
	}
	if provider.runtime.disabled.Load() {
		provider.runtime.token <- struct{}{}
		return nil, false
	}

	result := make(chan policyProviderResult, 1)
	go func() {
		out := policyProviderResult{}
		defer func() {
			if recover() != nil {
				out = policyProviderResult{}
			}
			result <- out
			provider.runtime.token <- struct{}{}
		}()
		contributions, err := provider.reg.Resolve(ctx, req)
		out = policyProviderResult{contributions: contributions, ok: err == nil}
	}()

	select {
	case <-ctx.Done():
		provider.runtime.disabled.Store(true)
		return nil, false
	case out := <-result:
		return out.contributions, out.ok
	}
}
```

Update `resolvePolicies` to call `invokePolicyProvider(p, request)`. Do not log timeout details.

- [ ] **Step 6: Run timeout and existing failure tests**

Run:

```powershell
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -race -count=1 ./internal/core/role -run 'EffectivePolicy'
```

Expected: PASS, including panic/error/invalid-output/native-fallback tests.

- [ ] **Step 7: Commit Task 1**

```powershell
git add -- internal/core/role/plugin_policy.go internal/core/role/plugin_policy_test.go
```

### Task 2: Reject duplicate provider names

**Files:**
- Modify: `internal/core/role/plugin_policy.go`
- Modify: `internal/core/role/plugin_policy_test.go`

**Interfaces:**
- Preserves: `func (s *Service) RegisterEffectivePolicyProvider(string, plugin.EffectivePolicyRegistration) error`
- Produces: unique provider-name invariant

- [ ] **Step 1: Write the failing duplicate-name test**

Register the same name twice with independently valid registrations. Assert the second call returns an error and that only the first resolver contributes.

```go
func TestRegisterEffectivePolicyProvider_RejectsDuplicateName(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	first := plugin.EffectivePolicyRegistration{Keys: []string{"canSearchNotes"}, Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: true}}, nil
	}}
	second := plugin.EffectivePolicyRegistration{Keys: []string{"canSearchNotes"}, Resolve: func(context.Context, plugin.EffectivePolicyRequest) ([]plugin.EffectivePolicyContribution, error) {
		return []plugin.EffectivePolicyContribution{{Key: "canSearchNotes", Priority: 2, Value: false}}, nil
	}}
	require.NoError(t, svc.RegisterEffectivePolicyProvider("same", first))
	require.Error(t, svc.RegisterEffectivePolicyProvider("same", second))
	policies, err := svc.GetUserPoliciesChecked("u1")
	require.NoError(t, err)
	assert.Equal(t, true, policies["canSearchNotes"])
}
```

- [ ] **Step 2: Run the duplicate test to verify RED**

Run:

```powershell
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -race -count=1 ./internal/core/role -run TestRegisterEffectivePolicyProvider_RejectsDuplicateName
```

Expected: FAIL because both registrations are currently appended.

- [ ] **Step 3: Reject duplicates under the registry lock**

After acquiring `policyProviderMu.Lock`, scan `s.policyProviders` for the name. Return a fixed identifier-free error before append when found.

```go
for _, existing := range s.policyProviders {
	if existing.name == name {
		return errors.New("role: effective policy provider name is already registered")
	}
}
```

Keep `sort.Slice`; unique names make its ordering deterministic.

- [ ] **Step 4: Run registration and full role tests**

Run:

```powershell
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -race -count=1 ./internal/core/role
```

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

```powershell
git add -- internal/core/role/plugin_policy.go internal/core/role/plugin_policy_test.go
```

### Task 3: Define MulFloat64 NaN behavior

**Files:**
- Modify: `internal/safemath/safemath.go`
- Modify: `internal/safemath/safemath_test.go`

**Interfaces:**
- Preserves: `func MulFloat64(float64, int64) int64`
- Produces: deterministic `NaN -> 0` conversion

- [ ] **Step 1: Write the failing NaN test**

Add:

```go
assert.Equal(t, int64(0), MulFloat64(math.NaN(), 1024))
```

to `TestMulFloat64SaturatesAndPreservesNormalFractions`.

- [ ] **Step 2: Run safemath test to verify RED**

Run:

```powershell
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -count=1 ./internal/safemath
```

Expected: FAIL because the current conversion is implementation-dependent and is not guaranteed to return zero.

- [ ] **Step 3: Add the NaN guard**

At the start of `MulFloat64`:

```go
if math.IsNaN(value) {
	return 0
}
```

- [ ] **Step 4: Run amd64 and 386 tests**

Run:

```powershell
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -race -count=1 ./internal/safemath
docker run --rm -e GOARCH=386 -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -count=1 ./internal/safemath
```

Expected: both PASS.

- [ ] **Step 5: Commit Task 3**

```powershell
git add -- internal/safemath/safemath.go internal/safemath/safemath_test.go
```

### Task 4: Document the runtime contract and verify the PR

**Files:**
- Modify: `docs/plugins/authoring.md`
- Verify: `docs/design/effective-policy-provider-timeout.md`
- Verify: PR #2619 body and maintainer comment

**Interfaces:**
- Consumes: Tasks 1-3 behavior
- Produces: plugin-author timeout contract

- [ ] **Step 1: Update authoring documentation**

Add this paragraph to the effective-policy section:

```markdown
resolverの実行token取得から完了までの期限は1秒。期限を超えたproviderはprocess再起動まで無効化され、declared keyは既存のnative fallbackへ戻る。resolverへ渡されたcontextをStorage I/Oにも必ず渡すこと。contextを無視する処理はhostから強制終了できないが、hostは同じproviderの実行をcapacity 1に制限するため、timeout後に残留するresolver goroutineはproviderごと最大1本になる。
```

- [ ] **Step 2: Run final verification**

Fresh PostgreSQLを作成する。

```powershell
docker network create mk-policy-timeout-verify
docker run -d --name mk-policy-timeout-postgres --network mk-policy-timeout-verify -e POSTGRES_DB=misskey_test -e POSTGRES_USER=mk -e POSTGRES_PASSWORD=mk postgres:18-alpine
$deadline = (Get-Date).AddSeconds(60); do { docker exec mk-policy-timeout-postgres pg_isready -U mk -d misskey_test; if ($LASTEXITCODE -eq 0) { break }; Start-Sleep -Seconds 1 } while ((Get-Date) -lt $deadline)
```

Run:

```powershell
docker run --rm --user 65532:65532 --network mk-policy-timeout-verify -e GOCACHE=/tmp/gocache -e TEST_DB_HOST=mk-policy-timeout-postgres -e TEST_DB_PORT=5432 -e TEST_DB_NAME=misskey_test -e TEST_DB_USER=mk -e TEST_DB_PASS=mk -e TEST_DB_SSLMODE=disable -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod:ro -w /src golang:1.26.6-bookworm go test -race -count=1 -timeout 15m ./plugin/... ./internal/pluginspec ./internal/entitycompat ./internal/safemath ./internal/misc/id ./internal/core/role ./internal/api/invite ./internal/api/i ./internal/core/drive ./internal/server/middleware ./internal/server
docker run --rm -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm sh -c "go vet ./... && go build ./..."
docker run --rm -v "${PWD}:/src:ro" -w /src golang:1.26.6-bookworm sh -c "test -z \"$(gofmt -s -d .)\""
docker run --rm -e GOARCH=386 -v "${PWD}:/src:ro" -v mk-go-mod:/go/pkg/mod -v mk-go-build:/root/.cache/go-build -w /src golang:1.26.6-bookworm go test -count=1 ./internal/safemath ./internal/core/role
git diff --check
```

Finally remove disposable resources:

```powershell
docker rm -f mk-policy-timeout-postgres
docker network rm mk-policy-timeout-verify
```

- [ ] **Step 3: Request final code review**

Review timeout races, token return ownership, maximum residual goroutine count, fallback scope, identifier redaction, duplicate registry behavior, NaN conversion, and 32-bit behavior. Fix every Critical/Important finding and rerun affected tests.

- [ ] **Step 4: Commit docs**

```powershell
git add -- docs/plugins/authoring.md
```

- [ ] **Step 5: Push and update PR #2619**

Push normally without force. Update the PR body with the one-second timeout, restart-required disable, duplicate-name rejection, NaN behavior, and fresh verification. Reply to the maintainer comment with commit hashes and the distinction between cooperative context cancellation and bounded non-cooperative execution.
