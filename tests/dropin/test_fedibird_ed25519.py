"""Fedibird-compatible mock との bidirectional Ed25519 verify (#1083).

`run-fedibird-test.sh` 経由で base + mk + fedibird overlay の stack を起動した
後に実行する。検証する 3 経路:

  1. mk-A が fedibird-mock の actor を fetch して assertionMethod の Ed25519
     公開鍵を user_publickey_extra に upsert する (P3 resolver 経路)
  2. fedibird-mock が Ed25519 sign で mk-A の inbox に Follow activity を POST
     → mk-A が verify 成功 (P3 dual lookup 経路 + signature.go の Ed25519
     algorithm 経路)
  3. mk-A の alice が fedibird-mock を follow → P4 outbound capability-gated
     deliver で Ed25519 sign された Follow activity が mock の inbox に届く
     (mock 側で Ed25519 verify 成立)

これにより ed25519 全 phase (P2 expose / P3 parse / P4 outbound capability /
P5 backfill) が実 federation 経路で動くことを担保する。
"""

from __future__ import annotations

import json
import time

import requests
from conftest_base import MisskeyLikeClient, poll_until  # type: ignore[import-not-found]

MOCK_HOST = "fedibird-mock.test"
MOCK_ACTOR = f"https://{MOCK_HOST}/users/mock-alice"
MOCK_INBOX = f"{MOCK_ACTOR}/inbox"


def _mock_get(path: str, **kwargs):
    return requests.get(f"https://{MOCK_HOST}{path}", verify=False, timeout=10, **kwargs)


def _mock_post(path: str, payload: dict):
    return requests.post(
        f"https://{MOCK_HOST}{path}",
        json=payload,
        verify=False,
        timeout=10,
    )


def test_fedibird_mock_actor_exposes_assertion_method() -> None:
    """sanity: mock の actor JSON が期待 shape を返している (test harness の
    self-check)。これが fail するなら mock server / TLS の起動が壊れている。"""
    resp = _mock_get(
        "/users/mock-alice", headers={"Accept": "application/activity+json"}
    )
    assert resp.status_code == 200
    actor = resp.json()
    assert actor.get("id") == MOCK_ACTOR
    ams = actor.get("assertionMethod") or []
    assert ams and ams[0].get("type") == "Multikey"
    assert ams[0].get("publicKeyMultibase", "").startswith("z6Mk")


def test_mk_resolver_upserts_mock_assertion_method(
    instance_a: MisskeyLikeClient,
) -> None:
    """[Phase 1] mk-A が mock actor を解決すると user_publickey_extra に
    Ed25519 行が upsert される (= P3 cacheAssertionMethods 経路)。

    検証方法:
        mk-A の `/api/ap/show` 経由で mock actor URI を resolve → 内部で
        Resolver.ResolveActor + cacheAssertionMethods が走る。直接 DB を見る
        手段は test runner に無いので、続く Phase 2 (mock → mk-A inbox)
        で Ed25519 verify が通ることをもって upsert を間接的に確認する。
    """
    res = instance_a._api("ap/show", {"uri": MOCK_ACTOR})
    assert res.get("type") == "User"
    user = res.get("object") or {}
    # remote user として認識されている (host が mock の domain)
    assert user.get("host") == MOCK_HOST, f"mock host mismatch: {user!r}"


def test_mock_to_mk_inbox_ed25519_verified(
    instance_a: MisskeyLikeClient, alice: dict
) -> None:
    """[Phase 2] mock が Ed25519 sign で mk-A の alice inbox に Follow を
    POST → mk-A が dual lookup + Ed25519 verify で 202 を返し、
    follow が成立する。

    mk-A 側の verify 経路: signature.go の algorithm whitelist で `ed25519`
    が許容され、user_publickey_extra から keyId 一致の Ed25519 公開鍵を取得
    → ed25519.Verify で OK。
    """
    alice_inbox = f"https://a/users/{alice['id']}/inbox"

    follow_activity = {
        "@context": "https://www.w3.org/ns/activitystreams",
        "id": f"{MOCK_ACTOR}/follows/{int(time.time())}",
        "type": "Follow",
        "actor": MOCK_ACTOR,
        "object": f"https://a/users/{alice['id']}",
    }
    res = _mock_post(
        "/_test/deliver",
        {
            "target": alice_inbox,
            "activity": follow_activity,
            "algorithm": "ed25519",
        },
    )
    assert res.status_code == 200, f"mock deliver helper failed: {res.text}"
    body = res.json()
    assert body.get("status") in (200, 202), (
        f"mk-A did not accept Ed25519-signed Follow: {body!r}"
    )

    # alice 側の followers list で mock-alice が見えるまで待つ
    def _has_follower() -> bool:
        try:
            followers = instance_a._api(
                "users/followers",
                {"userId": alice["id"], "limit": 20},
            )
        except RuntimeError:
            return False
        for f in followers:
            follower = f.get("follower") or {}
            if follower.get("username") == "mock-alice":
                return True
        return False

    assert poll_until(_has_follower, timeout=60, desc="alice gets mock-alice follower"), (
        "mk-A did not register the follower after Ed25519 inbox POST"
    )


def test_mk_outbound_to_mock_uses_ed25519_when_capable(
    instance_a: MisskeyLikeClient, alice: dict
) -> None:
    """[Phase 3] mk-A の alice が mock-alice を follow → P4 capability-gated
    outbound deliver path で Ed25519 sign された Follow activity が mock の
    inbox に届く (mock 側で Ed25519 verify 成立)。

    検証方法:
        1. alice 側で users/show?username=mock-alice&host=fedibird-mock.test
           で remote actor を resolve (= user_publickey_extra に既に Phase 2
           で upsert 済)
        2. alice.follow(mock_user)
        3. mock の /_test/inbox-log を poll して受信 activity に Follow が
           あり、algorithm=ed25519 で記録されていることを確認
    """
    mock_user = instance_a._api(
        "users/show", {"username": "mock-alice", "host": MOCK_HOST}
    )
    assert mock_user and mock_user.get("id"), f"could not resolve mock-alice: {mock_user!r}"

    # follow 実行
    instance_a._api("following/create", {"userId": mock_user["id"]})

    def _follow_arrived_with_ed25519() -> bool:
        resp = _mock_get("/_test/inbox-log")
        if resp.status_code != 200:
            return False
        for entry in resp.json():
            algo = entry.get("algorithm", "")
            activity = entry.get("activity") or {}
            if activity.get("type") == "Follow" and algo.startswith("ed25519"):
                return True
        return False

    assert poll_until(
        _follow_arrived_with_ed25519,
        timeout=60,
        desc="mock receives Ed25519-signed Follow from mk-A",
    ), "mk-A did not sign the outbound Follow with Ed25519"
