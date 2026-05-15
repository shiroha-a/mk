"""FEP-521a Multikey encode helper for Ed25519 public keys.

Mirrors the canonical "z" + base58btc(multicodec || raw key) format that
mk-go's `internal/activitypub/multikey.go` produces. Kept dependency-free
(uses our own minimal base58btc impl) so the Fedibird mock container stays
small (alpine + stdlib + cryptography + flask only).
"""

from __future__ import annotations

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

_BASE58_INDEX = {c: i for i, c in enumerate(
    "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
)}

# Multicodec varint prefix for Ed25519 public key (table value 0xed → varint 0xed 0x01).
ED25519_MULTICODEC_PREFIX = bytes([0xED, 0x01])

# Bitcoin base58 alphabet (Bitcoin / IPFS / Multibase Base58BTC).
_BASE58_ALPHABET = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"


def _b58encode(payload: bytes) -> str:
    """Base58BTC encode without external dep."""
    # Count leading zero bytes — each becomes a leading '1' in Base58BTC.
    n_zeros = 0
    for b in payload:
        if b == 0:
            n_zeros += 1
        else:
            break
    num = int.from_bytes(payload, "big")
    out = ""
    while num > 0:
        num, rem = divmod(num, 58)
        out = _BASE58_ALPHABET[rem] + out
    return ("1" * n_zeros) + out


def encode_ed25519_multikey(pub: Ed25519PublicKey) -> str:
    """Return the Multibase "z6Mk..." representation of an Ed25519 public key."""
    raw = pub.public_bytes(
        encoding=serialization.Encoding.Raw,
        format=serialization.PublicFormat.Raw,
    )
    payload = ED25519_MULTICODEC_PREFIX + raw
    return "z" + _b58encode(payload)


def _b58decode(s: str) -> bytes:
    """Base58BTC decode (= inverse of _b58encode). Raises ValueError on
    invalid characters."""
    n_zeros = 0
    for c in s:
        if c == "1":
            n_zeros += 1
        else:
            break
    num = 0
    for c in s:
        if c not in _BASE58_INDEX:
            raise ValueError(f"invalid base58btc character: {c!r}")
        num = num * 58 + _BASE58_INDEX[c]
    raw = num.to_bytes((num.bit_length() + 7) // 8, "big") if num > 0 else b""
    return b"\x00" * n_zeros + raw


def decode_ed25519_multikey(s: str) -> Ed25519PublicKey:
    """Parse a Multibase "z6Mk..." string into an Ed25519 public key.

    Raises ValueError when the input is not a base58btc Multibase value,
    the multicodec prefix is not Ed25519, or the embedded key is not the
    canonical 32 bytes.
    """
    if not s.startswith("z"):
        raise ValueError(f"expected base58btc multibase ('z' prefix), got: {s[:8]!r}")
    payload = _b58decode(s[1:])
    if len(payload) < len(ED25519_MULTICODEC_PREFIX):
        raise ValueError("payload too short")
    if payload[: len(ED25519_MULTICODEC_PREFIX)] != ED25519_MULTICODEC_PREFIX:
        raise ValueError("not an Ed25519 multicodec prefix")
    raw = payload[len(ED25519_MULTICODEC_PREFIX) :]
    if len(raw) != 32:
        raise ValueError(f"Ed25519 key size {len(raw)} != 32")
    return Ed25519PublicKey.from_public_bytes(raw)
