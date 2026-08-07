"""mk-go 独自機能が upstream 共有テーブルに残すデータを作る (#2372)。

`run-swap-test.sh` の stage 6b で、**mk-A に切り替わっている間に**実行する。
ここで作った行は TS-A に戻したあともそのまま DB に残るので、
`test_swap_roundtrip_verify.py` が「TS がそれで壊れないこと」を検証する。

## なぜ要るか

「独自機能は戻したら失われる、それでよい」という整理は**半分しか正しくない**。
機能は失われるが、**機能が書いたデータは残る**。

chat と reversi は upstream にも存在する機能で、テーブルも upstream のもの。
連合部分だけが mk-go 側の追加 (cherrypick 由来) なので、戻したあとの TS には
**upstream が想定していないリモートユーザー由来の行**が残る。TS の実装は
ローカル前提で書かれている可能性があり、そこを踏むと 500 になる。

つまり検証すべきは「機能が使えるか」ではなく「**残留データが TS をクラッシュ
させないか**」。

## ここで作るもの

- リモート (instance B) 相手の chat メッセージ
- リモート相手の reversi 招待

いずれも B 側は TS で連合に対応しないため成立はしない。**成立させることが
目的ではなく、A 側の DB にリモート参照を含む行を残すことが目的**。したがって
4xx は許容し、5xx (= mk-go 側が落ちた) だけを失格とする。
"""

from __future__ import annotations

import pytest
from conftest import B_DOMAIN  # type: ignore[import-not-found]
from conftest_base import MisskeyLikeClient  # type: ignore[import-not-found]


@pytest.fixture(scope="module")
def remote_bob_id(instance_a: MisskeyLikeClient, alice: dict) -> str:
    """mk-A から見たリモート bob の local id。

    session fixture の token を使い回す。テストごとに signin し直すと
    mk-go の signin rate limit に当たって 429 になる。
    """
    # setup stage で alice が bob をフォロー済みなので、A 側に既にリモート
    # ユーザー行がある。ap/show で引き直すより確実 (URI 形式に依存しない)。
    bob = instance_a.users_show("bob", host=B_DOMAIN)
    return bob["id"]


def test_seed_remote_chat_message(
    instance_a: MisskeyLikeClient, alice: dict, remote_bob_id: str
) -> None:
    """mk-A の alice からリモート (B) の bob へ chat を送る。

    B は TS なので chat の連合に対応しない。**送信が成立しなくてよい**。
    A 側の `chat_message` にリモート宛ての行が残ればそれで目的を果たす。
    """
    resp = instance_a.http.post(
        "/api/chat/messages/create-to-user",
        json={"i": instance_a.token, "toUserId": remote_bob_id, "text": "roundtrip probe"},
    )
    assert resp.status_code < 500, (
        f"mk-go が 5xx を返した: {resp.status_code} {resp.text[:300]}"
    )


def test_seed_remote_reversi_invitation(
    instance_a: MisskeyLikeClient, alice: dict, remote_bob_id: str
) -> None:
    """mk-A の alice からリモート (B) の bob へ reversi 招待を出す。

    mk-go は招待を `reversi_game` 行として持つ設計なので、B が応じなくても
    A 側にリモート参照を含む行が残る。
    """
    resp = instance_a.http.post(
        "/api/reversi/match", json={"i": instance_a.token, "userId": remote_bob_id}
    )
    assert resp.status_code < 500, (
        f"mk-go が 5xx を返した: {resp.status_code} {resp.text[:300]}"
    )
