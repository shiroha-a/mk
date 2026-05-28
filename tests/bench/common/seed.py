"""Seed benchmark data for mk-go / Misskey performance comparison.

Creates multiple users and notes on the target instance, then writes
tokens and note IDs to /seed/seed-data.json for k6 to consume.
"""

from __future__ import annotations

import json
import os
import sys
import time

import httpx

TARGET_URL = os.environ["TARGET_URL"]
TARGET_NAME = os.environ.get("TARGET_NAME", "target")
NUM_USERS = int(os.environ.get("SEED_USERS", "50"))
NUM_NOTES_PER_USER = int(os.environ.get("SEED_NOTES", "50"))
OUTPUT_DIR = os.environ.get("OUTPUT_DIR", "/seed")
# Number of followers each user gets. The default (NUM_USERS-1) builds a
# complete follow graph so every note-create fans out to all other users'
# home timelines — without this the bench measures notes-create with zero
# followers (fanout no-op), which is not representative of real load (#1379).
FOLLOWERS_PER_USER = int(os.environ.get("SEED_FOLLOWERS", str(NUM_USERS - 1)))


def wait_for_health(url: str, timeout: int = 180) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            resp = httpx.post(f"{url}/api/ping", json={}, timeout=5, verify=False)
            if resp.status_code == 200:
                print(f"{url} is ready")
                return
        except Exception:
            pass
        time.sleep(2)
    raise TimeoutError(f"{url} did not become healthy within {timeout}s")


def api(http: httpx.Client, endpoint: str, body: dict | None = None, token: str | None = None) -> dict:
    payload = dict(body or {})
    if token:
        payload["i"] = token
    resp = http.post(f"/api/{endpoint}", json=payload)
    if resp.status_code >= 400:
        raise RuntimeError(f"POST /api/{endpoint} failed ({resp.status_code}): {resp.text[:300]}")
    return resp.json() if resp.content else {}


def create_admin(http: httpx.Client, username: str, password: str) -> str:
    """Create the first (admin) user, or sign in if already exists."""
    resp = http.post("/api/admin/accounts/create", json={"username": username, "password": password})
    if resp.status_code == 200:
        return resp.json().get("token", "")
    # 既に初期化済み
    if resp.status_code in (400, 403, 409):
        resp2 = http.post("/api/signin-flow", json={"username": username, "password": password})
        resp2.raise_for_status()
        data = resp2.json()
        return data.get("i") or data.get("token") or ""
    resp.raise_for_status()
    return ""


def resolve_user_id(http: httpx.Client, token: str) -> str:
    """Resolve a token's own user ID via the `i` endpoint."""
    try:
        return api(http, "i", {}, token).get("id", "")
    except Exception as exc:
        print(f"WARN: resolve user id failed: {exc}", file=sys.stderr)
        return ""


def seed_following(http: httpx.Client, tokens: list[str], user_ids: list[str]) -> int:
    """Build a follow graph so notes-create actually fans out to followers.

    Each user follows up to FOLLOWERS_PER_USER of the *other* users, so every
    user ends up with followers and every note-create pushes to their home
    timelines. With the default (complete graph) each user has NUM_USERS-1
    followers. Returns the number of follow edges created.
    """
    edges = 0
    for i, token in enumerate(tokens):
        if not token:
            continue
        # i 番目の user が他 user を follow する。complete graph では全員。
        targets = [user_ids[j] for j in range(len(user_ids)) if j != i and user_ids[j]]
        for target_id in targets[:FOLLOWERS_PER_USER]:
            try:
                api(http, "following/create", {"userId": target_id}, token)
                edges += 1
            except Exception as exc:
                # 既に follow 済み等は best-effort で無視。
                print(f"WARN: follow {i}->{target_id} failed: {exc}", file=sys.stderr)
    return edges


def main() -> None:
    wait_for_health(TARGET_URL)

    http = httpx.Client(base_url=TARGET_URL, timeout=20, verify=False)

    # admin作成
    admin_token = create_admin(http, "benchadmin", "bench1234")
    if not admin_token:
        print("ERROR: failed to get admin token", file=sys.stderr)
        sys.exit(1)

    tokens = [admin_token]
    usernames = ["benchadmin"]

    # 追加ユーザー作成
    for i in range(1, NUM_USERS):
        username = f"benchuser{i}"
        try:
            resp = http.post(
                "/api/admin/accounts/create",
                json={"username": username, "password": "bench1234", "i": admin_token},
            )
            if resp.status_code == 200:
                token = resp.json().get("token", "")
                if token:
                    tokens.append(token)
                    usernames.append(username)
                    continue
            # ユーザーが既に存在する場合はsignin
            resp2 = http.post("/api/signin-flow", json={"username": username, "password": "bench1234"})
            if resp2.status_code == 200:
                data = resp2.json()
                token = data.get("i") or data.get("token") or ""
                if token:
                    tokens.append(token)
                    usernames.append(username)
        except Exception as exc:
            print(f"WARN: failed to create {username}: {exc}", file=sys.stderr)

    print(f"Created {len(tokens)} users on {TARGET_NAME}")

    # 各 token の user ID を解決し、follower graph を張る。これがないと
    # notes-create が 0 follower で fan-out しない非代表な計測になる (#1379)。
    user_ids = [resolve_user_id(http, t) for t in tokens]
    edges = seed_following(http, tokens, user_ids)
    print(f"Created {edges} follow edges on {TARGET_NAME} "
          f"(~{FOLLOWERS_PER_USER} followers/user)")

    # ノート投入
    note_ids: list[str] = []
    for idx, token in enumerate(tokens):
        for j in range(NUM_NOTES_PER_USER):
            try:
                result = api(http, "notes/create", {
                    "text": f"Benchmark seed note {idx}-{j} on {TARGET_NAME}",
                    "visibility": "public",
                }, token)
                nid = result.get("createdNote", {}).get("id")
                if nid:
                    note_ids.append(nid)
            except Exception as exc:
                print(f"WARN: note create failed: {exc}", file=sys.stderr)

    print(f"Created {len(note_ids)} notes on {TARGET_NAME}")

    # 結果出力
    os.makedirs(OUTPUT_DIR, exist_ok=True)
    output = {
        "tokens": tokens,
        "noteIds": note_ids,
        "adminToken": admin_token,
        "username": "benchadmin",
        "usernames": usernames,
        "userIds": user_ids,
        "followEdges": edges,
    }
    path = os.path.join(OUTPUT_DIR, "seed-data.json")
    with open(path, "w") as f:
        json.dump(output, f, indent=2)
    print(f"Wrote seed data to {path}")


if __name__ == "__main__":
    main()
