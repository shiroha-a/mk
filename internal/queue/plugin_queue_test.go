package queue_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/shiroha-a/mk/internal/queue"
	"github.com/shiroha-a/mk/internal/queue/driver"
)

func TestPluginQueueNaming(t *testing.T) {
	assert.Equal(t, "plugin:demo", queue.PluginQueueName("demo"))
	assert.Equal(t, "plugin:demo:refresh", queue.PluginTaskType("demo", "refresh"))

	// **本体のキューと衝突しないこと。** `:` を含むので、名前の形が違う。
	for _, name := range queue.AllQueueNames() {
		assert.NotEqual(t, name, queue.PluginQueueName("demo"))
	}
}

// キュー名は Redis のキーになるので、形の怪しいものは弾く。
func TestPluginQueueNames_SkipsInvalid(t *testing.T) {
	got := queue.PluginQueueNames([]string{"demo", "", "Bad", "a b", "x/y", "ok-2"})
	assert.Equal(t, []string{"plugin:demo", "plugin:ok-2"}, got)
}

// enqueue は **専用キュー**へ、**再試行なし**で積む。
func TestClient_EnqueuePlugin(t *testing.T) {
	d := newFakeDriver()
	c := queue.NewClient(d)

	require.NoError(t, c.EnqueuePlugin(context.Background(), "demo", "refresh", []byte(`{"a":1}`)))

	require.Len(t, d.client.calls, 1)
	got := d.client.calls[0]
	assert.Equal(t, "plugin:demo:refresh", got.taskType)
	assert.JSONEq(t, `{"a":1}`, string(got.payload))
	o := driver.ApplyEnqueueOptions(got.opts)
	assert.Equal(t, "plugin:demo", o.Queue)
	assert.Equal(t, 0, o.MaxRetry, "既定は再試行しない")
}

// 呼び出し側のオプションが後ろに来るので、既定を上書きできる。
func TestClient_EnqueuePlugin_OptionsOverrideDefaults(t *testing.T) {
	d := newFakeDriver()
	c := queue.NewClient(d)

	require.NoError(t, c.EnqueuePlugin(context.Background(), "demo", "refresh", nil,
		driver.WithMaxRetry(3), driver.WithProcessIn(90*time.Second)))

	o := driver.ApplyEnqueueOptions(d.client.calls[0].opts)
	assert.Equal(t, "plugin:demo", o.Queue, "キューは呼び出し側から変えられない値ではないが、既定は専用キュー")
	assert.Equal(t, 3, o.MaxRetry)
	assert.Equal(t, 90*time.Second, o.ProcessIn)
}

func TestClient_EnqueuePlugin_RejectsBadNames(t *testing.T) {
	c := queue.NewClient(newFakeDriver())
	assert.Error(t, c.EnqueuePlugin(context.Background(), "", "refresh", nil))
	assert.Error(t, c.EnqueuePlugin(context.Background(), "Bad Name", "refresh", nil))
	assert.Error(t, c.EnqueuePlugin(context.Background(), "demo", "", nil))
}

// fakeDriver は enqueue の引数だけを見るための最小実装。
//
// **実 Redis を使わない。** ここで確かめたいのは「どのキューへ、どの task type
// で、どのオプションで積むか」だけなので、driver を丸ごと立てる必要が無い。
type fakeDriver struct {
	driver.Driver
	client *fakeClient
}

func newFakeDriver() *fakeDriver { return &fakeDriver{client: &fakeClient{}} }

func (d *fakeDriver) Client() driver.Client       { return d.client }
func (d *fakeDriver) Inspector() driver.Inspector { return nil }

type enqueueCall struct {
	taskType string
	payload  []byte
	opts     []driver.EnqueueOption
}

type fakeClient struct{ calls []enqueueCall }

func (c *fakeClient) Enqueue(_ context.Context, taskType string, payload []byte, opts ...driver.EnqueueOption) error {
	c.calls = append(c.calls, enqueueCall{taskType: taskType, payload: payload, opts: opts})
	return nil
}

func (c *fakeClient) Close() error { return nil }
