# Installation

Palier tourne dans un conteneur sur la machine dont il met à jour les stacks.

## Construire l'image

```sh
docker build -t palier:local -f deploy/Dockerfile .
```

L'image ne contient que le binaire `palier`, le binaire `docker compose` et les certificats racines. Elle tourne avec un utilisateur non root.

## Lancer avec `docker run`

```sh
docker run -d --name palier --restart unless-stopped \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  --read-only --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE \
  -p 127.0.0.1:8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v palier-data:/var/lib/palier \
  -v /srv/stacks:/srv/stacks \
  -v "$HOME/.docker/config.json":/etc/palier-docker/config.json:ro \
  -e DOCKER_CONFIG=/etc/palier-docker \
  -e PALIER_AI_KEY=sk-ant-... \
  -e PALIER_GITHUB_TOKEN=ghp_... \
  palier:local
```

## Lancer avec Compose

```yaml
name: palier

services:
  palier:
    image: palier:local
    restart: unless-stopped
    group_add:
      - ${DOCKER_GID:?}
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - palier-data:/var/lib/palier
      - /srv/stacks:/srv/stacks
      - ${HOME}/.docker/config.json:/etc/palier-docker/config.json:ro
    environment:
      DOCKER_CONFIG: /etc/palier-docker
      PALIER_AI_KEY: sk-ant-...
      PALIER_GITHUB_TOKEN: ghp_...
    read_only: true
    cap_drop: [ALL]
    cap_add: [CHOWN, DAC_OVERRIDE]

volumes:
  palier-data: {}
```

```sh
DOCKER_GID=$(stat -c %g /var/run/docker.sock) docker compose up -d
```

## Ce qu'il faut fournir à Palier

| Quoi | Pourquoi |
|---|---|
| `PALIER_AI_KEY` | Indispensable en pratique. C'est l'IA qui lit les notes et les avis de sécurité, construit le plan et juge chaque étape. Sans elle, Palier ne travaille que sur les numéros de version. |
| Identifiants Docker Hub (`config.json` monté, `DOCKER_CONFIG`) | Docker Hub limite fortement les accès anonymes. Avec des identifiants, Palier lit les tags, les digests et les dates de publication sans être bloqué. |
| `PALIER_GITHUB_TOKEN` | Les notes de version et les avis de sécurité GitHub sont limités à 60 requêtes par heure sans jeton. |
| `PALIER_GITLAB_TOKEN` | Même chose pour les projets hébergés sur gitlab.com. |

Un jeton GitHub sans aucun droit particulier suffit : Palier ne lit que des données publiques.

## Trois points à respecter

**Le dossier des stacks se monte au même chemin que sur l'hôte.** Palier trouve le fichier Compose de chaque stack au chemin que donnent les labels de ses conteneurs (`com.docker.compose.project.working_dir`). Si vos stacks sont dans `/srv/stacks` sur l'hôte, montez `/srv/stacks:/srv/stacks`. Avec un autre chemin dans le conteneur, Palier ne trouve pas les fichiers.

**Le groupe du socket Docker.** `--group-add` donne à l'utilisateur du conteneur le droit de parler au socket. La valeur est le numéro de groupe du socket sur l'hôte.

**`CHOWN` et `DAC_OVERRIDE`.** Palier réécrit les fichiers Compose en gardant leur propriétaire, leur groupe et leur mode. Ces deux capacités lui servent à cela, et le reste est retiré. N'ajoutez pas `no-new-privileges` : il annulerait ces capacités, que le binaire porte comme capacités de fichier.

## Ouvrir l'interface

L'interface écoute sur le port 8080. Elle est protégée par un jeton, créé au premier démarrage dans le répertoire de données :

```sh
docker exec palier /palier token
```

Ouvrez `http://127.0.0.1:8080`, collez le jeton, et la liste des stacks apparaît après la première inspection. Pour choisir le jeton vous-même, définissez `PALIER_TOKEN`.

Le premier démarrage prend quelques minutes, et la page se met à jour toute seule. La page [Utilisation](utilisation.md#le-premier-demarrage) décrit ce qui se passe pendant ce temps.

!!! note "Sans clé d'IA"
    Palier fonctionne sans `PALIER_AI_KEY`, mais il perd l'essentiel. Il détecte les versions et propose des plans calculés sur les versions seules, de niveau « inconnu ». Ces plans s'appliquent seuls comme les autres, protégés par le retour arrière, mais personne n'a lu ce qui change, et les paliers obligatoires ou les dépendances ne sont pas pris en compte.

## Journaux

Palier écrit ses journaux en JSON sur la sortie standard :

```sh
docker logs -f palier
```
