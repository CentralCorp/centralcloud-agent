# Fonctionnalités et référence API

## 1. Principes communs

### URL et authentification

L'adresse par défaut est `127.0.0.1:9443`. Toutes les routes, y compris `/metrics` et `/v1/health`, exigent une authentification.

- Production : HTTPS avec certificat client mTLS.
- Développement : `Authorization: Bearer <token>` sur loopback uniquement.

### En-têtes des mutations

Chaque requête `POST` ou `DELETE` doit contenir :

| En-tête | Format | Fonction |
|---|---|---|
| `Idempotency-Key` | UUID en minuscules | Rejouer sans dupliquer l'opération |
| `X-Correlation-ID` | UUID en minuscules | Corréler réponse, erreur et journaux |
| `X-Request-Timestamp` | RFC 3339 UTC | Refuser une requête trop ancienne ou future |

L'écart accepté pour le timestamp vaut `security.timestamp_skew` (`5m` par défaut). Une même clé d'idempotence avec une requête identique renvoie la réponse mémorisée. La réutiliser avec une méthode, un chemin ou un corps différent renvoie `409 conflict`.

Les endpoints avec un corps exigent `Content-Type: application/json`. Les champs JSON inconnus, plusieurs valeurs JSON et les corps dépassant `server.max_request_bytes` sont refusés.

### Opérations asynchrones

La plupart des mutations répondent `202 Accepted` :

```json
{
  "operation_id": "8b32a8ef-d047-4f37-a0f7-a83d13053be3",
  "deployment_id": "123e4567-e89b-42d3-a456-426614174000",
  "status": "queued"
}
```

Il faut ensuite interroger `GET /v1/operations/{operation_id}`. Une seule opération `queued` ou `running` peut exister simultanément pour un même déploiement.

## 2. Liste des endpoints

| Méthode | Endpoint | Fonction | Réponse normale |
|---|---|---|---|
| `GET` | `/v1/health` | Santé agent, Docker, PostgreSQL et SQLite | `200` ou `503` |
| `GET` | `/v1/resources` | Capacité CPU, mémoire, disque et déploiements | `200` |
| `GET` | `/v1/deployments` | Liste des déploiements | `200` |
| `POST` | `/v1/deployments` | Créer ou recréer un déploiement | `202` |
| `GET` | `/v1/deployments/{id}` | Consulter un déploiement | `200` |
| `POST` | `/v1/deployments/{id}/start` | Démarrer un déploiement arrêté | `202` |
| `POST` | `/v1/deployments/{id}/stop` | Arrêter un déploiement actif | `202` |
| `POST` | `/v1/deployments/{id}/restart` | Arrêter puis redémarrer | `202` |
| `POST` | `/v1/deployments/{id}/upgrade` | Changer d'image avec sauvegarde et rollback | `202` |
| `POST` | `/v1/deployments/{id}/admin-reset` | Réinitialiser le compte administrateur | `202` |
| `POST` | `/v1/deployments/{id}/purge-token` | Obtenir un jeton de purge à usage unique | `201` |
| `DELETE` | `/v1/deployments/{id}?mode=soft` | Supprimer le conteneur, conserver PostgreSQL | `202` |
| `DELETE` | `/v1/deployments/{id}?mode=purge` | Supprimer conteneur, base, rôle et état | `202` |
| `GET` | `/v1/deployments/{id}/logs` | Lire les logs paginés et nettoyés | `200` |
| `GET` | `/v1/operations/{id}` | Suivre une opération | `200` |
| `GET` | `/metrics` | Exposer les métriques Prometheus | `200` |

`{id}` désigne l'UUID utilisé lors de la création, sauf pour `/v1/operations/{id}` où il désigne l'UUID de l'opération.

## 3. Santé et capacité

### `GET /v1/health`

Teste Docker, PostgreSQL et SQLite.

```json
{
  "node_id": "123e4567-e89b-42d3-a456-426614174010",
  "node_name": "node-paris-01",
  "agent_version": "1.0.0",
  "status": "ok",
  "version": "1.0.0",
  "docker": "ok",
  "postgres": "ok",
  "database": "ok"
}
```

Le statut global devient `degraded` et le code HTTP `503` si au moins un composant ne répond pas.

