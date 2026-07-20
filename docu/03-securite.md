# Sécurité de CentralCloud Node Agent

## 1. Modèle de confiance

L'agent est un composant privilégié du nœud. Son accès au socket Docker permet de contrôler le daemon et doit être considéré comme équivalent à un accès root sur le VPS, même si le processus Linux s'exécute sous un utilisateur non-root.

Les frontières de confiance sont :

- le Control Plane autorisé à appeler l'API ;
- l'agent et son compte système `centralcloud-agent` ;
- Docker et les conteneurs gérés ;
- le compte administrateur PostgreSQL de provisionnement ;
- les fichiers de configuration, certificats, clés et état local.

Conséquence : l'API ne doit jamais être exposée publiquement. Le filtrage réseau du VPS doit n'autoriser que le Control Plane, même lorsque mTLS est activé.

Le Control Plane Laravel possède les utilisateurs, paiements et décisions métier dans MySQL. L'agent ne connaît que des identifiants de projet/déploiement et n'accède jamais à ce MySQL. Cette séparation limite l'impact d'une compromission applicative du Dashboard ou d'un panel, sans supprimer le besoin de protéger fortement l'API Agent.

## 2. Authentification de l'API

### Production : mTLS

Le mode `mtls` impose :

- TLS 1.3 minimum ;
- un certificat client valide signé par `security.client_ca_file` ;
- un DNS SAN ou URI SAN exactement présent dans `security.allowed_client_sans` ;
- un certificat et une clé serveur configurés par fichiers.

La CA valide la chaîne cryptographique et l'allowlist SAN limite les certificats clients autorisés à piloter l'agent.

### Développement : bearer token

Le mode `token` :

- lit le token depuis `security.token_file` ;
- exige au moins 32 octets après suppression des espaces extérieurs ;
- compare le token en temps constant ;
- refuse de démarrer si l'adresse d'écoute n'est pas loopback.

Ce mode utilise HTTP dans l'implémentation actuelle. Il ne doit pas être employé sur un réseau partagé ou en production.

Toutes les routes sont authentifiées, y compris `/v1/health` et `/metrics`.

### Filtrage de l'adresse source

Lorsque `security.allowed_source_cidrs` n'est pas vide, l'agent exige que l'IPv4 ou l'IPv6 de `RemoteAddr` appartienne à un préfixe configuré. `X-Forwarded-For` et les autres en-têtes proxy sont ignorés. Une adresse interdite reçoit `403` avant l'authentification. Cette protection complète mTLS et le firewall ; elle ne les remplace pas.

## 3. Protection contre rejeu et doublons

Toutes les mutations exigent :

- `Idempotency-Key`, UUID unique ;
- `X-Correlation-ID`, UUID de traçage ;
- `X-Request-Timestamp`, horodatage RFC 3339 récent.

L'agent calcule un SHA-256 de la méthode, du chemin et du corps. La réponse est associée à la clé d'idempotence dans SQLite :

- même clé et même requête : la réponse initiale est rejouée ;
- même clé et requête différente : `409 conflict` ;
- timestamp hors fenêtre : `400 stale_request`.

Une contrainte SQLite interdit également plusieurs opérations actives simultanées pour le même déploiement.

## 4. Validation des entrées

Les protections principales sont :

- taille maximale du corps configurable, 1 Mio par défaut ;
- `Content-Type: application/json` obligatoire pour les corps ;
- rejet des champs JSON inconnus et des documents contenant plusieurs valeurs ;
- UUID stricts pour les en-têtes de mutation ;
- validation du suffixe DNS autorisé ;
- allowlist du dépôt d'images CentralPanel ;
- validation stricte des identifiants PostgreSQL ;
- allowlist des variables d'environnement, rejet des clés réservées et des noms susceptibles de contenir un secret ;
- parsing exact du dépôt Docker et digest SHA-256 obligatoire lorsque configuré ;
- chemins de healthcheck absolus sans retour à la ligne ;
- commandes de migration et de reset transmises comme tableau d'arguments, sans shell ;
- noms de fichiers secrets réduits à un nom de base, sans traversée de répertoire.

Le serveur applique aussi des timeouts de lecture, écriture et inactivité, une limite de 32 Kio sur les en-têtes et une limitation de débit par identité mTLS ou adresse IP.

## 5. Gestion des secrets

### Au repos

