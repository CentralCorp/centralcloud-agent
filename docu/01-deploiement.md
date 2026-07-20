# Déploiement de CentralCloud Node Agent

## 1. Architecture de production attendue

Le mode de déploiement de référence est un binaire Linux exécuté par `systemd` sous l'utilisateur dédié `centralcloud-agent`. L'agent communique localement avec :

- Docker via `/var/run/docker.sock` ;
- PostgreSQL via le compte provisionneur configuré ;
- SQLite via `/var/lib/centralcloud-agent/state.db` ;
- le répertoire d'exécution `/run/centralcloud-agent` pour les secrets matérialisés ;
- `/var/lib/centralcloud-agent/backups` pour les dumps chiffrés ;
- `/var/lib/centralcloud-agent/panels` pour le stockage persistant des panels.

En production, l'API doit fonctionner en mTLS avec TLS 1.3. Le mode token est réservé au développement et le programme refuse de l'écouter sur une adresse non loopback.

## 2. Prérequis

- un hôte Linux avec `systemd` ;
- Docker installé, démarré et un groupe système `docker` existant ;
- PostgreSQL accessible depuis l'hôte ;
- un rôle PostgreSQL provisionneur capable de créer/supprimer des rôles et bases ;
- Traefik configuré avec le provider Docker et l'entrypoint attendu ;
- un enregistrement DNS couvrant les hôtes sous `traefik.domain_suffix` ;
- une PKI fournissant le certificat serveur, sa clé privée et la CA des clients ;
- l'image CentralPanel disponible dans le dépôt autorisé ;
- Go 1.26.x ou Docker pour construire le binaire.

L'image CentralPanel v2 doit écouter en HTTP sur le port interne `8080`, définir son `HEALTHCHECK` sur `http://127.0.0.1:8080/up`, fonctionner sous `10001:10001` avec un rootfs en lecture seule, accepter les variables PostgreSQL et les secrets sous forme de fichiers, et supporter les commandes configurées dans `panel.install_command`, `panel.migration_command` et `panel.admin_reset_command`. Son unique montage persistant est `/app/storage`; `/tmp` et `/run` restent temporaires.

## 3. Construire l'agent

Depuis la racine du dépôt :

```sh
make fmt-check vet test test-race build-all
```

Les artefacts statiques sont générés dans :

```text
dist/centralcloud-agent-linux-amd64
dist/centralcloud-agent-linux-arm64
```

Pour construire une image OCI :

```sh
make docker-build VERSION=1.0.0
```

L'image finale est basée sur `distroless`, fonctionne avec l'UID/GID `10001:10001` et lance :

```text
/usr/local/bin/centralcloud-agent -config /etc/centralcloud-agent/config.yaml
```

## 4. Installer les fichiers système

Choisir le binaire correspondant à l'architecture, puis lancer l'installateur depuis la racine du dépôt :

```sh
sudo deploy/install.sh dist/centralcloud-agent-linux-amd64
```

L'installateur :

- crée l'utilisateur système `centralcloud-agent` avec l'UID `10001` si nécessaire ;
- l'ajoute au groupe `docker` ;
- installe le binaire dans `/usr/local/bin/centralcloud-agent` ;
- crée les répertoires sous `/etc`, `/var/lib` et `/run` ;
- installe l'unité `systemd` ;
- copie l'exemple de configuration s'il n'existe pas encore.

Il ne démarre pas automatiquement le service, car les certificats et secrets doivent d'abord être installés.

## 5. Créer les secrets

```sh
sudo install -d -m 0750 -o root -g centralcloud-agent \
  /etc/centralcloud-agent/secrets \
  /etc/centralcloud-agent/tls

openssl rand -base64 32 | sudo tee /etc/centralcloud-agent/secrets/master.key >/dev/null
openssl rand -base64 48 | sudo tee /etc/centralcloud-agent/secrets/postgres_password >/dev/null

sudo chown root:centralcloud-agent /etc/centralcloud-agent/secrets/*
sudo chmod 0640 /etc/centralcloud-agent/secrets/*
```

