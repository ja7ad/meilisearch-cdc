#!/usr/bin/env bash
# Resets the full GUI e2e environment to a clean state: service data,
# e2e_* indexes on both Meilisearch instances, source data and Mailpit.
# Stop the service first; start it again afterwards.
set -euo pipefail
cd "$(dirname "$0")/../.."
KEY=e2e-master-key
rm -rf deploy/e2e/.data
for url in http://localhost:17700 http://localhost:17701; do
  for uid in $(curl -sf -H "Authorization: Bearer $KEY" "$url/indexes?limit=1000" | python3 -c 'import json,sys; print(" ".join(i["uid"] for i in json.load(sys.stdin)["results"] if i["uid"].startswith("e2e_")))'); do
    curl -sf -X DELETE -H "Authorization: Bearer $KEY" "$url/indexes/$uid" >/dev/null
  done
done
# Replication slots of earlier runs keep WAL around.
docker compose -f deploy/e2e/docker-compose.yml exec -T postgres psql -U cdc -d shop -qtAc \
  "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE NOT active" >/dev/null
curl -sf -X DELETE http://localhost:18025/api/v1/messages >/dev/null || true
go run ./test/ui/lab seed
echo "reset done"
