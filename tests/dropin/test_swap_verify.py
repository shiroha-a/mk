"""Phase 13-2 (#367) drop-in swap シナリオ: verify 段階。

`run-swap-test.sh` が test_swap_setup.py を実行 → backend を mk-go に差し替え
→ この test を実行する流れ。instance A の URL は変わらず https://a/ のまま
だが、その背後で動いている backend が TS から mk-go に切り替わっている。

DB-A / Redis-A は無事なので alice の token / follow 関係 / home timeline 内容
がすべて引き継がれているはず。さらに mk-A 上で alice が新しい操作 (reply,
reaction) を行い、TS-B 側に federation 経由で届くことも確認する。
"""

from __future__ import annotations

import time

from conftest import A_DOMAIN  # type: ignore[import-not-found]
from conftest_base import MisskeyLikeClient, poll_until  # type: ignore[import-not-found]
from test_swap_setup import (  # type: ignore[import-not-found]
    BASELINE_NOTE_TEXT,
    FOLLOWERS_NOTE_TEXT,
    HOME_NOTE_TEXT,
    LIST_NAME,
    SPECIFIED_NOTE_TEXT,
)


def test_post_swap_baseline_note_preserved(
    instance_a: MisskeyLikeClient,
    alice: dict,
) -> None:
    """切替前に bob が投稿し alice の home に届いていた note が、mk-A でも
    引き続き読めることを確認する。Redis prefix が TS と揃っていなければ
    ここでタイムラインが空になる (まさに #362 の症状)。
    """
    tl = instance_a._api("notes/timeline", {"limit": 40})
    assert any(n.get("text") == BASELINE_NOTE_TEXT for n in tl), \
        "baseline note disappeared from alice's home after backend swap"


def test_post_swap_home_visibility_preserved(
    instance_a: MisskeyLikeClient,
    alice: dict,
) -> None:
    """home visibility ノートも切替後に home timeline に残っている。"""
    tl = instance_a._api("notes/timeline", {"limit": 40})
    assert any(n.get("text") == HOME_NOTE_TEXT for n in tl), \
        "home-visibility note missing from alice's home after swap"


def test_post_swap_followers_visibility_preserved(
    instance_a: MisskeyLikeClient,
    alice: dict,
) -> None:
    """followers visibility ノートも切替後に home timeline に残っている。"""
    tl = instance_a._api("notes/timeline", {"limit": 40})
    assert any(n.get("text") == FOLLOWERS_NOTE_TEXT for n in tl), \
        "followers-visibility note missing from alice's home after swap"


def test_post_swap_specified_note_preserved(
    instance_a: MisskeyLikeClient,
    alice: dict,
) -> None:
    """specified visibility (DM) ノートが /api/notes/mentions?visibility=specified
    に残っている。
    """
    mentions = instance_a._api(
        "notes/mentions", {"limit": 40, "visibility": "specified"}
    )
    assert any(n.get("text") == SPECIFIED_NOTE_TEXT for n in mentions), \
        "specified-visibility DM missing from alice's mentions after swap"


def test_post_swap_user_list_preserved(
    instance_a: MisskeyLikeClient,
    alice: dict,
) -> None:
    """alice の user list 自体が引き継がれている (list メタデータの存在確認)。

    membership 構成の検証は test_post_swap_user_list_timeline_preserved で
    user-list-timeline 経由 (= 実際に list メンバーのノートが返る) で間接的に
    行う。mk-go の `users/lists/show` は TS 互換の `userIds` フィールドを
    返さない既知の API gap があるため、ここでは list のメタ情報のみ確認。
    """
    lists = instance_a._api("users/lists/list")
    target = next((l for l in lists if l.get("name") == LIST_NAME), None)
    assert target is not None, f"user list '{LIST_NAME}' missing after swap"


def test_post_swap_user_list_timeline_preserved(
    instance_a: MisskeyLikeClient,
    alice: dict,
) -> None:
    """user list timeline で baseline note (bob 由来) が引き続き読める。

    /api/notes/user-list-timeline は user_list_membership JOIN クエリで
    list メンバーのノートを集める。Redis fanout キャッシュが残っていれば
    そこから、空なら DB fallback で同じ結果になる。
    """
    lists = instance_a._api("users/lists/list")
    target = next((l for l in lists if l.get("name") == LIST_NAME), None)
    assert target is not None

    tl = instance_a._api(
        "notes/user-list-timeline",
        {"listId": target["id"], "limit": 40},
    )
    assert any(n.get("text") == BASELINE_NOTE_TEXT for n in tl), \
        "user-list-timeline lost baseline note from list member after swap"


def test_post_swap_alice_can_reply(
    instance_a: MisskeyLikeClient,
    instance_b: MisskeyLikeClient,
    alice: dict,
    bob: dict,
) -> None:
    """mk-A で alice が新しいリプライを投稿し、TS-B の bob 側に届くことを確認する。

    federation deliver のキュー (asynq) が稼働していれば届く。これが届かない
    場合は mk-go の deliver 処理 / SSL_CERT_FILE バンドル / inbox URL 解決
    のいずれかが壊れている。
    """
    tl = instance_a._api("notes/timeline", {"limit": 40})
    baseline = next((n for n in tl if n.get("text") == BASELINE_NOTE_TEXT), None)
    assert baseline is not None, "baseline note missing — cannot reply"

    reply_text = f"dropin-reply-{int(time.time())}"
    instance_a._api(
        "notes/create",
        {"text": reply_text, "replyId": baseline["id"]},
    )

    def _arrived() -> bool:
        notifications = instance_b.get_notifications(limit=20)
        return any(
            (n.get("note") or {}).get("text") == reply_text
            for n in notifications
        )

    assert poll_until(_arrived, timeout=120, desc="bob receives reply from mk-A alice")