Après décodage Base64, `master.key` doit contenir exactement 32 octets. Cette clé protège les secrets stockés dans SQLite et les sauvegardes ; elle doit être sauvegardée dans un coffre séparé. Sa perte rend ces données chiffrées inutilisables.

Si le registre Docker est privé, créer aussi :

```text
/etc/centralcloud-agent/secrets/registry_username
/etc/centralcloud-agent/secrets/registry_token
```

Les deux chemins doivent être configurés ensemble. En développement seulement, créer `api_token` avec au moins 32 caractères.

## 6. Installer les certificats mTLS

```sh
sudo install -m 0640 -o root -g centralcloud-agent server.crt \
  /etc/centralcloud-agent/tls/server.crt
sudo install -m 0640 -o root -g centralcloud-agent server.key \
  /etc/centralcloud-agent/tls/server.key
sudo install -m 0640 -o root -g centralcloud-agent client-ca.crt \
  /etc/centralcloud-agent/tls/client-ca.crt
```

Le certificat présenté par le Control Plane doit :

1. être signé par `client-ca.crt` ;
2. contenir un DNS SAN ou URI SAN figurant exactement dans `security.allowed_client_sans`.

## 7. Configurer l'agent

Le fichier principal est `/etc/centralcloud-agent/config.yaml`. Une base complète est disponible dans `deploy/examples/config.yaml`.

### Paramètres essentiels

| Section | Paramètre | Rôle |
|---|---|---|
| `node` | `id`, `name` | Identité stable publiée au Control Plane ; l'ID est généré et persisté dans SQLite s'il est omis |
| `server` | `address` | Adresse d'écoute, `127.0.0.1:9443` par défaut |
| `server` | `operation_timeout` | Durée maximale d'une opération asynchrone |
| `server` | `max_request_bytes` | Taille maximale d'un corps JSON |
| `server` | `rate_per_second`, `rate_burst` | Limitation de débit par identité cliente |
| `security` | `mode` | `mtls` en production, `token` en développement |
| `security` | `master_key_file` | Clé AES de 32 octets |
| `security` | `allowed_client_sans` | Allowlist des identités clientes mTLS |
| `security` | `allowed_source_cidrs` | Défense IP IPv4/IPv6 basée exclusivement sur la connexion TCP |
| `docker` | `socket` | Socket Unix Docker, obligatoirement préfixé par `unix://` |
| `docker` | `panel_image_repository` | Seul dépôt d'images autorisé |
| `docker` | `require_image_digest` | Exige `@sha256:<64 hex>` pour create et upgrade |
| `postgres` | `host`, `port` | Adresse PostgreSQL |
| `postgres` | `panel_host` | Adresse PostgreSQL vue des conteneurs ; vide utilise la gateway backend dédiée |
| `postgres` | `administrator_*` | Connexion du provisionneur ; le mot de passe vient d'un fichier |
| `traefik` | `domain_suffix` | Suffixe DNS autorisé pour les panels |
| `traefik` | `container_name` | Nom exact du conteneur Traefik connecté aux frontends dédiés |
| `limits` | `maximum_deployments` | Capacité logique maximale du nœud |
| `limits` | `maximum_concurrent_operations` | Nombre de workers asynchrones |
| `panel` | `install_command` | Installation initiale exécutée sans shell après disponibilité de `/up` |
| `panel` | `migration_command` | Migrations exécutées sans shell après remplacement d'image |
| `panel` | `allowed_environment_keys` | Seules variables personnalisées non secrètes acceptées par l'API |
| `storage` | `database_file` | État SQLite persistant |
| `storage` | `panel_directory` | Stockage persistant monté dans `/app/storage` |

Les variables suivantes peuvent surcharger certains champs :