`version` est conservé pour les clients existants ; `agent_version` contient la même valeur. `node_id` provient de `node.id` ou de l'identité générée une fois et persistée dans SQLite.

### `GET /v1/resources`

Retourne :

```json
{
  "node_id": "123e4567-e89b-42d3-a456-426614174010",
  "cpu_count": 8,
  "memory_total_bytes": 16777216000,
  "memory_available_bytes": 8589934592,
  "disk_total_bytes": 107374182400,
  "disk_available_bytes": 53687091200,
  "deployment_count": 4,
  "active_deployment_count": 3
}
```

Ces informations participent aussi au contrôle de capacité avant une création.

## 4. Créer un déploiement

### `POST /v1/deployments`

Corps complet :

```json
{
  "deployment_id": "123e4567-e89b-42d3-a456-426614174000",
  "project_id": "123e4567-e89b-42d3-a456-426614174001",
  "hostname": "example.cloud.centralcorp.fr",
  "image": "ghcr.io/centralcorp/centralpanel:1.0.0",
  "environment": {
    "APP_ENV": "production",
    "CENTRALPANEL_MODE": "cloud",
    "CLOUD_PROJECT_ID": "123e4567-e89b-42d3-a456-426614174001"
  },
  "resources": {
    "memory_bytes": 402653184,
    "cpu_limit": 0.5
  },
  "database": {
    "database_name": "panel_abcd_db",
    "username": "panel_abcd_user"
  },
  "healthcheck": {
    "path": "/health",
    "timeout_seconds": 60
  },
  "bootstrap": {
    "admin_name": "Administrateur",
    "admin_email": "admin@example.com",
    "admin_password": "mot-de-passe-fort",
    "internal_secret": "secret-interne-de-32-caracteres-minimum"
  }
}
```

### Validation des champs

| Champ | Règle |
|---|---|
| `deployment_id`, `project_id` | UUID valides, normalisés en minuscules |
| `hostname` | nom DNS valide, égal ou sous-domaine de `traefik.domain_suffix` |
| `image` | dépôt exactement égal à `docker.panel_image_repository`; digest SHA-256 obligatoire si `require_image_digest=true` |
| `environment` | clé présente dans `panel.allowed_environment_keys`, non réservée/non secrète, 128 entrées et 4096 caractères par valeur maximum |
| `resources.memory_bytes` | au moins 64 Mio ; valeur par défaut si `0` |
| `resources.cpu_limit` | strictement positif ; valeur par défaut si `0` |
| `database.database_name` | `[a-z][a-z0-9_]{0,62}` |
| `database.username` | même format, mais différent du nom de base |
| `healthcheck.path` | chemin absolu commençant par `/`, sans CR/LF |
| `healthcheck.timeout_seconds` | 1 à 600 ; `60` si `0` |
| `bootstrap.admin_name` | requis, 255 caractères maximum |
| `bootstrap.admin_email` | doit contenir `@`, 255 caractères maximum |
| `bootstrap.admin_password` | 12 à 4096 caractères |
| `bootstrap.internal_secret` | 32 à 4096 caractères |

Les variables PostgreSQL, `DATABASE_URL` et les variables internes comme `APP_KEY_FILE`, `PANEL_BOOTSTRAP_FILE` ou `PANEL_MANAGED` sont réservées. Les noms à sémantique secrète (`PASSWORD`, `TOKEN`, `SECRET`, `CREDENTIAL`, `KEY`, etc.) sont refusés même s'ils figurent par erreur dans l'allowlist. L'erreur cite uniquement la clé. Les secrets passent exclusivement par les fichiers protégés existants.

### Fonctionnement interne

1. vérification des limites de déploiement et de la mémoire disponible ;
2. génération et chiffrement des secrets ;
3. création du rôle et de la base PostgreSQL ;
4. création de `centralcloud-fe-<id>` et `centralcloud-be-<id>`, vérification de leurs labels puis connexion de Traefik au frontend ;
5. téléchargement de l'image autorisée ;
6. matérialisation temporaire des secrets en fichiers `0400` ;
7. création/vérification du stockage propriétaire puis création du conteneur sur ses deux réseaux dédiés ;
8. exécution de `panel.migration_command` ;
9. suppression du secret de bootstrap ;
10. attente du `HEALTHCHECK` Docker et d'une réponse HTTP `2xx` sur l'adresse backend du panel.

