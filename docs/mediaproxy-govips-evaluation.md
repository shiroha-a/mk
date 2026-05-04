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

## 関連

- #672 (parent issue: media format 拡充)
- #618 / #619 / #620 (cgo 完全排除 → static binary 化)
- #733 (Phase 1: Netpbm + TGA + JXR/MNG pass-through)
- #736 (Phase 2: JPEG 2000 pure Go)
- `internal/core/mediaproxy/bench_test.go` (本評価のベンチマーク source)
