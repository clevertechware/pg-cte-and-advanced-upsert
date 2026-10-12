#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

container="pg18-demo-$$"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT

docker run -d --name "$container" -e POSTGRES_PASSWORD=demo postgres:18 >/dev/null

# Le serveur temporaire de l'init n'écoute pas en TCP : un test sur 127.0.0.1 attend le serveur définitif.
until docker exec "$container" pg_isready -h 127.0.0.1 -U postgres -q 2>/dev/null; do
    sleep 1
done

# Un pseudo-terminal (-t) désactive la mise en tampon de psql : les erreurs apparaissent à leur place dans la sortie.
docker cp demo.sql "$container":/tmp/demo.sql
docker exec -t "$container" psql -U postgres -X -P pager=off -v ON_ERROR_STOP=1 -f /tmp/demo.sql | tr -d '\r'