`PGHOST` vaut `postgres.panel_host` lorsqu'il est configuré, sinon la gateway du backend isolé. Le panel ne partage ainsi aucun réseau avec un autre panel tout en atteignant PostgreSQL local.

Le déploiement passe à l'état `active` lorsque toutes les étapes réussissent. Il passe à `failed` avec `failed_step` en cas d'échec.

## 5. Consulter les déploiements

### `GET /v1/deployments`

```json
{
  "deployments": [
    {
      "deployment_id": "123e4567-e89b-42d3-a456-426614174000",
      "project_id": "123e4567-e89b-42d3-a456-426614174001",
      "hostname": "example.cloud.centralcorp.fr",
      "image": "ghcr.io/centralcorp/centralpanel:1.0.0",
      "state": "active",
      "resources": {"memory_bytes": 402653184, "cpu_limit": 0.5},
      "database": {"database_name": "panel_abcd_db", "username": "panel_abcd_user"},
      "healthcheck": {"path": "/health", "timeout_seconds": 60},
      "credentials_ref": "cccred://deployment/123e4567-e89b-42d3-a456-426614174000/postgres",
      "created_at": "2026-07-20T10:00:00Z",
      "updated_at": "2026-07-20T10:00:08Z"
    }
  ]
}
```

### `GET /v1/deployments/{id}`

Retourne le même objet pour un seul déploiement. Les secrets et les variables d'environnement ne sont pas exposés. Un identifiant inconnu renvoie `404`.

États possibles :

```text
pending, creating_database, pulling_image, creating_container,
starting, migrating, healthchecking, active, stopped,
updating, failed, deleting, deleted
```

## 6. Démarrer, arrêter et redémarrer

Ces trois endpoints n'ont pas de corps JSON :

```text
POST /v1/deployments/{id}/start
POST /v1/deployments/{id}/stop
POST /v1/deployments/{id}/restart
```

- `start` exige normalement l'état `stopped`, démarre le conteneur, puis refait le healthcheck.
- `stop` exige l'état `active` et accorde jusqu'à 30 secondes pour l'arrêt.
- `restart` enchaîne `stop` puis `start` ; il part donc d'un déploiement actif.

## 7. Mettre à jour une image

### `POST /v1/deployments/{id}/upgrade`

```json
{
  "image": "ghcr.io/centralcorp/centralpanel:1.1.0"
}
```

Le dépôt doit rester celui autorisé. Si `docker.require_image_digest=true`, l'image doit contenir `@sha256:` suivi de 64 caractères hexadécimaux. Le déploiement doit être `active` ou `stopped` et l'image doit être différente de l'image actuelle.

Déroulement : dump PostgreSQL chiffré, téléchargement de l'image, remplacement du conteneur, migrations, healthcheck, puis retour à l'état initial (`active` ou `stopped`). Si la nouvelle version échoue, l'agent restaure le dump et l'ancienne image. Il conserve au maximum deux dumps pendant sept jours.

## 8. Réinitialiser l'administrateur

### `POST /v1/deployments/{id}/admin-reset`

```json
{
  "admin_email": "new-admin@example.com",
  "admin_password": "nouveau-mot-de-passe"
}
```

L'adresse doit contenir `@` et le mot de passe doit contenir entre 12 et 4096 caractères. Le corps est chiffré dans la file d'opérations, matérialisé temporairement dans `/run/secrets/panel_admin_reset.json`, puis transmis à la commande `panel.admin_reset_command`. Le fichier est supprimé après exécution.

## 9. Supprimer un déploiement

### Suppression soft

```text
DELETE /v1/deployments/{id}?mode=soft
```

Arrête et supprime le conteneur, les secrets matérialisés et les deux réseaux dédiés après déconnexion de Traefik. La base PostgreSQL, le rôle, les secrets chiffrés, le stockage persistant et l'enregistrement SQLite sont conservés. Le même `deployment_id` peut ensuite être recréé sans écraser les fichiers existants.

### Purge définitive

La purge utilise une confirmation en deux étapes.

1. Demander un jeton :

```text
POST /v1/deployments/{id}/purge-token
```

