# Utilisation

## Le premier démarrage

Au premier démarrage, Palier travaille seul pendant quelques minutes avant que tout soit visible :

1. **L'inspection.** Il lit tous les conteneurs, les tags de chaque image au registre, les notes de version et les avis de sécurité. Comptez une à deux minutes pour une dizaine de stacks. La page d'accueil affiche « Première inspection en cours ».
2. **Les plans.** Pour chaque stack qui a une version plus récente, l'IA calcule un plan. Cela prend de quelques secondes à deux minutes par stack. Pendant ce temps, la stack est « plan en calcul ».
3. **Les mises à jour.** Chaque plan que Palier peut appliquer seul part aussitôt, une stack après l'autre. La stack passe « en cours », et sa page montre les étapes qui se cochent au fur et à mesure.

Une grosse montée de version peut durer longtemps. GitLab, avec 13 paliers obligatoires et leurs migrations de fond, demande environ deux heures. Les journaux (`docker logs -f palier`) disent à chaque instant ce que Palier fait.

## La liste des stacks

La page d'accueil montre chaque stack avec son état et l'âge de la dernière inspection. Palier inspecte toutes les stacks au démarrage, puis toutes les heures (`PALIER_SCAN_INTERVAL`), et aussi juste après chaque exécution ou décision.

| État | Ce qu'il veut dire |
|---|---|
| à jour | Aucune version plus récente n'est disponible. |
| plan en calcul | Des versions plus récentes existent, et l'IA n'a pas encore rendu son plan. |
| plan prêt | Un plan existe, et Palier l'appliquera seul dès que la stack précédente aura fini. |
| en attente de décision | Un plan existe, mais Palier ne peut pas l'appliquer seul : une étape est bloquante ou irréversible, la stack est reportée, ou `PALIER_AUTO` l'exclut. La raison est donnée sur la page de la stack. |
| plan incomplet | Le plan n'a pas passé les contrôles. La raison est affichée, et il ne s'applique pas. |
| détection incomplète | Les versions d'un service n'ont pas pu être lues au registre. |
| en cours, terminée, revenue en arrière, échec, abandonnée | Résultat de la dernière exécution. |
| observée seulement | Conteneur lancé hors Compose. Palier le montre mais n'y touche pas. |

## La page d'une stack

Elle montre chaque service avec son rôle (application ou dépendance), sa version en service, ses dépendances et les versions disponibles. Une version trop récente apparaît « en maturation » tant qu'elle n'a pas l'âge de `PALIER_MATURATION` (72 heures par défaut), sauf si elle corrige une faille de la version en service. Chaque faille connue de la version en service est signalée sous le service, avec sa gravité et un lien vers l'avis.

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
| Appliquer | Lance l'exécution tout de suite, sans attendre l'application automatique. Le bouton n'apparaît pas si le plan est incomplet. |
| Ignorer *service version* | Retire cette version des versions proposées. Le plan est recalculé sans elle. |
| Reporter jusqu'au | Empêche l'application automatique de cette stack jusqu'à la date choisie. |

Chaque décision est enregistrée avec son auteur (`interface`, `cli` ou `auto`) et sa date.

## Suivre une exécution

La page `/runs/{id}` se rafraîchit seule. Elle montre le résultat en tête, les étapes du plan (vérifiée, en cours, en échec ou à venir), puis chaque phase de l'exécution. Pendant une exécution, la page de la stack montre aussi ces étapes. En cas d'échec, elle donne la cause, le détail du retour arrière et un extrait des journaux du service qui a échoué.

Une exécution finit dans l'un de ces états :

| Résultat | Ce qu'il veut dire |
|---|---|
| terminée | Toutes les étapes sont passées. |
| revenue en arrière | Une étape a échoué, ou Palier a été redémarré pendant l'exécution, et toute la stack est revenue au dernier palier vérifié. Les étapes vérifiées avant restent appliquées, et en mode automatique, une nouvelle exécution repart de là à l'inspection suivante. |
| abandonnée | Rien n'a été modifié : la stack avait changé depuis le calcul du plan, un hook de sauvegarde a échoué, ou le point de reprise était impossible. |
| échec | Même le retour arrière a échoué. Le détail dit ce qui reste à l'état modifié. |

## L'application automatique

Palier est fait pour travailler seul. Par défaut (`PALIER_AUTO=all`), il applique tout plan complet, majeures comprises, quel que soit le niveau de risque jugé par l'IA. Ce qui le protège, c'est le point de reprise à chaque palier, la vérification par Docker et le retour au dernier palier vérifié. Les exécutions automatiques passent une stack après l'autre, pour ne pas charger la machine.

Un plan attend ta décision seulement dans ces cas :

- il est incomplet : une citation manque, un palier obligatoire est sauté, une dépendance sort de ce que l'application supporte ;
- une étape est bloquante : elle demande une action que Palier ne sait pas faire ;
- une étape est irréversible : même la restauration des volumes ne l'annulerait pas ;
- la stack est reportée.

Le niveau « attention » dit ce qui change, il ne bloque pas. `PALIER_AUTO=minor` exclut les majeures, `PALIER_AUTO=patch` garde seulement les correctifs, et `PALIER_AUTO=off` coupe l'application automatique : chaque plan attend alors un clic.

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
