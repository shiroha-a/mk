package instance

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// CapabilityTarget is the narrow surface of the signature-capability store
// consumed by CapabilityBuffer. TouchTarget と同じ理由で切り出している
// (unit test がフルの repository を組まなくても済む)。
type CapabilityTarget interface {
	RecordInboundAlg(host, alg string, at time.Time) error
	RecordLDSignature(host string, at time.Time) error
	RecordEd25519Accepted(host string, at time.Time) error
}

// capabilityObservation accumulates one host's observations within a flush
// window. ゼロ値の時刻は「その系統の観測がこの窓では無かった」を表し、flush 時に
// 書き込みをスキップする判定に使う。
type capabilityObservation struct {
	inboundAlg        string
	inboundAt         time.Time
	ldSignatureAt     time.Time
	ed25519AcceptedAt time.Time
}

// CapabilityBuffer coalesces high-frequency signature observations into at most
// one DB write per host per系統 per flush interval (#2393).
//
// inbound は同一 remote host から大量に来るため、観測のたびに UPDATE を撃つと
// TouchBuffer を入れて解消した drain time のボトルネック (#569) をそのまま再現
// してしまう。同じ縮退方式を踏襲する。
//
// TouchBuffer 本体を拡張せず別実装にしているのは、TouchBuffer が連合の hot path で
// 最も踏まれるコードで、pending の値型を time.Time から struct へ変える改造の
// リグレッションリスクが利得に見合わないため。
//
// Stop しないと bg goroutine がリークするので、上位は shutdown フローで Close()
// を必ず呼ぶ前提 (TouchBuffer と同じ)。
type CapabilityBuffer struct {
	target  CapabilityTarget
	clock   func() time.Time
	mu      sync.Mutex
	pending map[string]*capabilityObservation
	stopCh  chan struct{}
	doneCh  chan struct{}
	flushIn time.Duration
	// started=true は Start() が既に bg goroutine を起動済であることを表す。
	// Close は started=false なら doneCh を待たずに pending だけ flush する
	// (TouchBuffer が #580 で踏んだ deadlock と同じ穴を塞ぐ)。
	started atomic.Bool
}

// NewCapabilityBuffer returns a buffer that flushes every flushInterval.
// flushInterval = 0 は default 1s 扱い (TouchBuffer と同じ)。
func NewCapabilityBuffer(target CapabilityTarget, flushInterval time.Duration) *CapabilityBuffer {
	if flushInterval <= 0 {
		flushInterval = time.Second
	}
	return &CapabilityBuffer{
		target:  target,
		clock:   time.Now,
		pending: make(map[string]*capabilityObservation),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
		flushIn: flushInterval,
	}
}

// Start spawns the background flush goroutine. 二度目以降の呼び出しは
// 何もせずに return する (idempotent)。
func (b *CapabilityBuffer) Start(ctx context.Context) {
	if !b.started.CompareAndSwap(false, true) {
		return
	}
	go b.runLoop(ctx)
}

// Close stops the background flush goroutine and triggers a final flush.
// Start を呼ばずに Close した場合は doneCh を待たずに pending を best-effort で
// flush して return する。
func (b *CapabilityBuffer) Close() {
	if !b.started.Load() {
		b.flushOnce()
		return
	}
	select {
	case <-b.stopCh:
		return
	default:
	}
	close(b.stopCh)
	<-b.doneCh
}

// ObserveInboundAlg records the key type of a verified inbound HTTP Signature.
// per-request で呼ばれる hot path。
func (b *CapabilityBuffer) ObserveInboundAlg(host, alg string) {
	if b == nil || host == "" || alg == "" {
		return
	}
	b.withPending(host, func(obs *capabilityObservation) {
		obs.inboundAlg = alg
		obs.inboundAt = b.clock()
	})
}

// ObserveLDSignature records that an LD-Signature-bearing activity arrived.
func (b *CapabilityBuffer) ObserveLDSignature(host string) {
	if b == nil || host == "" {
		return
	}
	b.withPending(host, func(obs *capabilityObservation) {
		obs.ldSignatureAt = b.clock()
	})
}

// ObserveEd25519Accepted records that an Ed25519-signed delivery was accepted.
func (b *CapabilityBuffer) ObserveEd25519Accepted(host string) {
	if b == nil || host == "" {
		return
	}
	b.withPending(host, func(obs *capabilityObservation) {
		obs.ed25519AcceptedAt = b.clock()
	})
}

// FlushNow drains the pending map and applies updates synchronously.
// Test 用。production 経路は runLoop が定期 flush する。
func (b *CapabilityBuffer) FlushNow() {
	b.flushOnce()
}

func (b *CapabilityBuffer) withPending(host string, apply func(*capabilityObservation)) {
	b.mu.Lock()
	obs, ok := b.pending[host]
	if !ok {
		obs = &capabilityObservation{}
		b.pending[host] = obs
	}
	apply(obs)
	b.mu.Unlock()
}

func (b *CapabilityBuffer) runLoop(ctx context.Context) {
	defer close(b.doneCh)
	ticker := time.NewTicker(b.flushIn)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			b.flushOnce()
			return
		case <-b.stopCh:
			b.flushOnce()
			return
		case <-ticker.C:
			b.flushOnce()
		}
	}
}

// flushOnce drains the pending map and writes each observed signal.
//
// 系統ごとに別メソッドを呼ぶのは、repository 側が「観測した系統の列だけ」を更新
// する部分 upsert になっているため。窓内で観測されなかった系統を呼ばないことで、
// 既存の記録を消さずに済む。
func (b *CapabilityBuffer) flushOnce() {
	b.mu.Lock()
	if len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	pending := b.pending
	b.pending = make(map[string]*capabilityObservation, len(pending))
	b.mu.Unlock()

	if b.target == nil {
		return
	}
	for host, obs := range pending {
		if !obs.inboundAt.IsZero() {
			if err := b.target.RecordInboundAlg(host, obs.inboundAlg, obs.inboundAt); err != nil {
				slog.Warn("signature capability flush failed", "host", host, "signal", "inboundAlg", "err", err)
			}
		}
		if !obs.ldSignatureAt.IsZero() {
			if err := b.target.RecordLDSignature(host, obs.ldSignatureAt); err != nil {
				slog.Warn("signature capability flush failed", "host", host, "signal", "ldSignature", "err", err)
			}
		}
		if !obs.ed25519AcceptedAt.IsZero() {
			if err := b.target.RecordEd25519Accepted(host, obs.ed25519AcceptedAt); err != nil {
				slog.Warn("signature capability flush failed", "host", host, "signal", "ed25519Accepted", "err", err)
			}
		}
	}
}
