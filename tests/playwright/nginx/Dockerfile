# #817 part2: TLS proxy for Playwright stack。
#
# Browser の WebAuthn API は secure context (HTTPS) を要求するため、
# mk-go (HTTP 3000) の前に nginx を立てて HTTPS terminate する。
# self-signed cert は build 時に生成し、Playwright runner は
# `ignoreHTTPSErrors: true` で受け入れる。
FROM nginx:alpine

# self-signed cert 生成。CN=mkgo.local で 10 年有効。
RUN apk add --no-cache openssl \
    && mkdir -p /etc/ssl/private \
    && openssl req -x509 -nodes -newkey rsa:2048 \
        -keyout /etc/ssl/private/mkgo.key \
        -out /etc/ssl/certs/mkgo.crt \
        -subj "/CN=mkgo.local" \
        -addext "subjectAltName=DNS:mkgo.local" \
        -days 3650 \
    && apk del openssl

COPY default.conf /etc/nginx/conf.d/default.conf
