"""mk-A が Ed25519 で連合した相手を作る (#2376)。

`run-swap-test.sh` の stage 6b で、**mk-A に切り替わっている間に**実行する。

## 何を確かめたいか

mk-go は remote actor を解決したとき、RSA (`user_publickey`、upstream テーブル) と
Ed25519 (`user_publickey_extra`、mk-go テーブル) を **排他ではなく並列に**保存する。

```go
r.cachePublicKey(user.ID, actor.PublicKey.ID, actor.PublicKey.PublicKeyPEM)
r.cacheAssertionMethods(user.ID, actor.ID, actor.AssertionMethod)
```

これが正しければ、TS に戻しても `user_publickey` が残っているので **RSA で連合を
継続できる**はず。Ed25519 が使えなくなるのは機能喪失であって破壊ではない。

「はず」で終わらせないために、ここで Ed25519 経由の連合を実際に成立させておく。
戻したあとの RSA 継続は `test_swap_roundtrip_verify.py` が実測する。
"""

from __future__ import annotations

import time

import requests  # type: ignore[import-not-found]
from conftest_base import MisskeyLikeClient, poll_until  # type: ignore[import-not-found]

MOCK_HOST = "fedibird-mock.test"
MOCK_ACTOR = f"https://{MOCK_HOST}/users/mock-alice"


def _mock_post(path: str, payload: dict):
    return requests.post(
        f"https://{MOCK_HOST}{path}", json=payload, verify=False, timeout=10
    )


def test_mkgo_resolves_mock_actor(
    instance_a: MisskeyLikeClient, alice: dict
) -> None:
    """mk-A が mock actor を解決する。

    mock は RSA と Ed25519 の両方を expose しているので、mk-go は両方を保存する
    はず。**保存されていることは後続の 2 テストが挙動で示す** (Ed25519 で受理
    できる / 戻した TS が RSA で受理できる)。DB を直接覗くより、実際に検証が
    通るかで見る方が意味がある。
    """
    # ap/show は {"type": "User", "object": {...}} で包んで返す。
    resolved = instance_a.resolve_ap(MOCK_ACTOR)
    assert resolved.get("type") == "User", f"解決結果が想定外: {resolved}"
    obj = resolved.get("object") or {}
    assert obj.get("host") == MOCK_HOST, f"解決結果が想定外: {resolved}"
    assert obj.get("username") == "mock-alice", f"解決結果が想定外: {resolved}"


def test_mock_follows_alice_with_ed25519(
    instance_a: MisskeyLikeClient, alice: dict
) -> None:
    """mock が **Ed25519 署名**で mk-A の inbox に Follow を送り、受理される。

    これで「mk-go は Ed25519 でこの相手と連合していた」状態を作る。この follow
    関係は TS に戻したあとも DB に残るので、roundtrip 側で RSA 継続を試せる。
    """
    alice_inbox = f"https://a/users/{alice['id']}/inbox"
    res = _mock_post(
        "/_test/deliver",
        {
            "target": alice_inbox,
            "algorithm": "ed25519",
            "activity": {
                "@context": "https://www.w3.org/ns/activitystreams",
                "id": f"{MOCK_ACTOR}/follows/{int(time.time())}",
                "type": "Follow",
                "actor": MOCK_ACTOR,
                "object": f"https://a/users/{alice['id']}",
            },
        },
    )
    assert res.status_code == 200, f"mock deliver helper failed: {res.text[:300]}"
    body = res.json()
    assert body.get("status") in (200, 202), (
        f"mk-A did not accept Ed25519-signed Follow: {body!r}"
    )

    def _has_follower() -> bool:
        try:
            followers = instance_a._api(
                "users/followers", {"userId": alice["id"], "limit": 20}
            )
        except RuntimeError:
            return False
        return any(
            (f.get("follower") or {}).get("username") == "mock-alice" for f in followers
        )

    assert poll_until(_has_follower, timeout=60, desc="mock-alice が follower に載る"), (
        "Ed25519 経由の Follow が mk-A に反映されなかった"
    )
