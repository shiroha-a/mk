"""Differential JSON comparison for the mk-go <-> Misskey-TS e2e diff harness (#2089).

The entitycompat golden gate covers error code/id/status and field *presence*
(shape). It cannot check field *values* (computed results, normalization,
ordering, conditional branches). This module compares two API responses
field-by-field, ignoring instance-specific keys (ids, timestamps, host/uri/url,
tokens, etc.) so the remaining diffs are value-level parity candidates between
mk-go (2026.6.0) and Misskey TS (2026.5.4).

Pure stdlib so it is unit-testable without the running stack
(`python3 tests/diff/test_diff_core.py`).
"""

from __future__ import annotations

# Keys whose values legitimately differ between two independent instances and
# therefore must not be reported as parity diffs. Conservative by design: better
# to under-report (miss a diff) than drown real findings in instance noise.
# Extend per-endpoint via the `ignore_keys` / `ignore_paths` args when needed.
DEFAULT_IGNORE_KEYS = frozenset({
    # identity / time — always instance-specific
    "id", "createdAt", "updatedAt", "lastFetchedAt", "fetchedAt", "expiresAt",
    "lastActiveDate", "updatedAtHistory", "lastNotedAt", "approvalDate",
    # host / addressing — differ by instance domain
    "host", "uri", "url", "avatarUrl", "bannerUrl", "backgroundUrl",
    "avatarBlurhash", "bannerBlurhash", "src", "thumbnailUrl", "webpublicUrl",
    # secrets / per-instance tokens & keys
    "token", "i", "publicKey", "sshKey", "secret", "password",
    # version / build identifiers (mk-go reports its own version string)
    "version", "clientEntry", "clientManifestExists",
})


class Diff:
    """A single field-level discrepancy.

    kind is one of:
      - "value":           same path, different scalar value
      - "type":            different JSON types at the same path
      - "length":          arrays of different length
      - "missing_in_mkgo": key present in TS but absent in mk-go
      - "missing_in_ts":   key present in mk-go but absent in TS
    """

    __slots__ = ("path", "kind", "mkgo", "ts")

    def __init__(self, path: str, kind: str, mkgo: object, ts: object) -> None:
        self.path = path
        self.kind = kind
        self.mkgo = mkgo
        self.ts = ts

    def __repr__(self) -> str:
        return f"Diff({self.path!r}, {self.kind!r}, mkgo={self.mkgo!r}, ts={self.ts!r})"

    def __eq__(self, other: object) -> bool:
        return isinstance(other, Diff) and (self.path, self.kind, self.mkgo, self.ts) == (
            other.path, other.kind, other.mkgo, other.ts,
        )


def diff_json(mkgo, ts, *, ignore_keys=DEFAULT_IGNORE_KEYS, ignore_paths=frozenset(), path="$"):
    """Return the list of field-level diffs between mkgo and ts responses.

    ignore_keys: object keys to skip anywhere they appear (default instance noise).
    ignore_paths: fully-qualified `$.a.b[0].c` paths to skip (per-endpoint tuning).
    """
    out: list[Diff] = []
    _walk(mkgo, ts, path, ignore_keys, ignore_paths, out)
    return out


def _walk(a, b, path, ignore_keys, ignore_paths, out):
    if path in ignore_paths:
        return
    # int/float are interchangeable JSON numbers; bool is NOT a number here.
    if not _same_kind(a, b):
        out.append(Diff(path, "type", a, b))
        return
    if isinstance(a, dict):
        for key in sorted(set(a.keys()) | set(b.keys())):
            if key in ignore_keys:
                continue
            child = f"{path}.{key}"
            if child in ignore_paths:
                continue
            if key not in a:
                out.append(Diff(child, "missing_in_mkgo", None, b[key]))
            elif key not in b:
                out.append(Diff(child, "missing_in_ts", a[key], None))
            else:
                _walk(a[key], b[key], child, ignore_keys, ignore_paths, out)
    elif isinstance(a, list):
        if len(a) != len(b):
            out.append(Diff(path, "length", len(a), len(b)))
        for i in range(min(len(a), len(b))):
            _walk(a[i], b[i], f"{path}[{i}]", ignore_keys, ignore_paths, out)
    else:
        if a != b:
            out.append(Diff(path, "value", a, b))


def _same_kind(a, b) -> bool:
    """Whether a and b are the same JSON kind. int/float unified; bool distinct."""
    if isinstance(a, bool) or isinstance(b, bool):
        return isinstance(a, bool) and isinstance(b, bool)
    if isinstance(a, (int, float)) and isinstance(b, (int, float)):
        return True
    return type(a) is type(b)


def format_diffs(diffs) -> str:
    """Human-readable report (one line per diff) for test failure messages."""
    if not diffs:
        return "(no diffs)"
    lines = [f"{len(diffs)} value-level diff(s):"]
    for d in diffs:
        lines.append(f"  [{d.kind}] {d.path}: mkgo={d.mkgo!r} ts={d.ts!r}")
    return "\n".join(lines)
