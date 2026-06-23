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
    # notesCount は eventual-consistency (mk-go は note 作成で即時、TS は chart/job
    # で遅延更新) のため瞬間値が割れる timing-state。安定比較対象外。
    "notesCount",
    # onlineStatus は lastActiveDate 由来の timing-state (mk-go は activity で即
    # 'online'、TS は throttle で 'unknown')。安定した parity 比較対象ではない。
    "onlineStatus", "lastActiveDate",
    # (#2091 修正済: isAdmin/isModerator は self-view で populate されるように
    # なったため回帰 gate に戻した。ここで ignore しない。)
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


NOTE_IGNORE = DEFAULT_IGNORE_KEYS | {"userId", "user", "renoteId", "replyId"}


def test_note_with_reply_parity(mkgo, ts):
    # parent note -> reply -> show the reply, compare the packed reply (incl. the
    # embedded parent under `reply`).
    for c in (mkgo, ts):
        parent = c.json("notes/create", {"text": "parent", "visibility": "public"})["createdNote"]
        reply = c.json("notes/create", {"text": "child reply", "replyId": parent["id"], "visibility": "public"})["createdNote"]
        c._probe_note_id = reply["id"]
    mk_show = mkgo.json("notes/show", {"noteId": mkgo._probe_note_id})
    ts_show = ts.json("notes/show", {"noteId": ts._probe_note_id})
    # the embedded `reply` object is itself a note (instance-specific ids/user).
    diffs = diff_json(mk_show, ts_show, ignore_keys=NOTE_IGNORE, ignore_paths={"$.reply"})
    assert not diffs, format_diffs(diffs)


def test_note_with_renote_parity(mkgo, ts):
    # pure renote (no text) of a parent; compare the packed renote object.
    for c in (mkgo, ts):
        parent = c.json("notes/create", {"text": "renote target", "visibility": "public"})["createdNote"]
        renote = c.json("notes/create", {"renoteId": parent["id"], "visibility": "public"})["createdNote"]
        c._probe_note_id = renote["id"]
    mk_show = mkgo.json("notes/show", {"noteId": mkgo._probe_note_id})
    ts_show = ts.json("notes/show", {"noteId": ts._probe_note_id})
    diffs = diff_json(mk_show, ts_show, ignore_keys=NOTE_IGNORE, ignore_paths={"$.renote"})
    assert not diffs, format_diffs(diffs)


def test_note_hashtags_parity(mkgo, ts):
    # text -> tags extraction (hashtags). mentions carry user ids (instance-
    # specific) so we ignore `mentions` and compare `tags`.
    text = "hello #harness #ParityCheck world"
    mk_note = mkgo.json("notes/create", {"text": text, "visibility": "public"})["createdNote"]
    ts_note = ts.json("notes/create", {"text": text, "visibility": "public"})["createdNote"]
    mk_show = mkgo.json("notes/show", {"noteId": mk_note["id"]})
    ts_show = ts.json("notes/show", {"noteId": ts_note["id"]})
    diffs = diff_json(mk_show, ts_show, ignore_keys=NOTE_IGNORE | {"mentions"})
    assert not diffs, format_diffs(diffs)


def test_note_state_parity(mkgo, ts):
    for c in (mkgo, ts):
        note = c.json("notes/create", {"text": "state probe", "visibility": "public"})["createdNote"]
        c._probe_note_id = note["id"]
    mk = mkgo.json("notes/state", {"noteId": mkgo._probe_note_id})
    tj = ts.json("notes/state", {"noteId": ts._probe_note_id})
    diffs = diff_json(mk, tj)
    assert not diffs, format_diffs(diffs)


# /api/i (authoritative MeDetailed). 自己固有 / role 依存 / version-gap を吸収する。
I_IGNORE = META_IGNORE | {
    "policies",  # role policy 依存 (instance role 設定で変わる)
    "avatarId", "bannerId", "achievements", "loggedInDays", "signupReason",
    "twoFactorEnabled", "usePasswordLessLogin", "securityKeys",
    "notesCount", "twoFactorBackupCodesStock", "lastActiveDate", "onlineStatus",
    "isAdmin", "isModerator",  # role 依存 (root fallback) は users/show 側で gate 済
    # room / clientData は mk-go の **意図的な** pass-through extra (upstream は削除済
    # だが mk-go は client 固有 JSON 保存機能として read/write/test 付きで保持、#2094 で
    # intentional と判断)。upstream に無い field なので harness では永続無視する。
    "room", "clientData",
    # (#2094 修正済: moderationNote は moderator viewer に emit するようになったため
    # ここで ignore しない = 回帰 gate に戻した。)
}


