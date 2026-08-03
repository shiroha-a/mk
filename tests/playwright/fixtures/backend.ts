// Backend-dependent expectations.
//
// spec は mk-go / Misskey TS の両 backend で走らせて drop-in 互換を検証する
// (docker-compose.playwright.ts.yml)。ただし mk-go には docs/divergence.md に
// 記録済みの意図的な差分があり、そこだけは backend ごとに期待値を変える必要が
// ある。差分そのものを spec から消すと mk-go 側の検証が緩くなるので、
// 「どちらでも通る」ゆるい assert ではなく backend ごとの厳密値を使う。

/** True when the spec runs against the upstream Misskey TS backend. */
export const isTsBackend = process.env.MK_BACKEND_TYPE === 'ts';

/**
 * Expected HTTP status for NO_SUCH_* style errors.
 *
 * upstream は `ApiError` の `kind` 既定が `'client'` なので
 * `ApiCallService.#sendApiError` の `statusCode ?? 400` に落ちて、対象が
 * 存在しない場合も 400 を返す。mk-go は意味的に正確な 404 を返す
 * (docs/divergence.md「admin 系 error の HTTP status」)。error の `code` /
 * `id` は両者一致するので、spec 側は status だけ切り替えて code は共通に
 * assert する (#2276)。
 */
export const NOT_FOUND_STATUS = isTsBackend ? 400 : 404;
