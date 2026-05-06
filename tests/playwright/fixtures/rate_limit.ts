// #744 Phase 1: rate limit reset helper.
//
// mk-go の signup endpoint は IP base 1h 5 回 hardcoded
// (internal/server/middleware/ratelimit_defs.go)。spec 累積で 429 になる
// のを防ぐため、test の境界で Redis を flush して counter をゼロから
// 始める。後続 PR で signup endpoint を更に使う spec が増えても、各 spec
// の beforeAll で `resetRateLimit()` を呼べば独立性が保たれる。
//
// `redis-cli` は Dockerfile.runner で apt 経由で install 済み。
// child_process.execFileSync で同期的に発行する (Playwright fixture は
// async OK だが redis-cli が ms オーダーで終わるので sync で十分)。

import { execFileSync } from 'node:child_process';

const REDIS_HOST = process.env.MK_REDIS_HOST ?? 'redis';

// resetRateLimit issues `redis-cli FLUSHDB` against the test stack's redis.
// Throws when the command is unreachable so callers fail fast (= globalSetup
// or a spec's beforeAll) instead of letting later 429s mask the root cause.
export function resetRateLimit(): void {
  execFileSync('redis-cli', ['-h', REDIS_HOST, 'FLUSHDB'], {
    stdio: 'pipe',
    timeout: 5_000,
  });
}
