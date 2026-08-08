// Package procstats collects live statistics about the running mk-go process
// (Go runtime counters and uptime).
//
// 既存の core/serverstats は**ホストマシンの静的スペック** (CPU model / cores、
// メモリ総量、ディスク) を返すもので、プロセスの状態は扱わない。queue/runtimestats
// はキュー driver の観測専用。どちらとも役割が重ならないので別パッケージにしている
// (#2395)。
package procstats

import (
	"runtime"
	"time"
)

// Stats is the full snapshot returned to the admin API.
//
// バイトは *Bytes、時間は *Ms / *Ns と field 名に単位を出す。frontend 側で ms と ns
// を取り違えても型では気付けないため。
type Stats struct {
	UptimeMs int64       `json:"uptimeMs"`
	Version  VersionInfo `json:"version"`
	Go       GoStats     `json:"go"`
}

// VersionInfo identifies the running build.
type VersionInfo struct {
	Misskey string `json:"misskey"`
	MkGo    string `json:"mkGo"`
}

// GoStats holds Go runtime counters.
type GoStats struct {
	Goroutines     int     `json:"goroutines"`
	GOMAXPROCS     int     `json:"gomaxprocs"`
	HeapAllocBytes uint64  `json:"heapAllocBytes"`
	HeapSysBytes   uint64  `json:"heapSysBytes"`
	HeapObjects    uint64  `json:"heapObjects"`
	GCNum          uint32  `json:"gcNum"`
	LastGCPauseNs  uint64  `json:"lastGcPauseNs"`
	GCCPUFraction  float64 `json:"gcCpuFraction"`
}

// Deps carries the optional inputs. StartedAt が zero なら uptime は 0 になる
// だけで、収集全体は成功する (best-effort)。
type Deps struct {
	StartedAt time.Time
	// Now はテスト用の時計 seam。nil なら time.Now。
	Now func() time.Time
}

// Collect gathers a snapshot of the current process.
//
// runtime.ReadMemStats は stop-the-world を伴う。呼び出し頻度は API の呼び出し側
// (admin UI のポーリング) が握るので、ここでは間引きをしない。
func Collect(deps Deps, misskeyVersion, mkGoVersion string) Stats {
	now := time.Now
	if deps.Now != nil {
		now = deps.Now
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	stats := Stats{
		Version: VersionInfo{Misskey: misskeyVersion, MkGo: mkGoVersion},
		Go: GoStats{
			Goroutines:     runtime.NumGoroutine(),
			GOMAXPROCS:     runtime.GOMAXPROCS(0),
			HeapAllocBytes: mem.HeapAlloc,
			HeapSysBytes:   mem.HeapSys,
			HeapObjects:    mem.HeapObjects,
			GCNum:          mem.NumGC,
			GCCPUFraction:  mem.GCCPUFraction,
		},
	}
	// PauseNs はリングバッファで、直近の値は (NumGC+255)%256 の位置にある。
	// GC が一度も走っていない状態で読むと常に 0 なので NumGC で分岐する。
	if mem.NumGC > 0 {
		stats.Go.LastGCPauseNs = mem.PauseNs[(mem.NumGC+255)%256]
	}

	if !deps.StartedAt.IsZero() {
		uptime := now().Sub(deps.StartedAt)
		if uptime > 0 {
			stats.UptimeMs = uptime.Milliseconds()
		}
	}

	return stats
}
