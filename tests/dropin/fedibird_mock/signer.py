"""HTTP Signature sign / verify helper used by the Fedibird mock.

Supports both RSA-SHA256 (Cavage-style "rsa-sha256") and Ed25519 signatures
("ed25519" / "hs2019" with Ed25519 key) so the mock can interoperate with
mk-go's signature.go verify path and the dual-key actor JSON it publishes.
"""

from __future__ import annotations

import base64
import hashlib
import re
from typing import Any
from urllib.parse import urlparse

import requests
from cryptography.exceptions import InvalidSignature
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ed25519, padding, rsa


def http_signature_sign(
    *,
    method: str,
    url: str,
    body: bytes,
    key_id: str,
    private_key,
    algorithm: str,
) -> dict[str, str]:
    """Return headers (Host / Date / Digest / Signature) for the request.

    algorithm = "rsa-sha256" or "ed25519". 他値は ValueError。
    """
    from email.utils import formatdate

    parsed = urlparse(url)
    host = parsed.netloc
    path = parsed.path or "/"
    digest = "SHA-256=" + base64.b64encode(hashlib.sha256(body).digest()).decode()
    date = formatdate(timeval=None, localtime=False, usegmt=True)

    signing_lines = [
        f"(request-target): {method.lower()} {path}",
        f"host: {host}",
        f"date: {date}",
        f"digest: {digest}",
    ]
    signing_input = "\n".join(signing_lines).encode()

    if algorithm == "rsa-sha256":
        sig_bytes = private_key.sign(signing_input, padding.PKCS1v15(), hashes.SHA256())
    elif algorithm == "ed25519":
        sig_bytes = private_key.sign(signing_input)
    else:
        raise ValueError(f"unsupported algorithm: {algorithm}")
    sig_b64 = base64.b64encode(sig_bytes).decode()

    headers_param = '(request-target) host date digest'
    sig_header = (
        f'keyId="{key_id}",algorithm="{algorithm}",headers="{headers_param}",'
        f'signature="{sig_b64}"'
    )
    return {
        "Host": host,
        "Date": date,
        "Digest": digest,
        "Signature": sig_header,
        "Content-Type": "application/activity+json",
    }


_SIG_FIELD_RE = re.compile(r'(\w+)="([^"]*)"')


def http_signature_verify(
    *,
    method: str,
    path: str,
    headers: dict[str, str],
    body: bytes,
    public_key,
    algorithm: str,
) -> bool:
    """Verify a Cavage-style HTTP Signature header against the supplied key.

    headers は **lowercased keys** で渡すこと (Flask の request.headers は
    case-insensitive だが本 helper は normalize 前提)。
    """
    sig_header = headers.get("signature") or ""
    fields = dict(_SIG_FIELD_RE.findall(sig_header))
    header_list = fields.get("headers", "").split()
    sig_bytes = base64.b64decode(fields.get("signature", ""))

    def _lookup(name: str) -> str:
        if name == "(request-target)":
            return f"{method.lower()} {path}"
        return headers.get(name.lower(), "")

    signing_input = "\n".join(f"{h}: {_lookup(h)}" if h != "(request-target)" else f"(request-target): {method.lower()} {path}" for h in header_list).encode()

    try:
        if algorithm == "rsa-sha256":
            public_key.verify(sig_bytes, signing_input, padding.PKCS1v15(), hashes.SHA256())
        elif algorithm in ("ed25519", "ed25519-sha512", "hs2019"):
            public_key.verify(sig_bytes, signing_input)
        else:
            return False
    except InvalidSignature:
        return False
    return True


def parse_pem_public_key(pem: str):
    """Decode a PEM public key (PKIX). Returns RSAPublicKey or Ed25519PublicKey."""
    return serialization.load_pem_public_key(pem.encode())


def post_signed(
    *,
    url: str,
    body: bytes,
    key_id: str,
    private_key,
    algorithm: str,
    verify_tls: bool = False,
    timeout: float = 10.0,
) -> requests.Response:
    """Build signed headers and POST. body は raw JSON bytes を渡すこと。"""
    headers = http_signature_sign(
        method="POST",
        url=url,
        body=body,
        key_id=key_id,
        private_key=private_key,
        algorithm=algorithm,
    )
    return requests.post(url, data=body, headers=headers, verify=verify_tls, timeout=timeout)