```text
CENTRALCLOUD_SERVER_ADDRESS
CENTRALCLOUD_NODE_ID
CENTRALCLOUD_NODE_NAME
CENTRALCLOUD_SECURITY_MODE
CENTRALCLOUD_SECURITY_CERTIFICATE_FILE
CENTRALCLOUD_SECURITY_PRIVATE_KEY_FILE
CENTRALCLOUD_SECURITY_CLIENT_CA_FILE
CENTRALCLOUD_SECURITY_TOKEN_FILE
CENTRALCLOUD_SECURITY_MASTER_KEY_FILE
CENTRALCLOUD_DOCKER_SOCKET
CENTRALCLOUD_DOCKER_REGISTRY_USERNAME_FILE
CENTRALCLOUD_DOCKER_REGISTRY_TOKEN_FILE
CENTRALCLOUD_POSTGRES_PASSWORD_FILE
CENTRALCLOUD_POSTGRES_PANEL_HOST
CENTRALCLOUD_STORAGE_DATABASE_FILE
CENTRALCLOUD_STORAGE_PANEL_DIRECTORY
CENTRALCLOUD_LIMITS_MAXIMUM_DEPLOYMENTS
```

Les valeurs secrètes elles-mêmes ne doivent pas être placées dans ces variables : seules leurs localisations sont configurées.

Les anciens champs `docker.frontend_network` et `docker.egress_network` restent acceptés par le chargeur pour faciliter les mises à jour, mais ne pilotent plus les panels. Les noms dédiés sont calculés à partir du `deployment_id`.

### Exemple de production complet

```yaml
node:
  id: "123e4567-e89b-42d3-a456-426614174010"
  name: "node-paris-01"
server:
  address: "10.0.0.10:9443"
  read_timeout: 30s
  write_timeout: 30s
  idle_timeout: 60s
  operation_timeout: 10m
  max_request_bytes: 1048576
  rate_per_second: 10
  rate_burst: 20
security:
  mode: "mtls"
  certificate_file: "/etc/centralcloud-agent/tls/server.crt"
  private_key_file: "/etc/centralcloud-agent/tls/server.key"
  client_ca_file: "/etc/centralcloud-agent/tls/client-ca.crt"
  master_key_file: "/etc/centralcloud-agent/secrets/master.key"
  allowed_client_sans: ["spiffe://centralcloud/control-plane"]
  allowed_source_cidrs: ["203.0.113.42/32", "2001:db8::/64"]
  timestamp_skew: 5m
docker:
  socket: "unix:///var/run/docker.sock"
  panel_image_repository: "ghcr.io/centralcorp-cloud/centralpanel-cloud"
  require_image_digest: true
  panel_user: "10001:10001"
  pids_limit: 256
  registry_username_file: "/etc/centralcloud-agent/secrets/registry_username"
  registry_token_file: "/etc/centralcloud-agent/secrets/registry_token"
postgres:
  host: "127.0.0.1"
  port: 5432
  administrator_database: "postgres"
  administrator_username: "centralcloud_provisioner"
  administrator_password_file: "/etc/centralcloud-agent/secrets/postgres_password"
  backup_image: "postgres:17-alpine"
  panel_host: ""
traefik:
  container_name: "centralcloud-traefik"
  domain_suffix: "cloud.centralcorp.fr"
  entrypoint: "websecure"
  certificate_resolver: "letsencrypt"
limits:
  maximum_deployments: 50
  default_memory_bytes: 402653184
  default_cpu_limit: 0.5
  maximum_concurrent_operations: 4
panel:
  allowed_environment_keys: []
  install_command: ["php", "artisan", "auto:install", "--bootstrap-file=/run/secrets/panel_bootstrap.json", "--no-interaction"]
  migration_command: ["php", "artisan", "migrate", "--force", "--no-interaction"]
  admin_reset_command: ["php", "artisan", "panel:admin-reset", "--bootstrap-file=/run/secrets/panel_admin_reset.json", "--no-interaction"]
storage:
  database_file: "/var/lib/centralcloud-agent/state.db"
  runtime_directory: "/run/centralcloud-agent"
  backup_directory: "/var/lib/centralcloud-agent/backups"
  panel_directory: "/var/lib/centralcloud-agent/panels"
```

