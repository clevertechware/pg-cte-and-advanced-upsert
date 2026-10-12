# Une requête, plusieurs écritures dans PostgreSQL : script SQL

Rejoue dans l'ordre les exemples de l'article : `WITH … MATERIALIZED`, `UPDATE … RETURNING old/new`,
`UPDATE … FROM` (avec et sans doublon dans `price_changes`), CTE modifiante `DELETE … RETURNING` → `INSERT`,
snapshot partagé (deux variantes) et index unique partiel `tasks_pending_target_uq` (erreurs `23505`).

## Prérequis

Docker. L'image `postgres:18` est téléchargée au premier lancement. `old` et `new` dans `RETURNING` exigent
PostgreSQL 18.

## Lancer

```bash
./run.sh
```

Le script démarre un conteneur éphémère, exécute `demo.sql` avec `psql` et supprime le conteneur à la sortie.
Les deux erreurs `23505` sont attendues : `demo.sql` désactive `ON_ERROR_STOP` uniquement autour d'elles.

## Sur un serveur existant

```bash
psql -f demo.sql "$DATABASE_URL"
```

La base doit être vide : le script crée `products`, `products_archive`, `price_changes` et `tasks`.
