// #829 PR-C: drive 系 spec で共有する fixture file。
//
// drive/files/create / find / find-by-hash / 等の spec で同じ test image を
// upload するため、binary を 1 か所に集約する。test 内で動的生成すると
// node の Sharp/Canvas 依存が増えるので、固定の minimal PNG を base64 で
// 持つ方が deterministic + 軽量。

// 1x1 transparent PNG, 67 bytes。base64 decode 結果も deterministic で
// md5 (= file content hash) も常に同じになる = find-by-hash spec で
// 検証 anchor として使える。
export const tinyPNG = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=',
  'base64',
);
