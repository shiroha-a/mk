"""Unit tests for diff_core (#2089). Runnable standalone (no pytest needed):

    python3 tests/diff/test_diff_core.py

Also collected by pytest inside the diff-runner container.
"""

from __future__ import annotations

from diff_core import DEFAULT_IGNORE_KEYS, Diff, diff_json, format_diffs


def test_identical_no_diffs():
    a = {"name": "x", "count": 3, "nested": {"a": [1, 2]}}
    assert diff_json(a, dict(a)) == []


def test_scalar_value_diff():
    diffs = diff_json({"k": "mk"}, {"k": "ts"})
    assert diffs == [Diff("$.k", "value", "mk", "ts")]


def test_ignore_keys_skipped():
    # id / createdAt / host differ per instance but must be ignored.
    mk = {"id": "aaa", "createdAt": "2026-01-01", "host": "mk.local", "name": "alice"}
    ts = {"id": "bbb", "createdAt": "2026-02-02", "host": "ts.local", "name": "alice"}
    assert diff_json(mk, ts) == []


def test_ignore_keys_skipped_when_value_actually_differs():
    # even differing name is caught, but id/host noise is not.
    mk = {"id": "a", "host": "m", "name": "alice"}
    ts = {"id": "b", "host": "t", "name": "ALICE"}
    assert diff_json(mk, ts) == [Diff("$.name", "value", "alice", "ALICE")]


def test_missing_in_each_direction():
    diffs = diff_json({"only_mk": 1, "shared": 2}, {"only_ts": 9, "shared": 2})
    by_path = {d.path: d for d in diffs}
    assert by_path["$.only_mk"].kind == "missing_in_ts"
    assert by_path["$.only_ts"].kind == "missing_in_mkgo"
    assert "$.shared" not in by_path


def test_type_mismatch():
    diffs = diff_json({"k": "3"}, {"k": 3})
    assert diffs == [Diff("$.k", "type", "3", 3)]


def test_int_float_unified():
    # JSON numbers: 3 and 3.0 are equal, no diff.
    assert diff_json({"k": 3}, {"k": 3.0}) == []


def test_bool_not_a_number():
    diffs = diff_json({"k": True}, {"k": 1})
    assert len(diffs) == 1 and diffs[0].kind == "type"


def test_list_length_and_elementwise():
    diffs = diff_json({"xs": [1, 2, 3]}, {"xs": [1, 9]})
    kinds = {(d.path, d.kind) for d in diffs}
    assert ("$.xs", "length") in kinds
    assert ("$.xs[1]", "value") in kinds


def test_nested_path():
    diffs = diff_json({"a": {"b": {"c": 1}}}, {"a": {"b": {"c": 2}}})
    assert diffs == [Diff("$.a.b.c", "value", 1, 2)]


def test_ignore_paths():
    mk = {"meta": {"flaky": "x", "stable": "v"}}
    ts = {"meta": {"flaky": "y", "stable": "v"}}
    assert diff_json(mk, ts, ignore_paths={"$.meta.flaky"}) == []


def test_extra_ignore_keys_param():
    mk = {"custom": "a", "name": "n"}
    ts = {"custom": "b", "name": "n"}
    assert diff_json(mk, ts, ignore_keys=DEFAULT_IGNORE_KEYS | {"custom"}) == []


def test_format_diffs_readable():
    out = format_diffs([Diff("$.k", "value", "m", "t")])
    assert "$.k" in out and "value" in out
    assert format_diffs([]) == "(no diffs)"


def _run_standalone():
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_") and callable(v)]
    failed = 0
    for fn in fns:
        try:
            fn()
            print(f"PASS {fn.__name__}")
        except AssertionError as e:
            failed += 1
            print(f"FAIL {fn.__name__}: {e}")
    print(f"\n{len(fns) - failed}/{len(fns)} passed")
    return failed


if __name__ == "__main__":
    import sys

    sys.exit(1 if _run_standalone() else 0)
