#!/bin/bash
# Phase 13-2 (#367) drop-in swap test orchestrator.
#
# 流れ:
#   1. TS-A + TS-B stack を起動 (Phase 13-1 と同じ)
#   2. test_swap_setup.py で alice/bob/follow/baseline note を作る
#   3. TS-A backend (app-a) を停止 — DB-A / Redis-A は走り続ける
#   4. mk-go overlay で app-a を mk-go ビルドに置き換えて起動
#   5. mk-A の healthy 待ち
#   6. test_swap_verify.py で state preserved + 新規 federation 動作を確認
#   7. mk-A backend を停止して TS-A に戻す (#1082 SHOULD shape)
#   8. TS-A 起動 healthy 待ち + nginx-a restart
#   9. test_swap_roundtrip_verify.py で TS 戻し後の連合継続を確認
#  10. cleanup
#
# pytest セッションを跨いで docker compose を切り替える必要があるため、
# orchestration を bash で外側に置く。test runner コンテナ内に docker CLI を
# 入れるとイメージが太るしソケットマウントの security trade-off があるので、
# bash + 複数回 pytest 起動の構成にした。
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

BASE=docker-compose.dropin.yml
OVERLAY=docker-compose.dropin.mk.yml

cleanup() {
  echo "===> cleanup"
  docker compose -f "$BASE" -f "$OVERLAY" down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "===> stage 1: bring up TS-A + TS-B stack"
docker compose -f "$BASE" up -d --build

echo "===> stage 1b: wait for both TS instances healthy"
deadline=$(($(date +%s) + 240))
while :; do
  states=$(docker compose -f "$BASE" ps --format json | python3 -c "
import sys, json
ls=[json.loads(l) for l in sys.stdin if l.strip()]
healthy=[s for s in ls if s.get('Service') in ('app-a','app-b') and s.get('Health')=='healthy']
print(len(healthy))
")
  if [ "$states" = "2" ]; then
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "FAIL: TS instances did not become healthy within 240s"
    docker compose -f "$BASE" ps
    exit 1
  fi
  sleep 3
done

echo "===> stage 2: setup alice / bob / follow / baseline note"
docker compose -f "$BASE" --profile test run --rm test-runner pytest test_swap_setup.py -v

echo "===> stage 3: stop TS-A backend (DB-A / Redis-A keep state)"
docker compose -f "$BASE" stop app-a

echo "===> stage 4: bring up mk-go overlay on instance A"
# --force-recreate で旧 TS-A image の停止 container を確実に破棄し、新規
# mk-A container として起動する。image label 一致時に compose が container
# を reuse して image が更新されない曖昧さを回避 (#1085 review #1)。
docker compose -f "$BASE" -f "$OVERLAY" up -d --build --force-recreate app-a

echo "===> stage 5: wait for mk-A healthy"
deadline=$(($(date +%s) + 180))
while :; do
  state=$(docker compose -f "$BASE" -f "$OVERLAY" ps --format json | python3 -c "
import sys, json
ls=[json.loads(l) for l in sys.stdin if l.strip()]
ms=[s for s in ls if s.get('Service')=='app-a']
print(ms[0].get('Health') if ms else 'missing')
")
  if [ "$state" = "healthy" ]; then
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "FAIL: mk-A did not become healthy within 180s"
    docker compose -f "$BASE" -f "$OVERLAY" logs app-a | tail -50
    exit 1
  fi
  sleep 3
done

echo "===> stage 5b: restart nginx-a so the upstream re-resolves to mk-A"
# stop app-a 中に nginx-a が backend を unreachable と覚え込む可能性があるので
# 念のため restart する。
docker compose -f "$BASE" -f "$OVERLAY" restart nginx-a

echo "===> stage 6: verify state preserved on mk-A"
docker compose -f "$BASE" -f "$OVERLAY" --profile test run --rm test-runner pytest test_swap_verify.py -v

echo "===> stage 7: stop mk-A backend (roundtrip 戻し前準備)"
docker compose -f "$BASE" -f "$OVERLAY" stop app-a

echo "===> stage 8: bring up TS-A backend (overlay 解除で TS に戻す)"
# overlay を指定せず base のみで up することで、app-a が TS-A の image に戻る。
# --force-recreate で停止中の mk-A container を確実に破棄し、新規 TS-A
# container を起動する (#1085 review #1)。
docker compose -f "$BASE" up -d --force-recreate app-a

echo "===> stage 8b: wait for TS-A healthy"
deadline=$(($(date +%s) + 180))
while :; do
  state=$(docker compose -f "$BASE" ps --format json | python3 -c "
import sys, json
ls=[json.loads(l) for l in sys.stdin if l.strip()]
ms=[s for s in ls if s.get('Service')=='app-a']
print(ms[0].get('Health') if ms else 'missing')
")
  if [ "$state" = "healthy" ]; then
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "FAIL: TS-A (roundtrip) did not become healthy within 180s"
    docker compose -f "$BASE" logs app-a | tail -50
    exit 1
  fi
  sleep 3
done

echo "===> stage 8c: restart nginx-a so the upstream re-resolves to TS-A"
docker compose -f "$BASE" restart nginx-a

echo "===> stage 9: verify federation continuity after TS roundtrip (#1082)"
docker compose -f "$BASE" --profile test run --rm test-runner pytest test_swap_roundtrip_verify.py -v

echo "===> all stages PASS"