Les CIDR ci-dessus sont documentaires : ils doivent être remplacés par les IP de sortie réelles. Si Infomaniak ne fournit pas d'IP de sortie fixe au Control Plane, utiliser un réseau privé, VPN ou tunnel contrôlé plutôt que d'ouvrir largement le port Agent.

L'agent fournit lui-même `APP_ENV=production`, `APP_URL=https://<hostname>`, `CENTRALPANEL_MODE=centralcloud`, `CLOUD_PROJECT_ID=<project_id>` et `PANEL_MANAGED=true`. Ces clés sont réservées et ne doivent pas figurer dans `allowed_environment_keys`. Après démarrage initial, il attend `/up`, exécute `install_command` avec le fichier `/run/secrets/panel_bootstrap.json`, puis vérifie une nouvelle fois la santé. La commande est idempotente côté CentralPanel et ne réinitialise jamais une installation existante. Lors d'un upgrade, `migration_command` applique uniquement les migrations Laravel après le remplacement de l'image.

Avec `require_image_digest: true`, le Control Plane doit envoyer le digest d'une image réellement publiée dans GHCR. L'identifiant d'un build Docker local n'est pas une référence de registre publiable. Le digest composé de `a` dans `deploy/examples/create-deployment.json` est uniquement un gabarit syntaxique à remplacer.

## 8. Préparer Traefik et PostgreSQL

Traefik doit utiliser le provider Docker avec `exposedByDefault=false`. L'agent crée `centralcloud-fe-<deployment_id>` et `centralcloud-be-<deployment_id>`, connecte dynamiquement le conteneur `traefik.container_name` au frontend et configure `traefik.docker.network` sur ce réseau. Aucun port CentralPanel n'est publié. Un DNS wildcard doit couvrir `*.traefik.domain_suffix`.

Le compte PostgreSQL configuré est un compte d'administration technique. Chaque panel reçoit ensuite son propre rôle sans privilèges élevés (`NOSUPERUSER`, `NOCREATEDB`, `NOCREATEROLE`, `NOINHERIT`, `NOREPLICATION`) et sa propre base. PostgreSQL doit écouter sur l'hôte et sur les gateways Docker nécessaires, jamais sur Internet ; `pg_hba.conf` doit limiter les plages Docker et les rôles autorisés. `postgres.panel_host` peut remplacer la gateway découverte si l'infrastructure utilise une adresse dédiée.

## 9. Démarrer et vérifier

```sh
sudo systemctl enable --now centralcloud-agent
sudo systemctl status centralcloud-agent
journalctl -u centralcloud-agent -f
```

Vérifier la version installée :

```sh
/usr/local/bin/centralcloud-agent -version
```

Tester la santé en mTLS :

```sh
curl --fail --silent --show-error \
  --cert control-plane.crt \
  --key control-plane.key \
  --cacert server-ca.crt \
  https://127.0.0.1:9443/v1/health
```

Une réponse `200` indique que Docker, PostgreSQL et SQLite répondent. Une réponse `503` avec `status: "degraded"` précise le composant en erreur.

## 10. Mise à jour du binaire

```sh
sudo systemctl stop centralcloud-agent
sudo install -m 0755 dist/centralcloud-agent-linux-amd64 \
  /usr/local/bin/centralcloud-agent
sudo systemctl start centralcloud-agent
```

La base SQLite passe automatiquement en mode WAL et applique son schéma au démarrage. Toute opération restée `running` est replacée en file `queued`, puis reprise par les workers.

Avant une mise à jour importante, sauvegarder au minimum :

```text
/etc/centralcloud-agent/
/var/lib/centralcloud-agent/state.db*
/var/lib/centralcloud-agent/backups/
/var/lib/centralcloud-agent/panels/
```

## 11. Environnement de développement

Le Compose fourni lance PostgreSQL et Traefik sur loopback :

