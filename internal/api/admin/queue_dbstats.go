package admin

import (
	"context"
	"strconv"
	"strings"
)

// QueueRedisInfoProvider exposes the job-queue Redis `INFO` output. Implemented
// by a thin adapter over the go-redis client in server wiring so that this
// package stays free of a go-redis import. Mirrors upstream Misskey TS
// `QueueService.queueGetQueue`, which builds the `db` block from
// `parseRedisInfo(await queue.client.info())`.
type QueueRedisInfoProvider interface {
	QueueRedisInfo(ctx context.Context) (string, error)
}

// SetQueueRedis wires the job-queue Redis INFO provider used to populate the
// per-queue `db` (memory / clients / uptime) block returned by queue-stats /
// queues. nil leaves the zero-stub behaviour intact.
func (h *Handler) SetQueueRedis(p QueueRedisInfoProvider) {
	h.queueRedis = p
}

// defaultQueueDB returns the zero-valued `db` block used when no Redis INFO is
// available (provider unwired / INFO error). frontend (admin/job-queue.vue) が
// 参照する shape を満たす。
func defaultQueueDB() map[string]any {
	return map[string]any{
		"version":   "",
		"mode":      "",
		"runId":     "",
		"processId": "",
		"port":      0,
		"os":        "",
		"uptime":    0,
		"memory":    map[string]any{"total": 0, "used": 0, "peak": 0, "fragmentationRatio": 0},
		"clients":   map[string]any{"connected": 0, "blocked": 0},
	}
}

// queueDBStats fetches the job-queue Redis INFO and maps it into the `db` block.
// Redis INFO は per-queue でなく接続単位 (= job-queue Redis 全体) なので全 queue
// で同値になる (upstream も各 queue が同じ Redis を見るため同じ)。provider 未配線
// / INFO 失敗時は defaultQueueDB() を返して 200 を維持する。
func (h *Handler) queueDBStats(ctx context.Context) map[string]any {
	if h.queueRedis == nil {
		return defaultQueueDB()
	}
	raw, err := h.queueRedis.QueueRedisInfo(ctx)
	if err != nil {
		return defaultQueueDB()
	}
	info := parseRedisInfo(raw)

	atoi := func(k string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(info[k]))
		return n
	}
	// total_system_memory が無い (maxmemory 運用) ときは maxmemory に fallback。
	// upstream: parseInt(total_system_memory) || parseInt(maxmemory)。
	total := atoi("total_system_memory")
	if total == 0 {
		total = atoi("maxmemory")
	}
	// mem_fragmentation_ratio は float ("1.52")。upstream は parseInt 相当で
	// 整数部のみ採る。
	fragRatio := 0
	if f, ferr := strconv.ParseFloat(strings.TrimSpace(info["mem_fragmentation_ratio"]), 64); ferr == nil {
		fragRatio = int(f)
	}

	return map[string]any{
		"version":   info["redis_version"],
		"mode":      info["redis_mode"],
		"runId":     info["run_id"],
		"processId": info["process_id"],
		"port":      atoi("tcp_port"),
		"os":        info["os"],
		"uptime":    atoi("uptime_in_seconds"),
		"memory": map[string]any{
			"total":              total,
			"used":               atoi("used_memory"),
			"peak":               atoi("used_memory_peak"),
			"fragmentationRatio": fragRatio,
		},
		"clients": map[string]any{
			"connected": atoi("connected_clients"),
			"blocked":   atoi("blocked_clients"),
		},
	}
}

// parseRedisInfo parses the line-oriented Redis `INFO` payload into a flat
// key→value map. Section headers ("# Server") and blank lines are skipped;
// only the first ':' on each line splits key from value (values never contain
// a leading key, and CRLF is trimmed).
func parseRedisInfo(raw string) map[string]string {
	out := make(map[string]string, 128)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}
		out[line[:idx]] = line[idx+1:]
	}
	return out
}
