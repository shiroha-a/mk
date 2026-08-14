# pg_bigm 入り PostgreSQL 18 (#2514)。
#
# pg_bigm は 2-gram の GIN index (`gin_bigm_ops`) で LIKE 部分一致を加速する
# 拡張。日本語のように単語境界の無い言語の部分一致検索に効く。mk-go の
# sqlLike 検索は upstream と同じ `lower(text) LIKE ...` 形なので、
# 拡張とインデックスを作るだけで provider の変更なしに index が効く
# (手順は docs/deployment.md)。
#
# 公式 image に pg_bigm は無いのでソースからビルドする。alpine の postgres
# image は pgxs (拡張ビルド基盤) を同梱しており、build deps を一時導入して
# make するだけでよい (icu-dev は pg_locale.h が要求、clang/llvm は JIT 用
# bitcode の emit に要る。いずれも --virtual で削除され runtime には残らない)。
#
# **release tag ではなく commit SHA に固定する。** tag は force-push で
# 差し替え可能だが、commit archive は immutable。SHA は v1.2-20250903
# (PostgreSQL 18 を公式サポートした release) を指す。
FROM postgres:18-alpine

ARG PG_BIGM_COMMIT=735dceba0ecdd8ac1aaaaa207226a7102b6bbd71

RUN apk add --no-cache --virtual .build-deps build-base clang19 llvm19 curl icu-dev \
 && curl -fsSL "https://github.com/pgbigm/pg_bigm/archive/${PG_BIGM_COMMIT}.tar.gz" -o /tmp/pg_bigm.tar.gz \
 && tar xzf /tmp/pg_bigm.tar.gz -C /tmp \
 && make -C "/tmp/pg_bigm-${PG_BIGM_COMMIT}" USE_PGXS=1 install \
 && rm -rf /tmp/pg_bigm* \
 && apk del .build-deps