La clé maître doit faire exactement 32 octets, bruts ou encodés en Base64. L'agent utilise AES-256-GCM avec un nonce aléatoire pour chiffrer :

- mot de passe PostgreSQL de chaque panel ;
- clé applicative et secret interne ;
- données de bootstrap initial ;
- demande de réinitialisation administrateur en attente ;
- dumps PostgreSQL créés avant une mise à jour.

Les mots de passe de base ne sont jamais renvoyés par l'API. Seule une référence logique `cccred://...` apparaît dans un déploiement.

### À l'exécution

Les secrets sont écrits sous `/run/centralcloud-agent/deployments/{id}` avec :

- répertoire en mode `0700` ;
- fichiers en mode `0400` ;
- montage en lecture seule dans `/run/secrets` du conteneur ;
- variables `*_FILE` plutôt que valeurs secrètes dans l'environnement.

Le secret de bootstrap et le fichier de reset administrateur sont supprimés après usage. Les identifiants du registre et le mot de passe administrateur PostgreSQL sont eux aussi lus depuis des fichiers configurés.

La clé maître ne doit pas être stockée avec une sauvegarde de `state.db` sans protection supplémentaire. Une compromission simultanée des deux permet de déchiffrer les secrets.

## 6. Isolation des conteneurs

Les conteneurs CentralPanel sont créés avec :

- utilisateur non-root configurable, `10001:10001` par défaut ;
- rootfs en lecture seule ;
- toutes les capacités Linux supprimées ;
- `no-new-privileges` ;
- limites mémoire, CPU et PID ;
- `/tmp` et `/run` en `tmpfs` avec `noexec` et `nosuid` ;
- aucun port hôte publié ;
- aucun montage du socket Docker ;
- secrets montés en lecture seule ;
- stockage persistant limité à `/app/storage` ;
- politique de redémarrage `on-failure` limitée à trois tentatives.

Chaque déploiement reçoit un bridge frontend et un bridge backend distincts. Traefik est connecté uniquement au frontend concerné ; le panel est connecté à son frontend et son backend ; les utilitaires PostgreSQL temporaires utilisent uniquement ce backend. Les réseaux portent `centralcloud.managed`, `centralcloud.deployment_id` et `centralcloud.network_role`. Une ressource homonyme avec des labels étrangers n'est jamais adoptée.

Le backend est `Internal`. PostgreSQL local est atteint via sa gateway Docker ou `postgres.panel_host`. Les panels A et B ne possèdent ainsi aucune interface sur un réseau commun. Traefik reste un composant partagé et privilégié : sa sécurité, ses mises à jour et `exposedByDefault=false` restent indispensables.

Les conteneurs utilitaires de sauvegarde utilisent également un rootfs en lecture seule, aucune capacité, `no-new-privileges`, un `tmpfs` protégé et un fichier `pgpass` temporaire en `0400`.

## 7. Protection PostgreSQL

Chaque déploiement reçoit un rôle dédié créé avec :

```text
NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION
```

L'agent révoque les droits publics sur la base et accorde les droits au seul rôle du panel. Il ajoute le marqueur `centralcloud:<deployment_id>` au rôle et à la base.

Avant une purge, il vérifie ce marqueur et refuse de supprimer une ressource qui n'appartient pas au déploiement. Les identifiants SQL sont validés, puis cités par PostgreSQL via `format('%I', ...)` au lieu d'être concaténés directement.

## 8. Suppressions et mises à jour

Une suppression soft conserve PostgreSQL, les secrets chiffrés et `/app/storage`, mais retire conteneur, secrets matérialisés et réseaux dédiés. Une purge exige un jeton aléatoire :

- seul son SHA-256 est stocké ;
- il expire après cinq minutes ;
- il est lié au déploiement ;
- il n'est consommable qu'une seule fois.

Avant un upgrade, l'agent crée un dump logique PostgreSQL chiffré. Si la migration ou le healthcheck échoue, il restaure le dump et recrée le conteneur avec l'ancienne image. Deux dumps au maximum sont conservés pendant sept jours.

Le stockage et les backups utilisent un chemin UUID strictement sous leur racine canonicalisée, un répertoire réel non symlinké et un marqueur de propriété. La purge refuse toute incohérence, renomme d'abord le répertoire dans une quarantaine interne, puis effectue le `RemoveAll`. Le succès de l'opération et la suppression de l'état principal sont finalisés dans une même transaction SQLite, ce qui rend une purge interrompue rejouable.

