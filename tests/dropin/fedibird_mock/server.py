"""Fedibird-compatible ActivityPub mock server for drop-in e2e (#1083).

mk-A の federation を walks through するため、FEP-521a Multikey 形式で Ed25519
公開鍵を `assertionMethod[]` として expose する remote actor を模擬する。

Endpoints:
  - GET /.well-known/webfinger?resource=acct:mock-alice@<host>
  - GET /users/mock-alice  (Accept: application/activity+json)
  - POST /users/mock-alice/inbox  (HTTP Signature verify)
  - GET /_test/inbox-log   (test 用: 受信した activity 一覧を返す)
  - POST /_test/deliver    (test 用: mock → mk-A の任意 inbox に Ed25519 sign で送る)

production の Fedibird / Mastodon 実装の actor JSON shape を意図して再現
しているため、mk-A の resolver が assertionMethod を parse して user_publickey_extra
に upsert する経路を e2e で検証できる。
"""

from __future__ import annotations

import json
import os
import threading
from typing import Any

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ed25519, rsa
from flask import Flask, jsonify, request

from multikey import encode_ed25519_multikey
from signer import (
    http_signature_verify,
    parse_pem_public_key,
    post_signed,
)

DOMAIN = os.environ.get("FEDIBIRD_DOMAIN", "fedibird-mock.test")
ACTOR_ID = f"https://{DOMAIN}/users/mock-alice"

# Generate fresh RSA + Ed25519 keypairs at process start so each test run is
# isolated. Production Fedibird ではこれらは永続化されるが mock は短命なので
# 起動毎に変える方が test の冪等性が high。
_rsa_priv = rsa.generate_private_key(public_exponent=65537, key_size=2048)
_rsa_pub = _rsa_priv.public_key()
_ed_priv = ed25519.Ed25519PrivateKey.generate()
_ed_pub = _ed_priv.public_key()


def _rsa_pem(pub) -> str:
    return pub.public_bytes(
        serialization.Encoding.PEM,
        serialization.PublicFormat.SubjectPublicKeyInfo,
    ).decode("ascii")


# 受信した activity を memoize する。本 mock は単一 process / 単一 worker
# で動かす前提 (drop-in test container は 1 instance)。
_received_lock = threading.Lock()
_received: list[dict[str, Any]] = []

# remote actor の PEM cache (= signature verify 用)。mk-A 等の actor を初回
# fetch して以降は cache hit。
_remote_keys: dict[str, Any] = {}
_remote_keys_lock = threading.Lock()


def _resolve_remote_public_key(key_id: str):
    """key_id (= `<actor>#main-key` etc.) から actor URL を抽出し fetch して
    publicKey or assertionMethod から鍵を取得する。in-memory cache あり。"""
    with _remote_keys_lock:
        if key_id in _remote_keys:
            return _remote_keys[key_id]
    actor_url = key_id.split("#", 1)[0]
    import requests
    resp = requests.get(
        actor_url,
        headers={"Accept": "application/activity+json"},
        verify=False,
        timeout=10,
    )
    if resp.status_code != 200:
        return None
    actor = resp.json()
    pub = None
    pk = actor.get("publicKey") or {}
    if isinstance(pk, dict) and pk.get("id") == key_id:
        pub = parse_pem_public_key(pk.get("publicKeyPem", ""))
    if pub is None:
        for am in actor.get("assertionMethod") or []:
            if am.get("id") == key_id:
                # NB: mock では Multikey の decode は不要 (mk → mock 経路は
                # 通常 RSA #main-key で sign される)。ここで Ed25519 が必要に
                # なる経路があれば DecodeEd25519Multikey 相当を追加する。
                break
    if pub is not None:
        with _remote_keys_lock:
            _remote_keys[key_id] = pub
    return pub


def build_actor_json() -> dict[str, Any]:
    return {
        "@context": [
            "https://www.w3.org/ns/activitystreams",
            "https://w3id.org/security/v1",
            "https://w3id.org/security/multikey/v1",
            "https://w3id.org/security/data-integrity/v1",
        ],
        "id": ACTOR_ID,
        "type": "Person",
        "preferredUsername": "mock-alice",
        "name": "Mock Alice (Fedibird-like)",
        "inbox": f"{ACTOR_ID}/inbox",
        "outbox": f"{ACTOR_ID}/outbox",
        "followers": f"{ACTOR_ID}/followers",
        "following": f"{ACTOR_ID}/following",
        "endpoints": {"sharedInbox": f"https://{DOMAIN}/inbox"},
        "publicKey": {
            "id": f"{ACTOR_ID}#main-key",
            "owner": ACTOR_ID,
            "publicKeyPem": _rsa_pem(_rsa_pub),
        },
        "assertionMethod": [
            {
                "id": f"{ACTOR_ID}#ed25519-key",
                "type": "Multikey",
                "controller": ACTOR_ID,
                "publicKeyMultibase": encode_ed25519_multikey(_ed_pub),
            }
        ],
    }


