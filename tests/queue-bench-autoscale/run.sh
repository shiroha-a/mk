#!/usr/bin/env bash
# Auto-scale comparison bench orchestrator (#1126 / #1120 tracker).
#
# Iterates over 3 scenarios (fixed16 / fixed64 / auto) on the same
# single mkq stack. Each scenario:
#   1. compose down -v       (fresh DB / Redis state)
#   2. swap config mount     (sed in docker-compose.override)
#   3. compose up -d         (bring stack up with new config)
#   4. wait for app healthy  (compose healthcheck does the heavy lifting)
#   5. compose run driver    (seed + burst + drain + measure)
#   6. capture results       (driver writes /results/<scenario>.json)
#
# Final step: python report.py reads all 3 JSONs and writes
# results/report.md as a markdown comparison.

set -euo pipefail

cd "$(dirname "$0")"

SCENARIOS=("fixed16" "fixed64" "auto")
OUTBOUND_NOTES="${OUTBOUND_NOTES:-10}"
FOLLOWERS="${FOLLOWERS:-50}"
DRAIN_TIMEOUT_S="${DRAIN_TIMEOUT_S:-240}"

mkdir -p results

run_scenario() {
    local scenario="$1"
    echo "============================================================"
    echo "[run.sh] === SCENARIO: $scenario ==="
    echo "============================================================"

    # 完全クリーンスタート (volume 含む) → 前 scenario の state が leak しない
    docker compose down -v --remove-orphans

    # config を override で差し替え (configs/<scenario>.yml → /app/.config/default.yml)
    cat > docker-compose.override.yml <<EOF
services:
  app:
    volumes:
      - certs:/certs:ro
      - ./configs/${scenario}.yml:/app/.config/default.yml:ro
EOF

    # stack 起動 + healthy 待ち。--wait で healthcheck pass を待ち、migrations
    # 完了後 (= meta テーブル作成済) に次の psql に進む。
    docker compose up -d --build --wait app

    # federation='all' を UPDATE してから app を restart して meta cache を
    # 再 load させる (mk-go fresh instance default 'none' は outbound deliver
    # を suppress するため、bench の信頼性のため必須)。
    docker compose exec -T postgres \
        psql -U misskey -d misskey -c "UPDATE meta SET federation='all'" >/dev/null
    docker compose restart app
    docker compose up -d --wait app

    # restart 後の healthy を待ってから残 service を起動
    docker compose up -d nginx blackhole

    # driver 実行 (seeding + burst + drain + measure)
    # --build で毎回 driver image を rebuild する (driver dep / script 変更が
    # 即反映されるよう)。base layer cache は効くので 2-3s overhead のみ。
    SCENARIO="$scenario" \
    OUTBOUND_NOTES="$OUTBOUND_NOTES" \
    FOLLOWERS="$FOLLOWERS" \
    DRAIN_TIMEOUT_S="$DRAIN_TIMEOUT_S" \
        docker compose --profile bench run --rm --build driver

    echo "[run.sh] scenario $scenario done"
}

for s in "${SCENARIOS[@]}"; do
    run_scenario "$s"
done

# cleanup + report
docker compose down -v --remove-orphans
rm -f docker-compose.override.yml

echo "============================================================"
echo "[run.sh] generating report"
echo "============================================================"
python3 report.py
echo "[run.sh] done; see tests/queue-bench-autoscale/results/report.md"
