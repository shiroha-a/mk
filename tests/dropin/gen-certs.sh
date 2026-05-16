#!/bin/sh
# Generate self-signed certs for the two drop-in test instances (`a`, `b`).
# Mirrors tests/federation/common/gen-certs.sh but with different domains —
# keeping it separate avoids cross-suite coupling if the federation suite
# changes its domain set later.
set -e

CERT_DIR=/certs
mkdir -p "$CERT_DIR"

# `fedibird-mock.test` は #1083 で追加された Fedibird-like ActivityPub mock
# のホスト名。drop-in fedibird overlay で起動するときに mock 用の cert を
# bundle.pem に含めて mk-A の trust store に読ませる必要がある。
DOMAINS="a b fedibird-mock.test"

for domain in $DOMAINS; do
  if [ ! -f "$CERT_DIR/$domain.crt" ]; then
    openssl req -x509 -newkey rsa:2048 -nodes \
      -keyout "$CERT_DIR/$domain.key" \
      -out "$CERT_DIR/$domain.crt" \
      -days 1 \
      -subj "/CN=$domain" \
      -addext "subjectAltName=DNS:$domain" \
      2>/dev/null
    echo "Generated cert for $domain"
  fi
done

# bundle.pem は mk-go が SSL_CERT_FILE 経由で trust store に読ませるためのもの。
# Phase 13-2 (#367) で TS-A の backend を mk-A に差し替える時、mk-A が連合先
# instance B の自己署名 cert を検証できるようにする。Phase 13-1 では未使用だが
# ファイル生成は冪等なので常に作って置く。
: > "$CERT_DIR/bundle.pem"
for domain in $DOMAINS; do
  cat "$CERT_DIR/$domain.crt" >> "$CERT_DIR/bundle.pem"
done
echo "Wrote $CERT_DIR/bundle.pem"
