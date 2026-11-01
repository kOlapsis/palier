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
  -e PALIER_AI_KEY=sk-ant-... \
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
    environment:
      PALIER_AI_KEY: sk-ant-...
    read_only: true
    cap_drop: [ALL]
    cap_add: [CHOWN, DAC_OVERRIDE]

volumes:
  palier-data: {}
```

```sh
DOCKER_GID=$(stat -c %g /var/run/docker.sock) docker compose up -d
```

## Trois points à respecter

**Le dossier des stacks se monte au même chemin que sur l'hôte.** Palier trouve le fichier Compose de chaque stack au chemin que donnent les labels de ses conteneurs (`com.docker.compose.project.working_dir`). Si vos stacks sont dans `/srv/stacks` sur l'hôte, montez `/srv/stacks:/srv/stacks`. Avec un autre chemin dans le conteneur, Palier ne trouve pas les fichiers.

**Le groupe du socket Docker.** `--group-add` donne à l'utilisateur du conteneur le droit de parler au socket. La valeur est le numéro de groupe du socket sur l'hôte.

**`CHOWN` et `DAC_OVERRIDE`.** Palier réécrit les fichiers Compose en gardant leur propriétaire, leur groupe et leur mode. Ces deux capacités lui servent à cela, et le reste est retiré. N'ajoutez pas `no-new-privileges` : il annulerait ces capacités, que le binaire porte comme capacités de fichier.

## Ouvrir l'interface

L'interface écoute sur le port 8080. Elle est protégée par un jeton, créé au premier démarrage dans le répertoire de données :

```sh
docker exec palier cat /var/lib/palier/token
```

Ouvrez `http://127.0.0.1:8080`, collez le jeton, et la liste des stacks apparaît après la première inspection. Pour choisir le jeton vous-même, définissez `PALIER_TOKEN`.

!!! note "Sans clé d'IA"
    Palier fonctionne sans `PALIER_AI_KEY`. Il détecte les versions et propose des plans calculés sur les versions seules, de niveau « inconnu ». Ces plans ne partent jamais en automatique.

## Journaux

Palier écrit ses journaux en JSON sur la sortie standard :

```sh
docker logs -f palier
```
