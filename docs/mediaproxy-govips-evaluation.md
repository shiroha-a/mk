# mediaproxy: govips PoC 評価結果 (#735)

#672 (媒体形式拡充) の Phase 3 として、`govips` (libvips Go binding) を mediaproxy に導入する是非を評価する。

## TL;DR

**現状の pure Go / WASM (wazero) ライブラリで十分なパフォーマンスが得られており、govips 導入は推奨しない**。導入すると #618-#620 で実現した cgo-free / static binary 方針を破壊する。format coverage も #672 + #734 で完全になっているため、govips を入れる動機は弱い。

将来 (a) JPEG XR / MNG など pure Go decoder が無い format で完全 transcode が必要になったとき、または (b) 大規模インスタンスで現行 libraries の throughput が運用限界に達したとき、改めて評価する。

## 背景

### govips が提供するもの

`github.com/davidbyttow/govips` は libvips (C library) を cgo 経由で Go から呼ぶ binding。

主な特徴:
- **網羅的 format サポート**: HEIF / HEIC / WebP / AVIF / JPEG XL / JPEG 2000 / JPEG XR / TIFF / GIF / PNG / JPEG / BMP など
- **高速 decode/encode**: libvips は streaming pipeline で SIMD 最適化済み。pure Go / WASM 比で 5-30x の速度差
- **メモリ効率**: タイル単位処理で巨大画像でも低メモリで処理

### 採用コスト

cgo + system library (libvips + libheif + libwebp + libjpeg + ...) 依存:
- **`CGO_ENABLED=0` 不可**: project は #618-620 で cgo を完全排除して static binary 化済み。govips を入れると静的バイナリが作れなくなる
- **Docker image bloat**: libvips + 関連 system library で +50-100MB
- **CI / release process 複雑化**: cross-compile が困難 (Linux/Mac/Windows 各 platform で libvips を準備する必要)
- **開発者 setup**: `apt install libvips-dev` 等が必要、開発環境依存

## 現状の format coverage (PoC 時点)

#672 + #734 完了後、mediaproxy が **pure Go / WASM** で対応する format:

| Format | Library | 採用方式 |
|---|---|---|
| JPEG | std `image/jpeg` | std lib |
| PNG | std `image/png` | std lib |
| GIF | std `image/gif` | std lib |
| WebP (decode) | `golang.org/x/image/webp` | pure Go |
| WebP (encode) | `gen2brain/webp` | wazero (WASM libwebp) |
| AVIF | `gen2brain/avif` | wazero (WASM libavif) |
| HEIC / HEIF | `gen2brain/heic` | wazero |
| JPEG XL | `gen2brain/jpegxl` | wazero |
| BMP | `golang.org/x/image/bmp` | pure Go |
| TIFF | `golang.org/x/image/tiff` | pure Go |
| Netpbm (PBM/PGM/PPM) | `spakin/netpbm` | pure Go (#672 Phase 1) |
| TGA | `blezek/tga` | pure Go (#672 Phase 1) |
| JPEG 2000 (JP2/J2K) | `mrjoshuak/go-jpeg2000` | pure Go (#734) |
| favicon (ICO) | `kovidgoyal/imaging` 依存 | pure Go |

**pass-through のみ** (decode 不可、生バイナリ配信):
- JPEG XR (`image/jxr`)
- MNG / JNG (`video/x-mng`)
- JPX (`image/jpx`、Part 2 拡張)

## ベンチマーク結果

`internal/core/mediaproxy/bench_test.go` で計測 (Intel Xeon E-2274G @4.0GHz, Linux x86_64, Go 1.25)。

### Decode (1024x1024 入力)

| Format | Latency (ns/op) | Memory (B/op) | Allocs |
|---|---:|---:|---:|
| JPEG | 8,746,381 (8.7ms) | 1,587,765 | 30 |
| PNG | 11,426,928 (11.4ms) | 3,199,392 | 31 |
| GIF | 7,240,073 (7.2ms) | 1,086,259 | 541 |
| WebP | 16,261,930 (16.3ms) | 1,624,209 | 37 |
| AVIF | 158,390,312 (158ms) | 87,163,173 | 816 |
| **JP2** | **926,939,890 (927ms)** | **145,604,288** | **24,710** |

### Encode (320x320 avatar 出力)

| Format | Latency (ns/op) | Memory (B/op) | Allocs |
|---|---:|---:|---:|
| WebP | 9,320,671 (9.3ms) | 3,798,186 | 295 |
| AVIF | 74,358,298 (74ms) | 41,678,085 | 2,363 |

### End-to-end resize (decode → resize → encode WebP, avatar 320x320)

| Source | Latency (ns/op) | Memory (B/op) |
|---|---:|---:|
| JPEG → WebP | 26,483,770 (26ms) | 7,442,018 |
| PNG → WebP | 27,509,455 (28ms) | 9,053,190 |
| WebP → WebP | 33,768,343 (34ms) | 7,476,657 |
| **JP2 → WebP** | **944,122,261 (944ms)** | **151,475,918** |

### 観察

1. **JPEG / PNG / GIF / WebP は許容範囲** (<35ms / リクエスト)。Misskey 通常用途で問題なし。
2. **AVIF は wazero overhead で 158ms decode**。これは `gen2brain/avif` の WASM 起動コストが支配的。
3. **JPEG 2000 が圧倒的に遅い (927ms decode)**。pure Go 実装の wavelet 計算が非効率。
   - libvips (libopenjp2) なら ~50ms 程度の見込み (約 18x 高速化)
   - ただし JP2 は federated emoji / avatar で稀なため、運用上の影響は限定的
4. **メモリ消費**: JP2 / AVIF が >40MB だが pixel-bomb cap (`maxDecodedPixels` = 64 MP) で上限ガード済み。

## 判断

### 推奨: 現状維持

issue #735 の判断基準:
> - decode 速度が現行と同等以上 + メモリ消費が許容範囲なら推奨
> - 大幅遅延 / メモリ膨張があれば現行 (gen2brain + std) 維持

govips は速度面では有利だが、cgo / system library 依存の **architectural コスト**が project の static binary 方針 (#618-620) と真っ向衝突する。format coverage は #672/#734 で完成しており、govips 固有の利益は **JP2 decode の高速化のみ** に縮小している。

### 採用しない理由 (整理)

1. **cgo 必須 = static binary 方針破壊**: 直近 PR で cgo を全排除した投資を巻き戻す
2. **format coverage は完了**: pure Go / WASM で全主要 format 対応済み
3. **性能ボトルネックは JP2 のみ**: その JP2 も federated emoji で稀
4. **dependency 追加コスト**: libvips + system libraries で Docker image +50-100MB
5. **CI 複雑化**: cross-compile / dev setup の手間

### 再評価する条件

下記のいずれかが満たされたら本評価をやり直す:

- **Pure Go JPEG XR / MNG decoder の出現**: 現状 pass-through のみ。完全 transcode 需要が出れば govips が候補
- **JP2 traffic 増加**: federated emoji が JP2 化する trend が観測されたら decode latency が問題になる
- **大規模インスタンス運用**: 単一 mediaproxy が秒間数百リクエストを捌く規模では現行性能が limit に達する可能性
- **pure Go alternative の停滞**: `gen2brain/*` / `mrjoshuak/go-jpeg2000` の保守が止まり security / 速度問題が解消されない

## 追記 (#3032): GC 圧という第 2 の軸

本評価は **1 リクエストあたりの速度**だけを見ていた。#3032 の実測で、
それとは別に **Go ヒープへの割り当て量**が効いていることが分かったので記録する。

内蔵プロキシは API サーバーと同じプロセスで動く。デコードの中間バッファは
すべて Go ヒープに載るため、重い画像を捌いている間は**無関係なリクエストまで
GC assist を負わされる**。8 core で AVIF→avatar を c=4 流しながら、同じ
プロセスの軽い pass-through を測った結果:

| 軽い要求 | GOGC | p99 | rps |
|---|---|---|---|
| 343KB の JPEG (pass-through) | 100 (既定) | 163.9ms | 194 |
| 343KB の JPEG (pass-through) | 800 | 2.5ms | 1438 |
| 163B の PNG (pass-through) | 100 (既定) | 6.3ms | 747 |
| 163B の PNG (pass-through) | 800 | 0.4ms | 4086 |

**裾はリクエスト自身の割り当て量に比例する。** P の奪い合いなら要求サイズと
無関係なはずなので、これは GC assist と特定できる。#3032 の同時実行枠では
消えない (枠 1 でも p99 155ms、無制限で 168ms)。

GOGC を上げると消えるが、peak RSS が 585MB → 1,033-3,188MB に膨らむため
2GB VPS 構成 (定常 343MiB) では採れない。

**Misskey 純正が同条件で平坦 (p99 6.1ms → 6.7ms) なのは、sharp が libvips の
ネイティブメモリを使い、V8 のヒープに載せないため。** libuv スレッドプールで
動くことより、こちらの寄与のほうが大きい。

したがって、コーデックをネイティブ実装に寄せる判断には**速度以外の根拠がある**。

**ただし「`CGO_ENABLED=0` を維持したまま同じ効果を狙える」と書いていたのは
誤解を招く記述だった (下記で訂正)。** cgo ツールチェーンは確かに不要なままだが、
**静的バイナリではなくなる**。

## 追記 2 (#3037 の後): 動的リンク化は静的バイナリと両立しない

上の段落を受けて `-tags nodynamic` を外す案を実測したが、**#618 / #619 / #620 が
確立した「static binary 化」の方針と両立しない**ことが分かったので採らなかった。
同じ検討を繰り返さないために記録する。

### 1. 静的バイナリでなくなる

`purego` の nocgo 経路は `//go:cgo_import_dynamic purego_dlopen dlopen "libdl.so.2"`
で dlfcn を**動的にインポート**する。`./cmd/misskey` を両方の形でビルドして
`file` / `readelf -d` で比較した実測:

| ビルド | リンク | interpreter | DT_NEEDED |
|---|---|---|---|
| `-tags nodynamic` (現行) | **statically linked** | 無し | 無し |
| タグ無し | **dynamically linked** | `/lib64/ld-linux-x86-64.so.2` | `libdl.so.2` / `libpthread.so.0` / `libc.so.6` |

`gcr.io/distroless/static-debian13` は libc を持たないので、**現行の最終 stage では
そもそも起動しない**。ランタイムイメージの変更が必須になる。

### 2. builder が Alpine (musl) なので事情が増える

`golang:1.26.6-alpine` でビルドすると interpreter は `/lib/ld-musl-x86_64.so.1` に
なる (DT_NEEDED は glibc の soname のまま)。Alpine 上では起動して**動作もした**が、
builder とランタイムの libc を揃える必要が生じる。

### 3. 効果は出る (Alpine + `-dev` パッケージ)

1024x1024 の decode 実測 (`golang:1.26.6-alpine` 上):

| 構成 | webp | avif |
|---|---|---|
| A. `-tags nodynamic` (現行) | 31.10 ms | 222.69 ms |
| B. タグ無し・ライブラリ**無し** | 31.02 ms | 223.75 ms |
| C. タグ無し・native ライブラリ**あり** | **6.69 ms** | **46.74 ms** |

- **B が A と同じ** = ライブラリが無ければ黙って wazero にフォールバックする。
  イメージが lib を落としても壊れず**遅くなるだけ**なので、導入するなら
  「実際に native を使っているか」を起動時ログと CI で検証する必要がある
- **`-dev` パッケージが要る。** dlopen する名前は `libwebp.so` /
  `libavif.so` で、soname 付き (`libwebp.so.7`) では見つからない。
  その symlink を提供するのは `-dev` 側
- Alpine には `libavif` / `libjxl` / `libwebp` / `libheif` が揃っている

### 4. wazero 側にチューニングの余地は無い

`gen2brain/avif` は `CompileModule` を `sync.OnceFunc` で 1 回だけ行い、
`InstantiateModule` を decode ごとに呼ぶ。後者が支配的なら改善余地があるが、
画像サイズを振った実測では**固定コストは 3-4ms しかない**:

| サイズ | decode | 速度 |
|---|---|---|
| 64x64 | 4.05 ms | 1.0 px/ms |
| 256x256 | 18.40 ms | 3.6 px/ms |
| 1024x1024 | 227.49 ms | 4.6 px/ms |
| 2048x2048 | 904.04 ms | 4.6 px/ms |

大きい側で px/ms が一定に収束するので、**222ms は本物の AV1 デコード処理**であり
SIMD が無いことが原因。instantiation の削減では取れない。

### 5. 純 Go の代替も無い

`golang.org/x/image` が `Encode` を持つのは tiff と bmp だけで、**WebP encoder は
無い** (decoder のみ)。全リサイズ要求が通る WebP encode (7.22ms) を静的リンクの
まま置き換える既製品は存在しない。

### 結論: 外部メディアプロキシに逃がす

静的リンクを維持したまま残る手は「結果をキャッシュ / 永続化して**デコード回数を
減らす**」だけで、コーデックそのものは速くできない。一方 mk-go には
`externalMediaProxyEnabled` (`config.mediaProxy` を設定すると `/proxy` が 301 で
転送する) が既にあるので、**性能が要る運営者は media-proxy-rs 等を指せばよい**。
内蔵プロキシを無理に高速化する必要は無い、というのが現時点の判断。

**ただし外部に回すと 3 つを失う** (実測で確認した内蔵だけの利点):

- `swapToVariant` — ローカルファイルの resize モードで、生成済み variant に
  すり替えて画像処理ごと飛ばす
- ローカル drive ファイルの直接読み出し (HTTP 往復が無い)
- allowlist — mk-go の `/proxy` は open proxy ではない

`externalMediaProxyEnabled` は全経路を一律に転送するので、運営者はこの 3 つと
性能のどちらを取るかを選ぶことになる。

**再評価する条件**: 静的リンクの方針を変える判断が別途下りたとき、または
上記 3 点を保ったまま重い形式だけ外部へ委譲する仕組み
(`videoThumbnailGenerator` と同じ形) を入れる価値が出たとき。後者は
**AVIF / HEIC / JXL が実トラフィックに占める割合が未測定**なので、
先にそれを測ること。

## 関連

- #672 (parent issue: media format 拡充)
- #3032 (画像処理の同時実行枠。上記 GC 圧の測定元)
- #618 / #619 / #620 (cgo 完全排除 → static binary 化)
- #733 (Phase 1: Netpbm + TGA + JXR/MNG pass-through)
- #736 (Phase 2: JPEG 2000 pure Go)
- #3034 / #3035 / #3036 / #3037 (この調査から派生した `/proxy` のエラー写像の修正)
- `internal/core/mediaproxy/bench_test.go` (本評価のベンチマーク source)