def test_post_swap_alice_actor_exposes_ed25519_assertion_method(
    instance_a: MisskeyLikeClient,
    alice: dict,
) -> None:
    """P6 (#1073): mk-A 切替後に alice の actor JSON が FEP-521a Multikey
    形式で Ed25519 公開鍵を `assertionMethod[]` として expose することを
    検証する。alice は TS-A で signup された旧 user (= P1 マイグレーション前)
    なので、user_keypair_extra に行が無い状態 → mk-A の lazy backfill が
    動いて新規 Ed25519 鍵を発行・保存 → actor JSON で expose、の e2e 経路を
    確認する。
    """
    resp = instance_a.http.get(
        f"/users/{alice['id']}",
        headers={"Accept": "application/activity+json"},
    )
    assert resp.status_code == 200, (
        f"expected 200 from actor endpoint, got {resp.status_code}"
    )
    actor = resp.json()

    # @context に Multikey / Data-Integrity vocab が含まれる
    ctx = actor.get("@context")
    assert isinstance(ctx, list), "@context should be an array for Person actor"
    ctx_strs = [c for c in ctx if isinstance(c, str)]
    assert "https://w3id.org/security/multikey/v1" in ctx_strs, (
        "Multikey @context entry missing — lazy backfill or renderer drift"
    )

    # assertionMethod[0] が Ed25519 Multikey 形式
    ams = actor.get("assertionMethod")
    assert isinstance(ams, list) and len(ams) >= 1, (
        f"lazy backfill did not expose assertionMethod (got: {ams!r})"
    )
    am = ams[0]
    assert am.get("type") == "Multikey", f"unexpected type: {am.get('type')!r}"
    assert am.get("controller", "").endswith(f"/users/{alice['id']}")
    assert am.get("id", "").endswith("#ed25519-key"), (
        f"keyId fragment must be #ed25519-key (got: {am.get('id')!r})"
    )
    pkb = am.get("publicKeyMultibase", "")
    assert pkb.startswith("z6Mk"), (
        f"Ed25519 Multikey prefix should be z6Mk... (got: {pkb[:8]!r})"
    )

    # publicKey (RSA) も並行で expose されている — drop-in 互換維持
    pub = actor.get("publicKey", {})
    assert "publicKeyPem" in pub, "RSA publicKey must coexist with assertionMethod"


def test_post_swap_alice_actor_ed25519_stable_across_refetch(
    instance_a: MisskeyLikeClient,
    alice: dict,
) -> None:
    """P6 (#1073): backfill 後の Ed25519 鍵が persistent であることを確認。
    2 回連続で actor JSON を fetch して publicKeyMultibase が同一であれば、
    lazy backfill が初回のみ実行され、以後は user_keypair_extra に永続化
    された行が再利用されている (= 鍵 rotation していない)。
    """
    def _fetch_multikey() -> str:
        resp = instance_a.http.get(
            f"/users/{alice['id']}",
            headers={"Accept": "application/activity+json"},
        )
        assert resp.status_code == 200
        actor = resp.json()
        ams = actor.get("assertionMethod") or []
        assert ams and isinstance(ams, list)
        return ams[0].get("publicKeyMultibase", "")

    first = _fetch_multikey()
    second = _fetch_multikey()
    assert first == second, (
        "publicKeyMultibase changed across re-fetch — backfill is not "
        "persisted or InsertIfAbsent semantics broken"
    )
    assert first.startswith("z6Mk")


def test_post_swap_alice_can_react(
    instance_a: MisskeyLikeClient,
    instance_b: MisskeyLikeClient,
    alice: dict,
    bob: dict,
) -> None:
    """mk-A の alice が baseline note にリアクションを付け、TS-B の bob 側に
    届くことを確認する。
    """
    tl = instance_a._api("notes/timeline", {"limit": 40})
    baseline = next((n for n in tl if n.get("text") == BASELINE_NOTE_TEXT), None)
    assert baseline is not None

    try:
        instance_a.react(baseline["id"], "👍")
    except RuntimeError as e:
        # 切替前に既にリアクション済みの場合は許容 (ALREADY_REACTED 等)
        if "ALREADY" not in str(e).upper():
            raise

    def _arrived() -> bool:
        notifications = instance_b.get_notifications(limit=20)
        for n in notifications:
            if n.get("type") != "reaction":
                continue
            # alice は instance B から見ると remote (host = A_DOMAIN = "a")。
            # 旧実装は `host in (None, "")` で local user を許容していたが、
            # それだと B 上の任意の local user の reaction が誤マッチして
            # xfail(strict=True) を意図せず通してしまう (Devin #370 #2)。
            from_user = n.get("user") or {}
            if from_user.get("username") == "alice" and from_user.get("host") == A_DOMAIN:
                return True
        return False

    assert poll_until(_arrived, timeout=120, desc="bob receives reaction from mk-A alice")
