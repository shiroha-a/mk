"""SHOULD scenario (#1082): TS-A 戻し後の連合継続。

`run-swap-test.sh` の stage 9 で実行される。シナリオ:

  TS-A signup
    → test_swap_setup.py で alice/bob/follow/note 構築
    → mk-A 切替 (P5 lazy backfill で user_keypair_extra に Ed25519 鍵発行)
    → test_swap_verify.py で MUST 検証 (#1073)
    → ★ TS-A backend に戻し ★
    → 本ファイル: roundtrip 後の連合継続を検証

期待挙動:
  - TS-A の actor JSON から assertionMethod が消える (Misskey TS は出さない仕様)
  - 既存 RSA publicKey は引き続き expose
  - user_keypair_extra の row は DB に残るが TS は touch しない → 戻し時の
    state 引き継ぎは破壊されない
  - TS-B からの federation (follow / 投稿 / リアクション) が RSA 経由で
    引き続き動く
"""

from __future__ import annotations

import time

from conftest import A_DOMAIN  # type: ignore[import-not-found]
from conftest_base import MisskeyLikeClient, poll_until  # type: ignore[import-not-found]
from test_swap_setup import BASELINE_NOTE_TEXT  # type: ignore[import-not-found]


def test_roundtrip_alice_actor_no_longer_exposes_assertion_method(
    instance_a: MisskeyLikeClient,
    alice: dict,
) -> None:
    """TS-A 戻し後の actor JSON で assertionMethod が消える。Misskey TS は
    `user_keypair_extra` テーブルを認識せず renderer も assertionMethod を
    出さない仕様なので、mk-A 時代に発行された Ed25519 鍵は実質「actor JSON
    上は」消えた扱いになる。既存 RSA publicKey は引き続き expose されて
    いて federation は RSA fallback で継続する (#1082)。
    """
    resp = instance_a.http.get(
        f"/users/{alice['id']}",
        headers={"Accept": "application/activity+json"},
    )
    assert resp.status_code == 200, (
        f"expected 200 from actor endpoint, got {resp.status_code}"
    )
    actor = resp.json()

    # TS は assertionMethod を出さない (= 戻し後 actor JSON 上から消える)
    ams = actor.get("assertionMethod")
    assert not ams, (
        f"TS-A should not expose assertionMethod after roundtrip; got: {ams!r}"
    )

    # 既存 RSA publicKey は依然存在し、federation の verify path はこちらで動く
    pub = actor.get("publicKey") or {}
    assert "publicKeyPem" in pub, "RSA publicKey must remain after roundtrip"
    assert pub.get("id", "").endswith("#main-key"), (
        f"primary key id should still be #main-key (got: {pub.get('id')!r})"
    )


def test_roundtrip_alice_can_post_and_bob_receives(
    instance_a: MisskeyLikeClient,
    instance_b: MisskeyLikeClient,
    alice: dict,
    bob: dict,
) -> None:
    """TS-A 戻し後の alice が投稿し、TS-B (= bob) 側に federation 経由で届く
    ことを確認する。Ed25519 鍵を失った (= 出さなくなった) 状態で RSA fallback
    で deliver / verify が継続する evidence。

    検証手法は stage 6 の `test_post_swap_alice_can_reply` と同じく
    「bob の note への reply を作り、bob の通知で受信を確認する」形にする。
    setup が張る follow は alice → bob の片方向だけなので、alice の public
    note には B 側の配送先が存在せず、bob の home timeline にも (bob は誰も
    follow していないので) 現れない。reply なら reply 先の作者 = bob の
    inbox へ確実に配送され、通知として観測できる。
    """
    tl = instance_a._api("notes/timeline", {"limit": 40})
    baseline = next((n for n in tl if n.get("text") == BASELINE_NOTE_TEXT), None)
    assert baseline is not None, "baseline note missing — cannot reply"

    reply_text = f"dropin-roundtrip-reply-{int(time.time())}"
    instance_a._api("notes/create", {"text": reply_text, "replyId": baseline["id"]})

    def _arrived() -> bool:
        notifications = instance_b.get_notifications(limit=20)
        return any(
            (n.get("note") or {}).get("text") == reply_text for n in notifications
        )

    assert poll_until(
        _arrived, timeout=120, desc="bob receives post-roundtrip reply from TS-A alice"
    )


def test_roundtrip_bob_can_react_to_alice_note(
    instance_a: MisskeyLikeClient,
    instance_b: MisskeyLikeClient,
    alice: dict,
    bob: dict,
) -> None:
    """TS-B (= bob) から TS-A 戻し後の alice の note にリアクションを付け、
    alice 側 (TS-A) に federation 経由で届くことを確認する。逆方向
    (TS-B → TS-A) の deliver も RSA で動く evidence。

    本 test は前段 test に依存せず独立に動くよう、自身で reaction target を
    作る。前段と同じ理由で public note ではなく bob の note への reply を
    使い、bob 側では通知から note id を取得する。
    """
    tl = instance_a._api("notes/timeline", {"limit": 40})
    baseline = next((n for n in tl if n.get("text") == BASELINE_NOTE_TEXT), None)
    assert baseline is not None, "baseline note missing — cannot reply"

    target_text = f"dropin-roundtrip-react-target-{int(time.time())}"
    instance_a._api("notes/create", {"text": target_text, "replyId": baseline["id"]})

    def _arrived_on_b() -> bool:
        notifications = instance_b.get_notifications(limit=20)
        return any(
            (n.get("note") or {}).get("text") == target_text for n in notifications
        )

    assert poll_until(
        _arrived_on_b, timeout=120, desc="bob receives roundtrip reaction-target reply"
    )

    notifications = instance_b.get_notifications(limit=20)
    target = next(
        (n.get("note") or {})
        for n in notifications
        if (n.get("note") or {}).get("text") == target_text
    )

    try:
        instance_b.react(target["id"], "\U0001F389")
    except RuntimeError as e:
        if "ALREADY" not in str(e).upper():
            raise

    def _alice_sees_reaction() -> bool:
        notifications = instance_a.get_notifications(limit=20)
        for n in notifications:
            if n.get("type") != "reaction":
                continue
            from_user = n.get("user") or {}
            # bob は instance A から見ると remote (host = "b") として届く
            if from_user.get("username") == "bob" and from_user.get("host") != A_DOMAIN:
                return True
        return False

    assert poll_until(
        _alice_sees_reaction, timeout=120, desc="alice (TS-A) receives bob reaction after roundtrip"
    )
