package event

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
	"github.com/shiroha-a/mk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testRedis *testutil.TestRedis

func TestMain(m *testing.M) {
	ctx := context.Background()
	var err error
	testRedis, err = testutil.SetupRedis(ctx)
	if err != nil {
		log.Fatalf("failed to setup redis: %v", err)
	}

	code := m.Run()

	testRedis.Teardown(ctx)
	os.Exit(code)
}

func TestPubSubService_PublishSubscribe(t *testing.T) {
	svc := NewPubSubService(testRedis.Client, "test:")
	ctx := context.Background()
	defer svc.Close()

	type msg struct {
		Text string `json:"text"`
	}

	var mu sync.Mutex
	var received []msg

	svc.Subscribe(ctx, "chan1", func(data []byte) {
		var m msg
		if err := json.Unmarshal(data, &m); err == nil {
			mu.Lock()
			received = append(received, m)
			mu.Unlock()
		}
	})

	// サブスクリプションが確立されるのを待つ
	time.Sleep(100 * time.Millisecond)

	require.NoError(t, svc.Publish(ctx, "chan1", msg{Text: "hello"}))
	require.NoError(t, svc.Publish(ctx, "chan1", msg{Text: "world"}))

	// メッセージの受信を待つ
	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 2
	}, 2*time.Second, 50*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "hello", received[0].Text)
	assert.Equal(t, "world", received[1].Text)
}

func TestPubSubService_Unsubscribe(t *testing.T) {
	svc := NewPubSubService(testRedis.Client, "test:")
	ctx := context.Background()
	defer svc.Close()

	var count int
	var mu sync.Mutex

	svc.Subscribe(ctx, "chan2", func(data []byte) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	time.Sleep(100 * time.Millisecond)

	require.NoError(t, svc.Unsubscribe("chan2"))

	// Unsubscribe後のメッセージは受信されない
	time.Sleep(50 * time.Millisecond)
	_ = svc.Publish(ctx, "chan2", "after_unsub")

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	assert.Equal(t, 0, count)
	mu.Unlock()
}

func TestPubSubService_MultipleChannels(t *testing.T) {
	svc := NewPubSubService(testRedis.Client, "test:")
	ctx := context.Background()
	defer svc.Close()

	var mu sync.Mutex
	results := make(map[string]string)

	svc.Subscribe(ctx, "a", func(data []byte) {
		mu.Lock()
		results["a"] = string(data)
		mu.Unlock()
	})
	svc.Subscribe(ctx, "b", func(data []byte) {
		mu.Lock()
		results["b"] = string(data)
		mu.Unlock()
	})

	time.Sleep(100 * time.Millisecond)

	require.NoError(t, svc.Publish(ctx, "a", "msg_a"))
	require.NoError(t, svc.Publish(ctx, "b", "msg_b"))

	assert.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(results) >= 2
	}, 2*time.Second, 50*time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, results["a"], "msg_a")
	assert.Contains(t, results["b"], "msg_b")
}

func TestPubSubService_Unsubscribe_NotSubscribed(t *testing.T) {
	svc := NewPubSubService(testRedis.Client, "test:")
	defer svc.Close()

	// 未登録チャンネルのUnsubscribeはエラーなし
	err := svc.Unsubscribe("nonexistent")
	assert.NoError(t, err)
}

func TestPubSubService_Close_WithSubscriptions(t *testing.T) {
	svc := NewPubSubService(testRedis.Client, "test:")
	ctx := context.Background()

	svc.Subscribe(ctx, "close1", func(data []byte) {})
	svc.Subscribe(ctx, "close2", func(data []byte) {})

	time.Sleep(100 * time.Millisecond)

	err := svc.Close()
	assert.NoError(t, err)
}

func TestPubSubService_Close_AlreadyClosed(t *testing.T) {
	svc := NewPubSubService(testRedis.Client, "test:")
	ctx := context.Background()

	svc.Subscribe(ctx, "doublecl", func(data []byte) {})
	time.Sleep(100 * time.Millisecond)

	// 内部のPubSubを手動で閉じてからCloseを呼ぶ→sub.Close()がエラーを返す
	svc.mu.Lock()
	for _, sub := range svc.subs {
		sub.Close()
	}
	svc.mu.Unlock()

	// svc.Close()は内部でsub.Close()エラーをログに出すが、nilを返す
	err := svc.Close()
	assert.NoError(t, err)
}

func TestPubSubService_Publish_MarshalError(t *testing.T) {
	svc := NewPubSubService(testRedis.Client, "test:")
	ctx := context.Background()
	defer svc.Close()

	// chanがJSONにmarshalできない
	err := svc.Publish(ctx, "ch", make(chan int))
	assert.Error(t, err)
}

// crossWorkerFakeMeta is a minimal MetaRepository inner used to count DB fetches
// in the cross-worker invalidation e2e (#1740).
type crossWorkerFakeMeta struct {
	fetchCount atomic.Int32
	meta       *model.Meta
}

func (r *crossWorkerFakeMeta) Fetch() (*model.Meta, error) {
	r.fetchCount.Add(1)
	return r.meta, nil
}
func (r *crossWorkerFakeMeta) Update(map[string]any) error { return nil }
func (r *crossWorkerFakeMeta) EnsureInitial(string) error  { return nil }

// TestPubSubService_CrossWorkerMetaInvalidation は #1740 の核となる合成を検証する:
// worker A が meta を更新すると internal:metaUpdated が publish され、worker B が
// 受信して自プロセスの CachedMetaRepository を invalidate し、次の Fetch で
// 再取得する (= cross-worker で default policy 変更等が伝播する)。
func TestPubSubService_CrossWorkerMetaInvalidation(t *testing.T) {
	ctx := context.Background()
	innerA := &crossWorkerFakeMeta{meta: &model.Meta{ID: "m1"}}
	innerB := &crossWorkerFakeMeta{meta: &model.Meta{ID: "m1"}}
	cachedA := repository.NewCachedMetaRepositoryWithTTL(innerA, time.Hour)
	cachedB := repository.NewCachedMetaRepositoryWithTTL(innerB, time.Hour)

	psA := NewPubSubService(testRedis.Client, "internal_e2e:")
	psB := NewPubSubService(testRedis.Client, "internal_e2e:")
	defer psA.Close()
	defer psB.Close()

	// worker A は更新時に metaUpdated を publish する。
	cachedA.SetInvalidationHook(func() { _ = psA.Publish(ctx, "metaUpdated", struct{}{}) })
	// 両 worker が購読し、受信で自 cache を invalidate する。
	psA.Subscribe(ctx, "metaUpdated", func([]byte) { cachedA.Invalidate() })
	psB.Subscribe(ctx, "metaUpdated", func([]byte) { cachedB.Invalidate() })
	time.Sleep(100 * time.Millisecond) // subscription 登録待ち

	// 両 cache を温める。
	_, _ = cachedA.Fetch()
	_, _ = cachedB.Fetch()
	require.Equal(t, int32(1), innerB.fetchCount.Load())

	// worker A が更新 → publish → worker B が invalidate。
	require.NoError(t, cachedA.Update(map[string]any{"name": "x"}))

	// B は受信後 cache が drop され、次の Fetch で再取得する。
	require.Eventually(t, func() bool {
		_, _ = cachedB.Fetch()
		return innerB.fetchCount.Load() > 1
	}, 3*time.Second, 20*time.Millisecond, "worker B should re-fetch after cross-worker metaUpdated")
}