```sh
mkdir -p secrets
openssl rand -base64 48 > secrets/postgres_password
make compose-up
make test
make compose-down
```

Le fichier `deploy/examples/config.dev.yaml` montre le mode token. Il reste nécessaire de fournir une clé maître, un token d'au moins 32 caractères et les répertoires attendus. Ce Compose est destiné aux tests locaux ; PostgreSQL utilise un `tmpfs` et ses données disparaissent avec l'environnement.

## 12. Diagnostic rapide

| Symptôme | Vérification |
|---|---|
| démarrage refusé en mode token | l'adresse doit être loopback (`127.0.0.1`, `::1` ou `localhost`) |
| `401 unauthorized` | certificat client/CA/SAN ou bearer token |
| santé `degraded` | champs `docker`, `postgres` et `database` de `/v1/health` |
| création refusée | mémoire disponible, capacité maximale, dépôt d'image et suffixe DNS |
| opération bloquée ou échouée | `GET /v1/operations/{id}` et journald avec le `correlation_id` |
| panel non routé | réseau frontend, labels Docker, entrypoint et resolver Traefik |
| healthcheck expiré | `HEALTHCHECK` Docker, écoute sur `8080`, chemin et délai demandés |

## 13. Cycle de vie du stockage

Chaque panel utilise `<storage.panel_directory>/<deployment_id>`, monté dans `/app/storage`. L'agent crée ce répertoire en `0700`, conserve les données déjà présentes et maintient un marqueur `0600` sous `<storage.panel_directory>/.centralcloud-owners/<deployment_id>`. Ce marqueur n'est pas monté dans le conteneur.

- stop, start, restart et upgrade ne modifient pas le stockage ;
- un soft delete supprime conteneur, secrets matérialisés et réseaux, mais conserve stockage, base, rôle et secrets chiffrés ;
- une purge validée par purge-token supprime aussi PostgreSQL, stockage et backups ;
- toute incohérence de chemin, symlink ou marqueur fait échouer la purge avant le `RemoveAll`.

## 14. Migration des anciens réseaux partagés

La migration n'est volontairement pas automatique. Pour chaque panel existant :

1. vérifier une sauvegarde récente ;
2. effectuer `DELETE ...?mode=soft` et attendre le succès ;
3. rejouer `POST /v1/deployments` avec le même `deployment_id` et la spécification complète ;
4. vérifier santé, routage, stockage et PostgreSQL ;
5. retirer les anciens réseaux partagés seulement après migration de tous les panels.

Cette procédure provoque une interruption du panel, mais préserve ses données locales et évite une recréation automatique risquée au démarrage de l'agent.

## 15. Ajouter un node au Dashboard

1. installer Docker, PostgreSQL, Traefik, le binaire et la configuration ;
2. attribuer un UUID et un nom dans `node`, ou laisser l'agent générer l'UUID une fois dans `state.db` ;
3. installer le certificat serveur et autoriser le SAN du certificat client Laravel ;
4. limiter le firewall et `allowed_source_cidrs` aux sorties du Control Plane ;
5. démarrer l'agent puis relever `node_id` avec `/v1/health` ;
6. enregistrer dans MySQL l'URL privée, le `node_id`, le nom et les informations de capacité issues de `/v1/resources` ;
7. activer le polling périodique de ces deux endpoints.

Un `node.id` configuré différent de celui déjà enregistré dans SQLite bloque le démarrage. Cela évite qu'un node existant change silencieusement d'identité.

## 16. Sauvegardes de production

Les dumps chiffrés d'upgrade ne constituent pas une stratégie de reprise après perte du VPS. Sauvegarder extérieurement et tester la restauration de :

- toutes les bases PostgreSQL ;
- `storage.panel_directory` ;
- `state.db`, ses fichiers WAL/SHM et les backups locaux ;
- `/etc/centralcloud-agent` ;
- la clé maître dans un coffre séparé.

R2 ou S3 pourra être ajouté ultérieurement comme destination de backup, mais n'est ni requis ni utilisé comme stockage principal en V1.
