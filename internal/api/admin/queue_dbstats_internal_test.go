package admin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRedisInfo(t *testing.T) {
	raw := "# Server\r\nredis_version:7.2.4\r\nrun_id:abc\r\n\r\n# Memory\r\nused_memory:123\r\nmem_fragmentation_ratio:1.52\r\n"
	info := parseRedisInfo(raw)
	assert.Equal(t, "7.2.4", info["redis_version"])
	assert.Equal(t, "abc", info["run_id"])
	assert.Equal(t, "123", info["used_memory"])
	assert.Equal(t, "1.52", info["mem_fragmentation_ratio"])
	// section header / blank line は無視。
	_, hasHeader := info["# Server"]
	assert.False(t, hasHeader)
}

type stubRedisInfo struct {
	raw string
	err error
}

func (s stubRedisInfo) QueueRedisInfo(_ context.Context) (string, error) {
	return s.raw, s.err
}

func TestQueueDBStats_FromProvider(t *testing.T) {
	raw := "# Server\n" +
		"redis_version:7.2.4\n" +
		"redis_mode:standalone\n" +
		"run_id:abc123\n" +
		"process_id:42\n" +
		"tcp_port:6379\n" +
		"os:Linux\n" +
		"uptime_in_seconds:12345\n" +
		"# Clients\n" +
		"connected_clients:7\n" +
		"blocked_clients:1\n" +
		"# Memory\n" +
		"used_memory:1048576\n" +
		"used_memory_peak:2097152\n" +
		"total_system_memory:8589934592\n" +
		"mem_fragmentation_ratio:1.52\n" +
		"maxmemory:0\n"

	h := &Handler{}
	h.SetQueueRedis(stubRedisInfo{raw: raw})
	db := h.queueDBStats(context.Background())

	assert.Equal(t, "7.2.4", db["version"])
	assert.Equal(t, "standalone", db["mode"])
	assert.Equal(t, "abc123", db["runId"])
	assert.Equal(t, "42", db["processId"])
	assert.Equal(t, 6379, db["port"])
	assert.Equal(t, "Linux", db["os"])
	assert.Equal(t, 12345, db["uptime"])

	mem := db["memory"].(map[string]any)
	assert.Equal(t, 1048576, mem["used"])
	assert.Equal(t, 2097152, mem["peak"])
	assert.Equal(t, 8589934592, mem["total"])
	assert.Equal(t, 1, mem["fragmentationRatio"]) // parseInt 相当 (1.52 → 1)

	cl := db["clients"].(map[string]any)
	assert.Equal(t, 7, cl["connected"])
	assert.Equal(t, 1, cl["blocked"])
}

// total_system_memory が無いときは maxmemory に fallback する。
func TestQueueDBStats_MaxmemoryFallback(t *testing.T) {
	raw := "# Memory\nused_memory:10\nmaxmemory:5000\n"
	h := &Handler{}
	h.SetQueueRedis(stubRedisInfo{raw: raw})
	db := h.queueDBStats(context.Background())
	mem := db["memory"].(map[string]any)
	assert.Equal(t, 5000, mem["total"])
}

// provider 未配線 / INFO エラー時は zero-stub を返す (200 維持)。
func TestQueueDBStats_DefaultWhenUnavailable(t *testing.T) {
	// 未配線。
	h := &Handler{}
	db := h.queueDBStats(context.Background())
	assert.Equal(t, "", db["version"])
	assert.Equal(t, 0, db["port"])
	mem := db["memory"].(map[string]any)
	assert.Equal(t, 0, mem["used"])

	// INFO エラー。
	h.SetQueueRedis(stubRedisInfo{err: assert.AnError})
	db2 := h.queueDBStats(context.Background())
	require.Equal(t, defaultQueueDB(), db2)
}
