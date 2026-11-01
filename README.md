# Palier

Palier met à jour seul les stacks Docker Compose d'une machine, stack par stack et par paliers, en remplacement de Watchtower. Il lit les notes de version de toutes les versions sautées, fait proposer un plan par une IA, vérifie ce plan, l'applique étape par étape avec un point de reprise, contrôle le résultat avec ce que Docker rapporte, et remet toute la stack au dernier palier vérifié si une étape échoue.

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
docker exec palier /palier token
```

Le dossier de vos stacks (ici `/srv/stacks`) se monte au même chemin que sur l'hôte : Palier lit et modifie les fichiers Compose au chemin que donnent les labels des conteneurs. `CHOWN` et `DAC_OVERRIDE` lui servent à réécrire ces fichiers en gardant leur propriétaire, leur groupe et leur mode. L'interface est sur le port 8080, protégée par le jeton. `deploy/compose.yaml` fait la même chose avec Compose.

La ligne de commande passe par le même code : `palier serve` (par défaut), `palier scan`, `palier plan <stack>`, `palier apply <stack> [--dry-run]`, `palier runs`, `palier token`.

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
| `PALIER_AUTO` | `all` | Application seule de tout plan complet, majeures comprises, sauf étape bloquante ou irréversible ; `minor` exclut les majeures, `patch` garde les correctifs, `off` fait tout valider à la main |
| `PALIER_WEBHOOK_URL` | vide | Reçoit un JSON à chaque plan prêt, fin d'exécution, retour arrière ou échec |
| `PALIER_GITHUB_TOKEN`, `PALIER_GITLAB_TOKEN` | vide | Notes de version GitHub et gitlab.com, avis de sécurité GitHub, sans la limite anonyme |
| `PALIER_REGISTRY_MIRROR` | vide | Miroir Docker Hub, pour les lectures par digest |
| `DOCKER_CONFIG` | `$HOME/.docker` | Identifiants des registres privés, section `auths` de `config.json` |

Un label `palier.hook.backup` sur un service donne une commande de sauvegarde, lancée dans son conteneur avant toute modification ; si elle échoue, rien n'est appliqué.

## Ce que Palier fait

- Il inventorie les stacks Compose, leurs services, leurs dépendances (`depends_on`) et leurs volumes. Un conteneur lancé hors Compose est seulement observé.
- Il trouve les versions plus récentes au registre (Docker Hub, GHCR, registres OCI) et lit les notes de version GitHub et gitlab.com, les avis de sécurité GitHub qui touchent la version en service (une version qui corrige une faille passe sans délai de maturation), ainsi que les extraits de documentation de son catalogue.
- L'IA propose un plan dans un vocabulaire fermé d'opérations : `set_image`, `compose_edit`, `recreate`, `stop`, `start`, `wait_stable`, `exec_check`, `db_dump_restore`. Le code vérifie le plan avant toute exécution : versions existantes, paliers obligatoires, une dépendance dans l'intervalle que son application cite à chaque étape, citations mot pour mot.
- Par défaut (`PALIER_AUTO=all`), il applique seul tout plan complet, majeures comprises, une stack après l'autre. Il ne demande une décision que pour une étape bloquante ou irréversible, ou un plan incomplet.
- Il applique avec un point de reprise de toute la stack : fichiers Compose, images, copie des volumes. Il vérifie chaque étape (en service, stable, sans boucle de redémarrage, `healthy` si l'image a un HEALTHCHECK), reprend un point de reprise après chaque étape vérifiée, et revient au dernier à tout échec, y compris après un redémarrage de Palier au milieu d'une exécution.
- Il passe une base PostgreSQL, MySQL ou MariaDB d'une majeure à l'autre par export et réimport sur un volume neuf, sans jamais toucher à l'ancien, et compare rôles, bases, tables, lignes, index et contraintes. Après un correctif de PostgreSQL qui change la bibliothèque C de l'image, il reconstruit les index des bases et met à jour leur version de collation.

## Ce qu'il ne fait pas

Pas de comptes ni de rôles, pas d'agent ni de machine distante, pas de licence, pas d'autre alerte que le webhook, pas de nettoyage des images, pas de mise à jour de lui-même. Les autres moteurs de base n'ont pas de méthode d'export et de réimport : leurs majeures restent bloquées. Les identifiants de registre rangés dans un `credsStore` ou des `credHelpers` ne sont pas lus.

La feuille de route est dans `docs/roadmap.md`. La documentation complète est dans `docs/` (MkDocs : `docker compose -f docs/compose.yaml up`, puis http://127.0.0.1:8000). Les preuves de chaque phase, jouées sur un banc avec un vrai Docker, sont dans `EVIDENCE.md`.
