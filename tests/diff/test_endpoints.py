"""Differential endpoint tests (#2089): call the same endpoint on mk-go and TS,
diff the JSON responses, fail on value-level deviations.

This is the PoC coverage (meta + note packing). Extend with more endpoints as
the harness matures; each confirmed deviation should become a #2078 sub-issue.

Run via the diff-runner container: `make diff-test`.
"""

from __future__ import annotations

from diff_core import DEFAULT_IGNORE_KEYS, diff_json, format_diffs

# meta carries a lot of operator-specific config (names, urls, versions, limits
# that depend on the instance config). Ignore those so the diff focuses on
# structural/value parity of the shared fields.
META_IGNORE = DEFAULT_IGNORE_KEYS | {
    # operator config strings / theming
    "name", "shortName", "description", "maintainerName", "maintainerEmail",
    "tosUrl", "privacyPolicyUrl", "inquiryUrl", "repositoryUrl", "feedbackUrl",
    "serverRules", "themeColor", "bannerUrl", "backgroundImageUrl", "logoImageUrl",
    "iconUrl", "infoImageUrl", "notFoundImageUrl", "serverErrorImageUrl",
    "defaultLightTheme", "defaultDarkTheme", "mascotImageUrl", "languages",
    # instance-specific addressing / state (host-derived URL, per-instance proxy account)
    "mediaProxy", "proxyAccountName", "proxyAccountId",
    # version-gap (mk-go 2026.6.0 が持ち TS 2026.5.4 に無い field)。golden gate が
    # 2026.6.0 golden で presence を担保済なので diff harness では noise として無視。
    # version-matched TS に切替えたら見直す。
    "app192IconUrl", "app512IconUrl", "singleUserMode",
    "globalTimeline", "localTimeline",
}


def test_meta_value_parity(mkgo, ts):
    mk = mkgo.json("meta", {"detail": True})
    tj = ts.json("meta", {"detail": True})
    diffs = diff_json(mk, tj, ignore_keys=META_IGNORE)
    assert not diffs, format_diffs(diffs)


def test_note_packing_parity(mkgo, ts):
    # Create an equivalent note on each instance and compare the packed object.
    text = "diff-harness parity probe"
    mk_note = mkgo.json("notes/create", {"text": text, "visibility": "public"})["createdNote"]
    ts_note = ts.json("notes/create", {"text": text, "visibility": "public"})["createdNote"]

    mk_show = mkgo.json("notes/show", {"noteId": mk_note["id"]})
    ts_show = ts.json("notes/show", {"noteId": ts_note["id"]})

    # user / author objects are entirely instance-specific (id/host/keys/dates).
    note_ignore = DEFAULT_IGNORE_KEYS | {"userId", "user", "renoteId", "replyId"}
    diffs = diff_json(mk_show, ts_show, ignore_keys=note_ignore)
    assert not diffs, format_diffs(diffs)