## 9. Journaux et réponses

Les journaux structurés JSON passent par un filtre qui masque les clés et motifs liés à :

```text
password, passwd, pwd, token, secret, authorization, database_url
```

Le même nettoyage est appliqué aux logs des conteneurs renvoyés par l'API et aux erreurs d'opérations persistées. Les erreurs internes HTTP sont remplacées par `internal server error` ; le détail reste dans journald avec le `correlation_id`.

Le filtre réduit les fuites accidentelles mais ne remplace pas une discipline applicative : un secret journalisé sous un nom inattendu peut ne pas être détecté.

## 10. Durcissement systemd

L'unité fournie active notamment :

```text
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
MemoryDenyWriteExecute=true
UMask=0077
```

Les seuls chemins explicitement inscriptibles sont `/var/lib/centralcloud-agent` et `/run/centralcloud-agent`. L'accès au groupe `docker` reste néanmoins le privilège principal et domine une partie de ces protections.

## 11. État local et reprise

SQLite fonctionne avec :

- journal WAL ;
- clés étrangères activées ;
- une seule connexion ouverte ;
- tables séparées pour déploiements, opérations, étapes, idempotence, jetons de purge et audit.

Au démarrage, les opérations restées `running` repassent à `queued`. Les étapes externes sont conçues pour être rejouables : recherche des conteneurs par labels, vérification de propriété PostgreSQL et création conditionnelle des réseaux.

Les permissions du fichier SQLite et de ses fichiers WAL/SHM doivent rester limitées au compte de service.

## 12. Points d'attention

- Le socket Docker donne des privilèges de niveau root : protéger strictement le compte de service.
- La connexion PostgreSQL administrative utilise actuellement `sslmode=prefer`. Pour un PostgreSQL distant ou un réseau non fiable, imposer un tunnel privé ou faire évoluer l'implémentation vers une validation TLS stricte.
- Les variables d'environnement autorisées sont persistées dans SQLite en clair. L'allowlist doit rester limitée à des valeurs publiques/non sensibles ; les secrets utilisent les fichiers prévus.
- Le filtrage SAN applicatif exige une correspondance exacte. Les certificats et leur rotation doivent préserver l'identité déclarée.
- Les sauvegardes sont chiffrées, mais leurs répertoires, la clé maître et les restaurations doivent faire l'objet de tests réguliers.
- Les limites CPU/mémoire protègent les panels, pas le daemon Docker ni PostgreSQL eux-mêmes.
- Le stockage principal, PostgreSQL et SQLite restent locaux : la perte physique/logique du node reste un risque majeur sans sauvegarde externe testée.
- Si le Control Plane Infomaniak n'a pas d'IP de sortie fixe, préférer un VPN, réseau privé ou tunnel authentifié à une allowlist trop large.

## 13. Checklist de production

- [ ] API liée à une interface privée et filtrée par pare-feu.
- [ ] `security.mode: mtls`, certificats valides et TLS 1.3 compatible.
- [ ] SAN du Control Plane explicitement allowlisté.
- [ ] Firewall du VPS et `allowed_source_cidrs` limités aux réseaux de sortie réels du Control Plane.
- [ ] Clé maître aléatoire, permissions `0640`, copie dans un coffre séparé.
- [ ] Secrets de registre et PostgreSQL fournis par fichiers, jamais dans Git.
- [ ] Compte PostgreSQL provisionneur limité au strict nécessaire.
- [ ] Traefik avec `exposedByDefault=false`, nom de conteneur explicite et frontends isolés par déploiement.
- [ ] Image CentralPanel issue du dépôt autorisé et `docker.require_image_digest: true` en production.
- [ ] `panel.allowed_environment_keys` limitée aux valeurs non secrètes réellement nécessaires.
- [ ] PostgreSQL non public, écoute/gateways Docker et `pg_hba.conf` restrictifs.
- [ ] Pare-feu Docker/hôte vérifié, aucun port de panel publié directement.
- [ ] Répertoires `/etc/centralcloud-agent`, `/var/lib/centralcloud-agent` et `/run/centralcloud-agent` correctement permissionnés.
- [ ] Collecte journald et alertes sur santé, échecs d'opérations et capacité.
- [ ] Sauvegarde externe et restauration de PostgreSQL, SQLite, données panels, configuration et clé maître testées.
- [ ] Procédure de rotation des certificats, tokens de registre et secrets PostgreSQL définie.