Réponse `201 Created` :

```json
{
  "purge_token": "jeton-aleatoire",
  "expires_at": "2026-07-20T10:05:00Z"
}
```

2. Dans les cinq minutes, envoyer une nouvelle mutation avec une nouvelle clé d'idempotence :

```text
DELETE /v1/deployments/{id}?mode=purge
X-Purge-Token: jeton-aleatoire
```

Le jeton ne fonctionne qu'une fois. La purge supprime le conteneur, les réseaux, les fichiers secrets, la base, le rôle, le stockage persistant, les dumps locaux, les secrets chiffrés et l'enregistrement principal SQLite. Les marqueurs PostgreSQL, labels Docker et marqueurs de répertoire doivent tous correspondre au `deployment_id`.

Les répertoires sont d'abord renommés dans une quarantaine située sous leur racine validée, puis supprimés. La finalisation SQLite et le succès de l'opération sont atomiques : après un crash, une purge partielle est rejouée sans adopter de ressource étrangère.

## 10. Lire les logs

### `GET /v1/deployments/{id}/logs?limit=100&cursor=...`

| Paramètre | Défaut | Règle |
|---|---:|---|
| `limit` | `100` | entier de 1 à 1000 |
| `cursor` | vide | timestamp RFC 3339 Nano encodé en Base64 URL sans padding |

Réponse :

```json
{
  "lines": ["ligne 1", "ligne 2"],
  "next_cursor": "MjAyNi0wNy0yMFQxMDo..."
}
```

Les motifs contenant mots de passe, tokens, secrets, autorisation ou URL de base sont remplacés par `[REDACTED]` avant la réponse.

## 11. Suivre une opération

### `GET /v1/operations/{id}`

```json
{
  "id": "8b32a8ef-d047-4f37-a0f7-a83d13053be3",
  "deployment_id": "123e4567-e89b-42d3-a456-426614174000",
  "type": "create",
  "status": "succeeded",
  "result": {
    "deployment_id": "123e4567-e89b-42d3-a456-426614174000",
    "status": "succeeded"
  },
  "created_at": "2026-07-20T10:00:00Z",
  "updated_at": "2026-07-20T10:00:08Z"
}
```

Types : `create`, `start`, `stop`, `restart`, `upgrade`, `admin_reset`, `delete_soft`, `delete_purge`.

Statuts : `queued`, `running`, `succeeded`, `failed`. En cas d'échec, `error.code` vaut actuellement `operation_failed` et `error.message` contient l'erreur nettoyée.

## 12. Métriques

### `GET /metrics`

La réponse suit le format texte Prometheus. Métriques propres à l'agent :

```text
centralcloud_agent_up
centralcloud_deployments_total
centralcloud_deployments_active
centralcloud_operations_total{type="..."}
centralcloud_operations_failed_total{type="..."}
centralcloud_operation_duration_seconds{type="..."}
centralcloud_docker_health
centralcloud_postgres_health
centralcloud_available_memory_bytes
centralcloud_available_disk_bytes
```

## 13. Erreurs HTTP

Format commun :

```json
{
  "error": {
    "code": "invalid_request",
    "message": "description sans secret",
    "correlation_id": "f03f9a33-bf1f-48f9-ab01-7a21ac887fd6"
  }
}
```

| HTTP | Code courant | Signification |
|---:|---|---|
| `400` | `invalid_headers`, `stale_request`, `invalid_request`, `invalid_mode` | paramètres ou en-têtes invalides |
| `401` | `unauthorized` | authentification refusée |
| `403` | `source_ip_forbidden` | adresse TCP hors de `security.allowed_source_cidrs` |
| `404` | `not_found` | route, déploiement ou opération absent |
| `409` | `conflict`, `capacity_exceeded` | conflit d'état/idempotence ou capacité insuffisante |
| `413` | `request_too_large` | corps supérieur à la limite |
| `415` | `unsupported_media_type` | corps non JSON |
| `429` | `rate_limited` | limite de débit atteinte |
| `500` | `internal_error` | erreur interne masquée au client |
| `503` | réponse de santé | dépendance indisponible |

Toute réponse API JSON inclut `X-Content-Type-Options: nosniff`. L'agent renvoie ou génère également `X-Correlation-ID`.
