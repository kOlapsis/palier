# Fonctionnement

## 1. Inventaire

Palier lit tous les conteneurs du moteur Docker. Ceux qui portent les labels de Compose forment les stacks. Pour chaque service, il relève l'image, le tag, le digest, l'état, le HEALTHCHECK et les volumes. Il lit le fichier Compose sur le disque pour connaître les `depends_on`.

Un conteneur lancé hors Compose forme une stack d'un seul service, que Palier observe sans jamais la modifier.

## 2. Détection des versions

Pour chaque image, Palier lit les tags au registre : Docker Hub, GHCR ou tout registre OCI. La version en service vient du tag quand il est précis, sinon du digest et des labels OCI de l'image.

Les versions plus récentes sont triées. Les préversions sont exclues, et les variantes comme `-alpine` sont conservées : un service en `-alpine` reste en `-alpine`. Un tag republié avec un autre contenu n'est pas une nouvelle version. Une version plus jeune que `PALIER_MATURATION` attend.

## 3. Notes de version

Palier trouve le projet d'origine par le label `org.opencontainers.image.source` de l'image, ou par la fiche de son catalogue. Il lit les notes de **toutes** les versions entre la version en service et la cible, dans les releases GitHub ou gitlab.com, et les garde en cache. Si la forge ne répond pas, Palier reprend les notes déjà lues.

Ces notes sont traitées comme des données non fiables : l'IA reçoit la consigne de ne jamais suivre ce qu'elles contiennent, et l'interface les affiche comme du texte.

Le catalogue intégré contient des fiches pour Outline et GitLab : extraits de documentation (versions de base supportées, paliers obligatoires) et contrôles nommés, comme la fin des migrations de fond de GitLab.

## 4. Plan

L'IA reçoit le fichier Compose, les services, leurs versions, les dépendances, la fiche du catalogue et les notes. Elle répond par un plan en étapes. Chaque étape choisit ses opérations dans une liste fermée :

| Opération | Ce qu'elle fait |
|---|---|
| `set_image` | Change l'image d'un service dans le fichier Compose. |
| `compose_edit` | Modifie le fichier Compose de façon structurée : variables d'environnement, commande, healthcheck, volumes, retrait d'un service. |
| `recreate` | Recrée les services dont le fichier a changé. |
| `stop` / `start` | Arrête ou démarre des services dans l'ordre des dépendances. |
| `wait_stable` | Attend que des services restent stables. |
| `exec_check` | Lance un contrôle défini par une fiche du catalogue, jamais par l'IA. |
| `db_dump_restore` | Change la majeure d'une base par export et réimport. |

L'IA n'écrit jamais de commande. Le code vérifie ensuite tout le plan avant de le montrer :

- les services et les versions existent ;
- les paliers obligatoires de la fiche sont respectés, un palier par étape ;
- à chaque étape, chaque dépendance reste dans l'intervalle que l'application déclare supporter ;
- chaque fait cité se retrouve mot pour mot dans les notes.

Un fait sans citation valable est retiré. Un plan qui ne passe pas est marqué incomplet, avec la raison, et ne s'applique jamais.

## La stack suit son application

Dans une stack, l'application est le service dont aucun autre ne dépend. Les services dont elle dépend (base, cache) suivent.

La version cible d'une dépendance n'est pas la plus récente qui existe. C'est la plus haute que l'application déclare supporter, d'après une citation de ses notes ou de sa fiche. Sans aucune citation, la dépendance reste sur sa majeure et ne reçoit que des correctifs et des mineures. Le plan dit alors quelles majeures existent et pourquoi elles ne sont pas proposées.

Cette règle vaut à chaque étape, et c'est elle qui fixe l'ordre : si une version de l'application exige une base plus récente, la base passe d'abord.

## 5. Exécution

1. Les hooks de sauvegarde (`palier.hook.backup`) tournent. Si l'un échoue, rien n'est modifié.
2. Palier prend un point de reprise : copie des fichiers Compose et de leur fichier `.env`, image et digest de chaque service, copie des volumes des services qui changent.
3. Chaque étape applique ses opérations dans l'ordre. Les clients sont arrêtés dans l'ordre inverse des dépendances et redémarrés dans l'ordre.
4. Après chaque étape vient la vérification, puis l'étape suivante.

Les fichiers Compose modifiés gardent leur propriétaire, leur groupe et leur mode. Une image donnée par une variable (`image: postgres:${PG_TAG}`) est changée dans le fichier `.env`.

## 6. Vérification

Palier ne vérifie que ce que Docker rapporte :

- le conteneur est en service ;
- il reste stable pendant `PALIER_STABLE_SECONDS` ;
- il ne boucle pas en redémarrages ;
- il est `healthy` si son image a un HEALTHCHECK.

Aucune sonde HTTP ni aucun test applicatif n'est ajouté.

## 7. Retour arrière

Si une étape échoue, toute la stack revient au point de reprise : fichiers restaurés, anciennes images, anciens volumes, puis une nouvelle vérification. Il n'y a jamais de retour partiel.

Si Palier est arrêté au milieu d'une exécution, il la reprend à son redémarrage et la défait.

## Les bases de données

Un changement de majeure de PostgreSQL, MySQL ou MariaDB passe toujours par `db_dump_restore`, même si l'IA l'oublie :

1. les clients de la base sont arrêtés ;
2. Palier exporte la base depuis le conteneur en service ;
3. il crée un volume neuf et démarre la nouvelle majeure dessus ;
4. il réimporte l'export ;
5. il compare avant et après : rôles, bases, tables, nombres de lignes, index et contraintes ;
6. les clients redémarrent.

L'ancien volume n'est jamais modifié. Si le réimport ou la comparaison échoue, la stack repart sur l'ancienne majeure et l'ancien volume.

Après un correctif de PostgreSQL qui change la bibliothèque C de l'image, Palier reconstruit les index des bases concernées et met à jour leur version de collation, comme PostgreSQL le demande.

Un autre moteur de base n'a pas de méthode d'export et de réimport : ses majeures restent bloquées et ne partent jamais en automatique.
