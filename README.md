# CentralPanel Cloud Node Agent

Agent système Go installé sur chaque VPS CentralCloud. Il reçoit les ordres du Control Plane par une API JSON privée, provisionne PostgreSQL avec `pgx`, pilote Docker exclusivement avec le SDK officiel, configure Traefik et réconcilie l’état réel avec SQLite.

## Garanties de sécurité

- mTLS TLS 1.3 obligatoire en production, avec validation de la CA et allowlist DNS/URI SAN.
- Mode bearer token uniquement pour le développement et uniquement sur une adresse loopback.
- Mutations protégées par `Idempotency-Key`, `X-Correlation-ID` et `X-Request-Timestamp`.
- Secrets PostgreSQL générés par l’agent, chiffrés AES-256-GCM au repos et montés en fichier `0400`; aucun mot de passe n’est renvoyé.
- Conteneurs non privilégiés, sans port hôte ni socket Docker, capacités supprimées, rootfs read-only, tmpfs, limites CPU/RAM/PID et `no-new-privileges`.
- Deux réseaux Docker propriétaires par déploiement ; aucun réseau panel-à-panel partagé, Traefik rejoint uniquement chaque frontend nécessaire.
- Stockage persistant marqué et supprimable uniquement après validation stricte du chemin et de la propriété.
- Logs JSON structurés et nettoyage des mots de passe, tokens, DSN et en-têtes sensibles.

L’accès au socket Docker confère des privilèges équivalents à root. Le compte système dédié doit donc être traité comme un compte privilégié du nœud et l’API ne doit jamais être exposée publiquement.

## Construction

Go 1.26.5 est épinglé. Si Go n’est pas installé, les cibles Make utilisent l’image officielle :

```sh
make fmt-check vet test test-race build-all
make docker-build VERSION=1.0.0
```

Le binaire Linux statique est `dist/centralcloud-agent-linux-<arch>`.

## Configuration et secrets

Copier `deploy/examples/config.yaml` vers `/etc/centralcloud-agent/config.yaml`. Les champs sensibles désignent toujours des fichiers. Créer notamment :

```sh
install -d -m 0750 /etc/centralcloud-agent/secrets
openssl rand -base64 32 > /etc/centralcloud-agent/secrets/master.key
openssl rand -base64 48 > /etc/centralcloud-agent/secrets/postgres_password
chown root:centralcloud-agent /etc/centralcloud-agent/secrets/*
chmod 0640 /etc/centralcloud-agent/secrets/*
```

La clé maître décodée doit faire exactement 32 octets. En mode développement, créer également `api_token` avec au moins 32 caractères. Configurer `node.id`/`node.name`, `traefik.container_name`, l'allowlist `panel.allowed_environment_keys` (vide par défaut) et, en production, `docker.require_image_digest: true`. Si `node.id` est omis, il est généré une seule fois dans SQLite. Les variables documentées `CENTRALCLOUD_*` ne transportent jamais directement un secret.

L’image CentralPanel doit :

- écouter en HTTP sur le port interne 8080 et déclarer un `HEALTHCHECK` Docker;
- fonctionner avec l’utilisateur configuré et un rootfs read-only;
- accepter `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER` et `PGPASSWORD_FILE`;
- exposer `/up` et fournir `auto:install --bootstrap-file`, `migrate --force` ainsi que `panel:admin-reset --bootstrap-file`.

Le dépôt autorisé par défaut est `ghcr.io/centralcorp-cloud/centralpanel-cloud`. L'agent injecte les valeurs managées `APP_ENV`, `APP_URL`, `CENTRALPANEL_MODE`, `CLOUD_PROJECT_ID` et `PANEL_MANAGED`; une requête API ne peut pas les remplacer.

Les panels utilisent `centralcloud-fe-<deployment_id>` pour Traefik et un backend `Internal` distinct pour PostgreSQL. `postgres.host` sert à l'agent ; les panels utilisent `postgres.panel_host` ou, par défaut, la gateway de leur backend. PostgreSQL et son `pg_hba.conf` doivent accepter uniquement les bridges nécessaires et ne jamais être exposés publiquement.

## Installation systemd

Après construction :

```sh
sudo deploy/install.sh dist/centralcloud-agent-linux-amd64
sudo install -m 0640 -o root -g centralcloud-agent server.crt server.key client-ca.crt /etc/centralcloud-agent/tls/
sudo systemctl enable --now centralcloud-agent
journalctl -u centralcloud-agent -f
```

Le service démarre après Docker, utilise `centralcloud-agent` (UID 10001, identique au `panel_user` par défaut afin que le fichier secret `0400` soit lisible dans le conteneur), écrit dans journald et limite ses chemins inscriptibles à `/var/lib/centralcloud-agent` et `/run/centralcloud-agent`.

## API

Toutes les routes, y compris `/metrics`, nécessitent l’authentification. Les mutations répondent `202 Accepted` avec un `operation_id`; suivre ensuite `GET /v1/operations/{id}`.

```text
GET    /v1/health
GET    /v1/resources
GET    /v1/deployments
POST   /v1/deployments
GET    /v1/deployments/{id}
POST   /v1/deployments/{id}/start
POST   /v1/deployments/{id}/stop
POST   /v1/deployments/{id}/restart
POST   /v1/deployments/{id}/upgrade
POST   /v1/deployments/{id}/purge-token
DELETE /v1/deployments/{id}?mode=soft|purge
GET    /v1/deployments/{id}/logs?limit=100&cursor=...
GET    /v1/operations/{id}
GET    /metrics
```

Exemple de création en développement :

```sh
curl -sS http://127.0.0.1:9443/v1/deployments \
  -H "Authorization: Bearer $(cat /etc/centralcloud-agent/secrets/api_token)" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: $(uuidgen)" \
  -H "X-Correlation-ID: $(uuidgen)" \
  -H "X-Request-Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --data @deploy/examples/create-deployment.json
```

Pour une purge, demander d’abord un jeton, puis envoyer `X-Purge-Token` avec `DELETE ...?mode=purge`. Le jeton expire après cinq minutes et n’est utilisable qu’une fois. Une suppression soft conserve base, rôle, secrets chiffrés et stockage ; la purge retire aussi réseaux, PostgreSQL, stockage, backups et état principal SQLite.

Les réponses `/v1/health` et `/v1/resources` contiennent `node_id`; la santé expose aussi `node_name` et `agent_version`. Le Control Plane les sonde périodiquement et reste seul responsable du choix du node.

## Développement et tests isolés

Créer `secrets/postgres_password`, puis :

```sh
make compose-up
make test
make compose-down
```

Le Compose de test publie PostgreSQL et Traefik uniquement sur loopback et utilise un tmpfs pour PostgreSQL. Ne jamais pointer les tests destructifs vers une base ou un daemon de production.

## Reprise et opérations

SQLite fonctionne en WAL et stocke déploiements, opérations, étapes et réponses idempotentes. Au redémarrage, une opération restée `running` est remise en file et chaque étape externe est rejouée idempotemment. Un upgrade crée un dump logique chiffré via un conteneur PostgreSQL utilitaire, conserve deux dumps pendant sept jours et restaure base et ancienne image si le nouveau healthcheck échoue.

Les anciens panels utilisant les réseaux partagés doivent être migrés de façon contrôlée par soft delete puis recreate avec le même `deployment_id`. Cette opération préserve PostgreSQL et `/app/storage`. La V1 reste locale au node : sauvegarder extérieurement PostgreSQL, les panels, `state.db`, la configuration et la clé maître séparée.