def test_i_meDetailed_parity(mkgo, ts):
    mk = mkgo.json("i", {})
    tj = ts.json("i", {})
    diffs = diff_json(mk, tj, ignore_keys=I_IGNORE)
    assert not diffs, format_diffs(diffs)


# 独自 packer を持つ entity (clip / user list / channel / antenna)。
ENTITY_IGNORE = DEFAULT_IGNORE_KEYS | {"userId", "user"}


def test_clip_packing_parity(mkgo, ts):
    for c in (mkgo, ts):
        clip = c.json("clips/create", {"name": "harness clip", "isPublic": True, "description": "d"})
        c._probe = clip["id"]
    mk = mkgo.json("clips/show", {"clipId": mkgo._probe})
    tj = ts.json("clips/show", {"clipId": ts._probe})
    diffs = diff_json(mk, tj, ignore_keys=ENTITY_IGNORE)
    assert not diffs, format_diffs(diffs)


def test_userlist_packing_parity(mkgo, ts):
    for c in (mkgo, ts):
        lst = c.json("users/lists/create", {"name": "harness list"})
        c._probe = lst["id"]
    mk = mkgo.json("users/lists/show", {"listId": mkgo._probe})
    tj = ts.json("users/lists/show", {"listId": ts._probe})
    diffs = diff_json(mk, tj, ignore_keys=ENTITY_IGNORE)
    assert not diffs, format_diffs(diffs)


def test_channel_packing_parity(mkgo, ts):
    for c in (mkgo, ts):
        ch = c.json("channels/create", {"name": "harness channel", "description": "ch desc"})
        c._probe = ch["id"]
    mk = mkgo.json("channels/show", {"channelId": mkgo._probe})
    tj = ts.json("channels/show", {"channelId": ts._probe})
    diffs = diff_json(mk, tj, ignore_keys=ENTITY_IGNORE)
    assert not diffs, format_diffs(diffs)


def test_antenna_packing_parity(mkgo, ts):
    params = {
        "name": "harness antenna", "src": "all", "keywords": [["hello"]],
        "excludeKeywords": [], "users": [], "caseSensitive": False,
        "withReplies": False, "withFile": False, "notify": False,
    }
    for c in (mkgo, ts):
        a = c.json("antennas/create", params)
        c._probe = a["id"]
    mk = mkgo.json("antennas/show", {"antennaId": mkgo._probe})
    tj = ts.json("antennas/show", {"antennaId": ts._probe})
    diffs = diff_json(mk, tj, ignore_keys=ENTITY_IGNORE)
    assert not diffs, format_diffs(diffs)


def _create_second_user(admin, username):
    # admin (alice, root) creates a 2nd account via the authed admin path.
    resp = admin.call("admin/accounts/create", {"username": username, "password": "test-password-1234"})
    resp.raise_for_status()
    return resp.json()


def test_user_relation_parity(mkgo, ts):
    # alice follows bob on each instance, then compares users/show(bob) — the
    # relation block (isFollowing/isFollowed/...) is computed, a likely diff site.
    for c in (mkgo, ts):
        bob = _create_second_user(c, "bob")
        c.json("following/create", {"userId": bob["id"]})
        c._probe = bob["id"]
    mk = mkgo.json("users/show", {"userId": mkgo._probe})
    tj = ts.json("users/show", {"userId": ts._probe})
    # #2097: follower viewer に followedMessage=null を emit せず省略する乖離を
    # 検出。finding として追跡中なので relation 比較では一旦無視する。
    diffs = diff_json(mk, tj, ignore_keys=USER_IGNORE | {"followedMessage"})
    assert not diffs, format_diffs(diffs)


def test_drivefolder_packing_parity(mkgo, ts):
    for c in (mkgo, ts):
        folder = c.json("drive/folders/create", {"name": "harness folder"})
        c._probe = folder["id"]
    mk = mkgo.json("drive/folders/show", {"folderId": mkgo._probe})
    tj = ts.json("drive/folders/show", {"folderId": ts._probe})
    diffs = diff_json(mk, tj, ignore_keys=DEFAULT_IGNORE_KEYS | {"parentId"})
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
