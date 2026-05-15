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

from conftest import A_DOMAIN  # type: ignore[import-not-found]
from conftest_base import MisskeyLikeClient, poll_until  # type: ignore[import-not-found]


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
    """TS-A 戻し後の alice が新規 note を投稿し、TS-B (= bob) 側に federation
    経由で届くことを確認する。Ed25519 鍵を失った (= 出さなくなった) 状態で
    RSA fallback で deliver / verify が継続する evidence。
    """
    note_text = "dropin-roundtrip-after-ts-restore"
    instance_a.create_note(note_text)

    def _arrived() -> bool:
        tl = instance_b._api("notes/timeline", {"limit": 40})
        return any(n.get("text") == note_text for n in tl)

    assert poll_until(
        _arrived, timeout=120, desc="bob receives post-roundtrip note from TS-A alice"
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
    """
    # 先ほどの test で送った roundtrip note を bob 側で探す
    note_text = "dropin-roundtrip-after-ts-restore"
    tl_b = instance_b._api("notes/timeline", {"limit": 40})
    target = next((n for n in tl_b if n.get("text") == note_text), None)
    assert target is not None, "roundtrip note must be visible on B before reacting"

    try:
        instance_b.react(target["id"], "🎉")
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
