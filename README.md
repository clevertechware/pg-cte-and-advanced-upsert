# pg-cte-and-advanced-upsert

Dépôt d'exemples commun à deux articles de blog sur les requêtes avancées de PostgreSQL 18.

| Dossier | Article | Contenu |
|---|---|---|
| `sql/` | « Une requête, plusieurs écritures dans PostgreSQL » | Script `demo.sql` : `WITH`, `UPDATE … FROM`, `RETURNING`, CTE modifiantes, index unique partiel. `./run.sh` lance un conteneur `postgres:18` et rejoue le script. |
| `go/` | « Fusionner deux jobs au lieu d'en perdre un » | Remise en file d'un job qui fusionne ses identifiants dans le job `PENDING` du même catalogue, en une seule requête. Tests d'intégration avec testcontainers. |

Prérequis : Docker. Go ≥ 1.25 pour `go/`.

```bash
cd sql && ./run.sh
cd go && go test ./...
```