app = Flask(__name__)


@app.route("/.well-known/webfinger")
def webfinger():
    resource = request.args.get("resource", "")
    if resource != f"acct:mock-alice@{DOMAIN}":
        return "", 404
    return jsonify(
        {
            "subject": resource,
            "links": [
                {
                    "rel": "self",
                    "type": "application/activity+json",
                    "href": ACTOR_ID,
                }
            ],
        }
    )


@app.route("/users/mock-alice")
def actor():
    resp = jsonify(build_actor_json())
    resp.headers["Content-Type"] = "application/activity+json"
    return resp


@app.route("/users/mock-alice/inbox", methods=["POST"])
@app.route("/inbox", methods=["POST"])
def inbox():
    # HTTP Signature verify: mk-A からの POST には keyId + signature が乗る。
    raw_body = request.get_data()
    sig_header = request.headers.get("Signature", "")
    if not sig_header:
        return jsonify({"error": "missing signature"}), 401

    import re

    fields = dict(re.findall(r'(\w+)="([^"]*)"', sig_header))
    key_id = fields.get("keyId", "")
    algorithm = fields.get("algorithm", "rsa-sha256")
    if not key_id:
        return jsonify({"error": "missing keyId"}), 401

    public_key = _resolve_remote_public_key(key_id)
    if public_key is None:
        return jsonify({"error": "cannot resolve key"}), 401

    norm_headers = {k.lower(): v for k, v in request.headers.items()}
    ok = http_signature_verify(
        method="POST",
        path=request.path,
        headers=norm_headers,
        body=raw_body,
        public_key=public_key,
        algorithm=algorithm,
    )
    if not ok:
        return jsonify({"error": "signature verify failed"}), 401

    try:
        payload = json.loads(raw_body or b"null")
    except json.JSONDecodeError:
        payload = None
    with _received_lock:
        _received.append(
            {
                "key_id": key_id,
                "algorithm": algorithm,
                "activity": payload,
            }
        )
    return "", 202


@app.route("/_test/inbox-log")
def inbox_log():
    """test 用 debug API: 受信した全 activity を返す。"""
    with _received_lock:
        return jsonify(list(_received))


@app.route("/_test/deliver", methods=["POST"])
def test_deliver():
    """test 用 API: mock が任意 inbox に対して指定 algorithm で sign + POST する。

    Body: {"target": "<inbox URL>", "activity": {...}, "algorithm": "ed25519" or "rsa-sha256"}
    """
    cmd = request.get_json(force=True) or {}
    target = cmd.get("target")
    activity = cmd.get("activity") or {}
    algorithm = cmd.get("algorithm", "ed25519")

    if not target:
        return jsonify({"error": "missing target"}), 400

    if algorithm == "ed25519":
        key_id = f"{ACTOR_ID}#ed25519-key"
        priv = _ed_priv
    elif algorithm == "rsa-sha256":
        key_id = f"{ACTOR_ID}#main-key"
        priv = _rsa_priv
    else:
        return jsonify({"error": f"unsupported algorithm: {algorithm}"}), 400

    body = json.dumps(activity).encode()
    resp = post_signed(
        url=target,
        body=body,
        key_id=key_id,
        private_key=priv,
        algorithm=algorithm,
        verify_tls=False,
    )
    return jsonify({"status": resp.status_code, "body": resp.text[:512]}), 200


@app.route("/_test/healthz")
def healthz():
    return jsonify({"ok": True, "actor": ACTOR_ID})


if __name__ == "__main__":
    # TLS cert は外部 mount で /certs/server.crt + /certs/server.key を期待する。
    cert = os.environ.get("FEDIBIRD_TLS_CERT", "/certs/server.crt")
    key = os.environ.get("FEDIBIRD_TLS_KEY", "/certs/server.key")
    app.run(host="0.0.0.0", port=443, ssl_context=(cert, key))
