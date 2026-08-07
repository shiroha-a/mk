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

import requests  # type: ignore[import-not-found]

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


# ── mk-go 独自機能の残留データに対する耐性 (#2372) ────────────────────
#
# stage 6b (test_swap_seed_mkgo_only.py) が mk-A 上で作った、リモートユーザーを
# 参照する chat / reversi 行が DB に残っている状態で TS-A が動いている。
#
# 「独自機能は戻したら失われる」は半分しか正しくない。機能は失われるが**機能が
# 書いたデータは残る**。chat / reversi は upstream にも存在する機能で、テーブルも
# upstream のもの。連合部分だけが mk-go の追加なので、TS には upstream が想定
# していないリモート参照を含む行が残る。
#
# ここで見るのは「機能が使えるか」ではなく「**残留データが TS をクラッシュさせ
# ないか**」。4xx は仕様上あり得るので許容し、**5xx だけを失格**とする。

def _tolerate(resp, label: str) -> None:
    assert resp.status_code < 500, (
        f"{label}: TS が 5xx を返した ({resp.status_code})。mk-go が残した"
        f"リモート参照行を TS が処理できていない可能性がある。"
        f"\n{resp.text[:400]}"
    )


def test_roundtrip_ts_survives_remote_chat_rows(
    instance_a: MisskeyLikeClient, alice: dict
) -> None:
    """リモート宛て chat 行が残った状態で TS の chat endpoint が 5xx にならない。"""
    for endpoint, body in [
        ("chat/history", {"limit": 10}),
        ("chat/rooms/joining", {"limit": 10}),
    ]:
        resp = instance_a.http.post(
            f"/api/{endpoint}", json={"i": instance_a.token, **body}
        )
        _tolerate(resp, endpoint)


def test_roundtrip_ts_survives_remote_reversi_rows(
    instance_a: MisskeyLikeClient, alice: dict
) -> None:
    """リモート相手の reversi 行が残った状態で TS の reversi endpoint が 5xx にならない。"""
    for endpoint, body in [
        ("reversi/games", {"limit": 10}),
        ("reversi/invitations", {}),
    ]:
        resp = instance_a.http.post(
            f"/api/{endpoint}", json={"i": instance_a.token, **body}
        )
        _tolerate(resp, endpoint)


def test_roundtrip_ts_can_still_pack_alice_timeline(
    instance_a: MisskeyLikeClient, alice: dict
) -> None:
    """mk-go が書いた note を含む timeline を TS が pack できる。

    独自機能の残留データより広い網。mk-go が共有カラムに書いた値を TS が
    解釈できないと、ここで落ちる。
    """
    resp = instance_a.http.post(
        "/api/notes/local-timeline", json={"i": instance_a.token, "limit": 30}
    )
    _tolerate(resp, "notes/local-timeline")
    assert resp.status_code == 200, "timeline は 200 で返るべき"


# ── Ed25519 で連合していた相手との RSA 継続 (#2376) ──────────────────
#
# mk-go は remote actor 解決時に RSA (user_publickey、upstream テーブル) と
# Ed25519 (user_publickey_extra、mk-go テーブル) を**排他ではなく並列に**保存する。
# したがって TS に戻しても RSA が残っており、連合は継続できる **はず**。
#
# 「はず」を実測する。stage 6b で mock は Ed25519 署名で follow を成立させた。
# ここでは同じ mock が **RSA 署名**で送って TS-A が受理するかを見る。
#
# Ed25519 が使えなくなること自体は機能喪失であって破壊ではない。破壊かどうかは
# 「RSA にフォールバックして連合が続くか」で決まる。

MOCK_HOST = "fedibird-mock.test"
MOCK_ACTOR = f"https://{MOCK_HOST}/users/mock-alice"


def test_roundtrip_mock_can_still_deliver_with_rsa(
    instance_a: MisskeyLikeClient, alice: dict
) -> None:
    """TS-A に戻したあと、mock が RSA 署名で送った activity を TS が受理する。

    mk-go が Ed25519 と一緒に RSA も保存していなければ、ここで検証に失敗する。
    """
    res = requests.post(
        f"https://{MOCK_HOST}/_test/deliver",
        json={
            "target": f"https://a/users/{alice['id']}/inbox",
            "algorithm": "rsa-sha256",
            "activity": {
                "@context": "https://www.w3.org/ns/activitystreams",
                "id": f"{MOCK_ACTOR}/likes/{int(time.time())}",
                "type": "Undo",
                "actor": MOCK_ACTOR,
                "object": {
                    "type": "Follow",
                    "actor": MOCK_ACTOR,
                    "object": f"https://a/users/{alice['id']}",
                },
            },
        },
        verify=False,
        timeout=10,
    )
    assert res.status_code == 200, f"mock deliver helper failed: {res.text[:300]}"
    body = res.json()
    assert body.get("status") in (200, 202, 204), (
        "TS-A が RSA 署名の activity を受理しなかった。mk-go が Ed25519 と一緒に "
        f"RSA を保存できていない可能性がある: {body!r}"
    )


def test_roundtrip_ts_can_pack_ed25519_peer(
    instance_a: MisskeyLikeClient, alice: dict
) -> None:
    """Ed25519 で解決したリモートユーザーを TS が pack できる。

    `user_publickey_extra` は TS が知らないテーブルなので無視されるだけだが、
    `user` 行そのものは mk-go が作ったもの。TS がそれを読んで落ちないこと。
    """
    resp = instance_a.http.post(
        "/api/users/show",
        json={"i": instance_a.token, "username": "mock-alice", "host": MOCK_HOST},
    )
    _tolerate(resp, "users/show (Ed25519 で解決した相手)")


# ── 分割アップロードで作った drive file (#2376) ──────────────────────
#
# drive_file は upstream テーブルで TS が読む。分割アップロードは mk-go 独自
# 機能だが、**完成した行が upstream と同じ形になっているか**は別問題。
# storedInternal / accessKey の状態を TS が解釈できないと、戻した瞬間に
# ドライブが壊れる。

def test_roundtrip_ts_can_list_drive_files(
    instance_a: MisskeyLikeClient, alice: dict
) -> None:
    """mk-go が作った drive file を TS が一覧できる。"""
    resp = instance_a.http.post(
        "/api/drive/files", json={"i": instance_a.token, "limit": 30}
    )
    _tolerate(resp, "drive/files")
    assert resp.status_code == 200, "drive/files は 200 で返るべき"

