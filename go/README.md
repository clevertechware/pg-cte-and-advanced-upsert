# Remise en file d'un job avec fusion (PostgreSQL 18, Go)

Exemple de l'article « Fusionner deux jobs au lieu d'en perdre un ».

Domaine : un fournisseur publie des mises à jour de tarif. Chaque évènement met en file un job `PRICE_REFRESH` pour le
catalogue concerné, avec la liste des produits touchés. Un job déjà `PENDING` sur le même catalogue absorbe les nouveaux
produits.

## Prérequis

- Go 1.25 ou plus
- Docker (les tests démarrent `postgres:18` avec testcontainers-go)

`NewRepositoryGuardingEmptyArrays` (variante `COALESCE`) est la variante à reprendre en production : `NewRepository` échoue en
23502 quand les deux tableaux de produits sont vides.

## Tests

```bash
gofmt -l .
go vet ./...
go test -count=1 ./...
```

## Contenu

- `schema.sql` : table `jobs` et index unique partiel `jobs_price_refresh_pending_catalog_uq`
- `repository.go` : `EnqueuePriceRefresh`, `Claim`, `RequeuePriceRefresh` (CTE de fusion, puis repli vers `PENDING` sur
  `sql.ErrNoRows`)
- `repository_test.go` : le bug (23505), la fusion, les cas sans fusion, le claim perdu, les tableaux vides (23502 puis
  variante `COALESCE`) et la course résiduelle entre la fusion et le repli
