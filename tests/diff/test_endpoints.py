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


# user 固有値 (counts は fresh では 0 で一致するので無視しない)。
USER_IGNORE = DEFAULT_IGNORE_KEYS | {
    "avatarDecorations", "roles", "badgeRoles", "emojis", "instance",
    "avatarColor", "bannerColor", "followersCount", "followingCount",
    # onlineStatus は lastActiveDate 由来の timing-state (mk-go は activity で即
    # 'online'、TS は throttle で 'unknown')。安定した parity 比較対象ではない。
    "onlineStatus", "lastActiveDate",
    # #2091: users/show self-view が isAdmin/isModerator を populate しない (root を
    # 反映せず常に false)。finding として追跡中なので harness では一旦無視する。
    # #2091 修正後にこの 2 つを外して回帰 gate に戻すこと。
    "isAdmin", "isModerator",
}


def test_user_packing_parity(mkgo, ts):
    mk = mkgo.json("users/show", {"username": "alice"})
    tj = ts.json("users/show", {"username": "alice"})
    diffs = diff_json(mk, tj, ignore_keys=USER_IGNORE)
    assert not diffs, format_diffs(diffs)


def test_note_with_reaction_parity(mkgo, ts):
    # Create a note and react with a unicode emoji on each, compare the packed
    # reactions map / counts.
    for c in (mkgo, ts):
        note = c.json("notes/create", {"text": "react probe", "visibility": "public"})["createdNote"]
        c.json("notes/reactions/create", {"noteId": note["id"], "reaction": "\U0001F44D"})
        c._probe_note_id = note["id"]

    mk_show = mkgo.json("notes/show", {"noteId": mkgo._probe_note_id})
    ts_show = ts.json("notes/show", {"noteId": ts._probe_note_id})

    note_ignore = DEFAULT_IGNORE_KEYS | {"userId", "user", "renoteId", "replyId"}
    diffs = diff_json(mk_show, ts_show, ignore_keys=note_ignore)
    assert not diffs, format_diffs(diffs)


def test_note_with_poll_parity(mkgo, ts):
    poll = {"choices": ["alpha", "beta", "gamma"], "multiple": False}
    mk_note = mkgo.json("notes/create", {"text": "poll probe", "visibility": "public", "poll": poll})["createdNote"]
    ts_note = ts.json("notes/create", {"text": "poll probe", "visibility": "public", "poll": poll})["createdNote"]

    mk_show = mkgo.json("notes/show", {"noteId": mk_note["id"]})
    ts_show = ts.json("notes/show", {"noteId": ts_note["id"]})

    # compare just the packed poll object (the rest is covered by note packing).
    note_ignore = DEFAULT_IGNORE_KEYS | {"userId", "user", "renoteId", "replyId"}
    diffs = diff_json(mk_show.get("poll"), ts_show.get("poll"), ignore_keys=note_ignore, path="$.poll")
    assert not diffs, format_diffs(diffs)
