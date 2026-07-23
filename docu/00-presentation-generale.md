# CentralCloud Node Agent : présentation générale

## Qu'est-ce que c'est ?

CentralCloud Node Agent est un service système écrit en Go et installé sur chaque VPS géré par CentralCloud. Il expose une API HTTP privée utilisée par le Control Plane pour créer, exploiter, mettre à jour et supprimer des instances CentralPanel.

L'agent transforme une demande de haut niveau, par exemple « créer un panel pour ce projet », en opérations locales sur le nœud :

1. création d'un rôle et d'une base PostgreSQL dédiés ;
2. création des réseaux Docker nécessaires ;
3. téléchargement et démarrage de l'image CentralPanel ;
4. configuration du routage Traefik ;
5. exécution des migrations et du bootstrap administrateur ;
6. contrôle de santé de l'application ;
7. conservation de l'état et du suivi d'opération dans SQLite.

## À quoi sert-il ?

Il fournit une interface uniforme entre le Control Plane et l'infrastructure d'un VPS. Le Control Plane n'a donc pas besoin de manipuler directement Docker, PostgreSQL ou les fichiers du serveur.

Ses responsabilités principales sont :

- provisionner et piloter les déploiements CentralPanel ;
- appliquer des limites CPU, mémoire et PID aux conteneurs ;
- générer et protéger les secrets propres à chaque déploiement ;
- rendre les opérations idempotentes et récupérables après un redémarrage ;
- exposer l'état du nœud, les journaux et les métriques Prometheus ;
- sécuriser les échanges avec le Control Plane par HTTPS et un jeton Bearer
  distinct par Node en production (mTLS reste supporté pour les anciens Nodes).

Le Control Plane sonde périodiquement `GET /v1/health` et `GET /v1/resources`. L'agent n'effectue aucun callback et ne choisit jamais lui-même le node d'un projet.

## Architecture CentralCloud V1

```text
Utilisateur
    |
    | HTTPS
    v
Infomaniak : Laravel + Blade
    |-- authentification, Stripe, dashboard et Control Plane
    |-- MySQL : users, paiements, projets, nodes et deployments
    |
    | HTTPS + Bearer par Node
    v
Node VPS
    |-- CentralCloud Node Agent (Go)
    |    `-- SQLite : état local, opérations, idempotence et audit
    |-- Docker Engine
    |    |-- Traefik
    |    |-- CentralPanel A -- frontend A / backend A
    |    |-- CentralPanel B -- frontend B / backend B
    |    `-- CentralPanel C -- frontend C / backend C
    |-- PostgreSQL
    |    |-- base + rôle A
    |    |-- base + rôle B
    |    `-- base + rôle C
    `-- stockage persistant local A / B / C
```

MySQL appartient exclusivement au Control Plane. SQLite appartient exclusivement à l'agent et PostgreSQL contient les données applicatives des panels. L'agent ne contacte jamais le MySQL métier : Laravel décide du node, de l'image et des ressources, puis l'agent exécute localement cet ordre de haut niveau.

Traefik reste partagé mais chaque panel possède son propre réseau frontend. Les panels possèdent aussi un backend distinct pour joindre PostgreSQL via la gateway Docker du node ; aucun réseau panel-à-panel n'est partagé.

Les opérations qui modifient l'état sont asynchrones. L'API renvoie un `operation_id`, puis le client consulte `GET /v1/operations/{id}` jusqu'au statut `succeeded` ou `failed`.

## Limites de responsabilité

L'agent ne remplace pas :

- le Control Plane, qui décide quand et où déployer ;
- Docker, qui exécute les conteneurs ;
- Traefik, qui termine le trafic public et route les noms d'hôte ;
- PostgreSQL, qui stocke les données applicatives ;
- la PKI, qui doit fournir les certificats serveur, client et CA.

L'API de l'agent est une API d'administration privée. Elle ne doit jamais être publiée directement sur Internet.

Le stockage principal reste local au node pour la V1. La perte d'un node peut entraîner celle de PostgreSQL et des fichiers persistants si aucune sauvegarde externe n'est organisée.

## Documents

- [Déploiement](01-deploiement.md) : prérequis, construction, configuration, installation et vérification.
- [Fonctionnalités et API](02-fonctionnalites-et-api.md) : comportements, endpoints, paramètres, réponses et règles de validation.
- [Sécurité](03-securite.md) : modèle de confiance, protections en place, risques et checklist de production.
