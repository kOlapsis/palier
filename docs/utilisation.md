# Utilisation

## La liste des stacks

La page d'accueil montre chaque stack avec son état et l'âge de la dernière inspection. Palier inspecte toutes les stacks au démarrage, puis toutes les heures (`PALIER_SCAN_INTERVAL`), et aussi juste après chaque exécution ou décision.

| État | Ce qu'il veut dire |
|---|---|
| à jour | Aucune version plus récente n'est disponible. |
| plan prêt | Un plan existe et remplit les conditions de l'application automatique. |
| en attente de décision | Un plan existe et attend qu'on clique « Appliquer ». |
| plan incomplet | Le plan n'a pas passé les contrôles. La raison est affichée, et il ne s'applique pas. |
| détection incomplète | Les versions d'un service n'ont pas pu être lues au registre. |
| en cours, terminée, revenue en arrière, échec, abandonnée | Résultat de la dernière exécution. |
| observée seulement | Conteneur lancé hors Compose. Palier le montre mais n'y touche pas. |

## La page d'une stack

Elle montre chaque service avec son rôle (application ou dépendance), sa version en service, ses dépendances et les versions disponibles. Une version trop récente apparaît « en maturation » tant qu'elle n'a pas l'âge de `PALIER_MATURATION` (72 heures par défaut).

Sous les services vient le plan, étape par étape. Chaque étape indique :

- les services qui changent de version, et ceux qui sont arrêtés pendant l'étape ;
- les opérations, dans l'ordre ;
- le niveau de risque (sûr, attention, bloquant, inconnu) et la réversibilité ;
- les faits cités, avec un lien vers la note d'origine.

Le plan dit aussi quelles versions il écarte et pourquoi, par exemple une majeure de base que l'application ne cite pas.

## Les décisions

| Bouton | Effet |
|---|---|
| Essai à blanc | Vérifie que chaque image cible existe au registre et décrit ce que Palier ferait, sans rien modifier. |
| Appliquer | Lance l'exécution. Le bouton n'apparaît pas si le plan est incomplet. |
| Ignorer *service version* | Retire cette version des versions proposées. Le plan est recalculé sans elle. |
| Reporter jusqu'au | Empêche l'application automatique de cette stack jusqu'à la date choisie. |

Chaque décision est enregistrée avec son auteur (`interface`, `cli` ou `auto`) et sa date.

## Suivre une exécution

La page `/runs/{id}` se rafraîchit seule. Elle montre le résultat en tête, les étapes du plan (vérifiée, en cours, en échec ou à venir), puis chaque phase de l'exécution. Pendant une exécution, la page de la stack montre aussi ces étapes. En cas d'échec, elle donne la cause, le détail du retour arrière et un extrait des journaux du service qui a échoué.

Une exécution finit dans l'un de ces états :

| Résultat | Ce qu'il veut dire |
|---|---|
| terminée | Toutes les étapes sont passées. |
| revenue en arrière | Une étape a échoué, et toute la stack est revenue au dernier palier vérifié. Les étapes vérifiées avant l'échec restent appliquées. |
| abandonnée | Rien n'a été modifié : la stack avait changé depuis le calcul du plan, un hook de sauvegarde a échoué, ou le point de reprise était impossible. |
| échec | Même le retour arrière a échoué. Le détail dit ce qui reste à l'état modifié. |

## L'application automatique

Par défaut (`PALIER_AUTO=minor`), Palier applique seul un plan quand toutes ces conditions sont remplies :

- le plan est complet ;
- chaque étape est jugée sûre et réversible ;
- aucun changement ne dépasse le niveau autorisé (`patch` : correctifs seulement ; `minor` : correctifs et mineures ; jamais de majeure) ;
- la stack n'est pas reportée.

`PALIER_AUTO=patch` limite l'application automatique aux correctifs, et `PALIER_AUTO=off` la coupe : chaque plan attend alors un clic.

Sur la page de la stack, une ligne sous le plan dit si l'application automatique le prendra, ou pourquoi elle l'écarte.

Un plan est gardé tant que ses entrées ne changent pas : fichiers Compose, versions, notes de version, modèle et version de Palier. Des entrées identiques reçoivent donc toujours le même plan et le même jugement, sans nouvel appel à l'IA.

## Le webhook

Si `PALIER_WEBHOOK_URL` est défini, Palier y envoie un `POST` JSON à chacun de ces événements :

| `event` | Quand |
|---|---|
| `plan_ready` | Un nouveau plan complet vient d'être calculé. |
| `run_done` | Une exécution est terminée. |
| `run_rolled_back` | Une exécution est revenue en arrière. |
| `run_failed` | Le retour arrière a échoué. |
| `run_abandoned` | Une exécution a été abandonnée avant toute modification. |

```json
{"event": "run_rolled_back", "stack": "outline", "run": 12, "status": "rolled_back",
 "error": "…", "at": "2026-11-01T06:14:02Z"}
```

```json
{"event": "plan_ready", "stack": "whoami", "steps": 1,
 "changes": ["whoami v1.10.1 → v1.12.0"], "at": "2026-11-01T06:14:02Z"}
```

Les essais à blanc n'envoient pas de webhook.

## La ligne de commande

Les mêmes opérations existent en ligne de commande, par le même code. Elles écrivent du JSON sur la sortie standard et leurs journaux sur la sortie d'erreur.

```sh
docker exec palier /palier scan                  # une inspection de toutes les stacks
docker exec palier /palier plan outline          # le plan d'une stack
docker exec palier /palier apply outline --dry-run
docker exec palier /palier apply outline         # applique, code de sortie 1 si l'exécution ne finit pas « terminée »
docker exec palier /palier runs                  # les 50 dernières exécutions
docker exec palier /palier token                 # le jeton d'accès à l'interface
```

Sans argument, `palier` lance `serve`, c'est-à-dire l'interface et les inspections régulières.
