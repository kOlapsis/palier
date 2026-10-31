# Palier

Palier met à jour les stacks Docker Compose d'une machine, stack par stack et par paliers. Il lit les notes de version de toutes les versions sautées, fait proposer un plan par une IA, vérifie ce plan, l'applique étape par étape avec un point de reprise, contrôle le résultat avec ce que Docker rapporte, et remet toute la stack dans son état de départ si une étape échoue.

## Lancer Palier

```sh
docker build -t palier:local -f deploy/Dockerfile .
docker run -d --name palier --restart unless-stopped \
  --group-add "$(stat -c %g /var/run/docker.sock)" \
  --read-only --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE \
  -p 127.0.0.1:8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v palier-data:/var/lib/palier \
  -v /srv/stacks:/srv/stacks \
  -e PALIER_AI_KEY=sk-ant-... \
  palier:local
docker exec palier cat /var/lib/palier/token
```

Le dossier de vos stacks (ici `/srv/stacks`) se monte au même chemin que sur l'hôte : Palier lit et modifie les fichiers Compose au chemin que donnent les labels des conteneurs. `CHOWN` et `DAC_OVERRIDE` lui servent à réécrire ces fichiers en gardant leur propriétaire, leur groupe et leur mode. L'interface est sur le port 8080, protégée par le jeton. `deploy/compose.yaml` fait la même chose avec Compose.

La ligne de commande passe par le même code : `palier serve` (par défaut), `palier scan`, `palier plan <stack>`, `palier apply <stack> [--dry-run]`, `palier runs`.

## Variables

| Variable | Défaut | Rôle |
|---|---|---|
| `PALIER_DOCKER_HOST` | `unix:///var/run/docker.sock` | Moteur Docker |
| `PALIER_DATA_DIR` | `/var/lib/palier` | SQLite, exports de bases |
| `PALIER_LISTEN` | `:8080` | Port web |
| `PALIER_TOKEN` | généré au premier démarrage dans le répertoire de données | Accès à l'interface |
| `PALIER_AI_KEY`, `PALIER_AI_MODEL` | vide, `claude-sonnet-5-5` | IA ; sans clé, des plans sur les versions seules, de niveau inconnu |
| `PALIER_SCAN_INTERVAL` | `1h` | Fréquence d'inspection |
| `PALIER_MATURATION` | `72h` | Âge minimal d'une version proposée |
| `PALIER_STABLE_SECONDS` | `60` | Durée de stabilité exigée à chaque vérification |
| `PALIER_AUTO` | `off` | `patch` ou `minor` : application sans clic des plans sûrs, réversibles et de ce niveau au plus |
| `PALIER_WEBHOOK_URL` | vide | Reçoit un JSON à chaque plan prêt, fin d'exécution, retour arrière ou échec |
| `PALIER_GITHUB_TOKEN`, `PALIER_GITLAB_TOKEN` | vide | Notes de version sans la limite anonyme |
| `PALIER_REGISTRY_MIRROR` | vide | Miroir Docker Hub |
| `DOCKER_CONFIG` | `$HOME/.docker` | Identifiants des registres privés, section `auths` de `config.json` |

Un label `palier.hook.backup` sur un service donne une commande de sauvegarde, lancée dans son conteneur avant toute modification ; si elle échoue, rien n'est appliqué.

## Ce que Palier fait

- Il inventorie les stacks Compose, leurs services, leurs dépendances (`depends_on`) et leurs volumes. Un conteneur lancé hors Compose est seulement observé.
- Il trouve les versions plus récentes au registre (Docker Hub, GHCR, registres OCI) et lit les notes de version GitHub, ainsi que les extraits de documentation de son catalogue.
- L'IA propose un plan dans un vocabulaire fermé d'opérations : `set_image`, `compose_edit`, `recreate`, `stop`, `start`, `wait_stable`, `exec_check`, `db_dump_restore`. Le code vérifie le plan avant toute exécution : versions existantes, paliers obligatoires, une dépendance dans l'intervalle que son application cite à chaque étape, citations mot pour mot.
- Il applique avec un point de reprise de toute la stack : fichiers Compose, images, copie des volumes. Il vérifie chaque étape (en service, stable, sans boucle de redémarrage, `healthy` si l'image a un HEALTHCHECK) et revient au point de reprise à tout échec, y compris après un redémarrage de Palier au milieu d'une exécution.
- Il passe une base PostgreSQL d'une majeure à l'autre par export et réimport sur un volume neuf, sans jamais toucher à l'ancien.

## Ce qu'il ne fait pas

Pas de comptes ni de rôles, pas d'agent ni de machine distante, pas de licence, pas d'autre alerte que le webhook, pas de nettoyage des images, pas de mise à jour de lui-même. MySQL et MariaDB n'ont pas encore de méthode d'export et de réimport : leurs majeures restent bloquées. Les identifiants de registre rangés dans un `credsStore` ou des `credHelpers` ne sont pas lus.

Les preuves de chaque phase, jouées sur un banc avec un vrai Docker, sont dans `EVIDENCE.md`.
