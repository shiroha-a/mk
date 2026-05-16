# Fedibird-compatible ActivityPub mock for drop-in e2e (#1083).
# python:3.12-slim ベース。alpine だと cryptography wheel が ABI 不整合
# で fall back build に時間がかかるので slim を採用。
FROM python:3.12-slim

WORKDIR /app
COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r requirements.txt

COPY *.py /app/

# TLS cert は /certs に外部 mount される前提。
ENV FEDIBIRD_DOMAIN=fedibird-mock.test
ENV FEDIBIRD_TLS_CERT=/certs/server.crt
ENV FEDIBIRD_TLS_KEY=/certs/server.key

EXPOSE 443
CMD ["python", "/app/server.py"]
