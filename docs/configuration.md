# Configuration

Toute la configuration passe par des variables d'environnement. La page `/settings` de l'interface montre les valeurs lues, en lecture seule. Une valeur invalide arrête Palier au démarrage avec un message qui nomme la variable.

## Variables

| Variable | Défaut | Rôle |
|---|---|---|
| `PALIER_DOCKER_HOST` | `unix:///var/run/docker.sock` | Moteur Docker. |
| `PALIER_DATA_DIR` | `/var/lib/palier` | Base SQLite, jeton, exports de bases. |
| `PALIER_LISTEN` | `:8080` | Adresse d'écoute de l'interface. |
| `PALIER_TOKEN` | généré au premier démarrage | Jeton d'accès à l'interface. Sans valeur, il est écrit dans `token` du répertoire de données. |
| `PALIER_AI_KEY` | vide | Clé de l'API Anthropic. Sans clé, les plans sont calculés sur les versions seules. |
| `PALIER_AI_MODEL` | `claude-sonnet-5-5` | Modèle utilisé pour les plans. |
| `PALIER_SCAN_INTERVAL` | `1h` | Intervalle entre deux inspections (durée Go : `30m`, `2h`…). |
| `PALIER_MATURATION` | `72h` | Âge minimal d'une version avant qu'elle soit proposée. Une version qui corrige une faille de la version en service n'attend pas. |
| `PALIER_STABLE_SECONDS` | `60` | Durée pendant laquelle un service doit rester stable à chaque vérification. |
| `PALIER_AUTO` | `all` | `all`, `minor`, `patch` ou `off`. Voir [l'application automatique](utilisation.md#lapplication-automatique). |
| `PALIER_WEBHOOK_URL` | vide | Adresse qui reçoit les événements en JSON. |
| `PALIER_GITHUB_TOKEN` | vide | Lit les notes de version et les avis de sécurité GitHub sans la limite anonyme. |
| `PALIER_GITLAB_TOKEN` | vide | Lit les notes de version GitLab (gitlab.com) sans la limite anonyme, et celles des projets privés. |
| `PALIER_REGISTRY_MIRROR` | vide | Miroir de Docker Hub (`mirror.gcr.io`, par exemple), utilisé pour les lectures par digest. |
| `DOCKER_CONFIG` | `$HOME/.docker` | Dossier du `config.json` qui contient les identifiants des registres privés. |

## Registres privés

Palier lit la section `auths` du `config.json` de Docker, au format habituel. Montez ce fichier dans le conteneur et faites pointer `DOCKER_CONFIG` sur son dossier :

```sh
-v /root/.docker/config.json:/etc/palier-docker/config.json:ro \
-e DOCKER_CONFIG=/etc/palier-docker
```

Les identifiants de Docker Hub servent aussi à l'API de Docker Hub, qui donne la date de publication des tags, cent à la fois : avec eux, la limite de requêtes est plus haute qu'en accès anonyme.

Les identifiants rangés dans un `credsStore` ou des `credHelpers` (ECR, GAR, ACR) ne sont pas lus.

## Miroir de Docker Hub

Le miroir allège les requêtes faites à Docker Hub, qui limite les accès anonymes. Palier n'y lit que ce qui est désigné par un digest, et revient sur Docker Hub si le miroir ne l'a pas. La liste des tags et le digest d'un tag restent lus sur Docker Hub : un miroir n'en garde qu'une partie, parfois ancienne. Les identifiants Docker Hub ne sont jamais envoyés au miroir.

## Labels sur vos services

Deux labels, posés dans le fichier Compose de vos stacks, changent le comportement de Palier.

### `palier.hook.backup`

Une commande de sauvegarde, lancée dans le conteneur du service avant toute modification de la stack, avec `sh -c`. Si elle échoue, l'exécution est abandonnée et rien n'est modifié.

```yaml
services:
  postgres:
    image: postgres:16.4
    labels:
      palier.hook.backup: "pg_dumpall -U outline > /backup/avant-palier.sql"
```

### `palier.depends_on`

Des dépendances en plus de celles de `depends_on`, séparées par des virgules. Utile quand une application dépend d'un service sans que le fichier Compose le dise.

```yaml
services:
  app:
    labels:
      palier.depends_on: "postgres,redis"
```

Une dépendance suit son application : sa version cible dépend de ce que l'application déclare supporter (voir [Fonctionnement](fonctionnement.md#la-stack-suit-son-application)).
