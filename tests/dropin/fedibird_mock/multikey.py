"""FEP-521a Multikey encode helper for Ed25519 public keys.

Mirrors the canonical "z" + base58btc(multicodec || raw key) format that
mk-go's `internal/activitypub/multikey.go` produces. Kept dependency-free
(uses our own minimal base58btc impl) so the Fedibird mock container stays
small (alpine + stdlib + cryptography + flask only).
"""

from __future__ import annotations

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

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
