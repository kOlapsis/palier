# Fonctionnement

## 1. Inventaire

Palier lit tous les conteneurs du moteur Docker. Ceux qui portent les labels de Compose forment les stacks. Pour chaque service, il relève l'image, le tag, le digest, l'état, le HEALTHCHECK et les volumes. Il lit le fichier Compose sur le disque pour connaître les `depends_on`.

Un conteneur lancé hors Compose forme une stack d'un seul service, que Palier observe sans jamais la modifier.

## 2. Détection des versions

Pour chaque image, Palier lit les tags au registre : Docker Hub, GHCR ou tout registre OCI. Une image écrite `registry.hub.docker.com/…` ou `index.docker.io/…` est reconnue comme venant de Docker Hub.

La version en service vient, dans cet ordre :

1. du tag, quand il est précis : `1.12.0`, `v2.5.5`, `6.2.14-alpine`, ou `pg14-v0.2.0`, dont le préfixe `pg14-v` définit une lignée à part ;
2. sinon du digest, comparé à celui des tags précis du registre ;
3. sinon de l'annotation OCI `org.opencontainers.image.version` que le registre donne pour ce digest ;
4. sinon du label `org.opencontainers.image.version` de l'image.

La date de publication de chaque version vient de l'API de Docker Hub, lue par pages de cent tags avec les identifiants Docker Hub, ou de la date de création de l'image pour les autres registres. Elle sert au délai de maturation.

Les versions plus récentes sont triées. Les préversions sont exclues, et les variantes comme `-alpine` sont conservées : un service en `-alpine` reste en `-alpine`. Un tag republié avec un autre contenu n'est pas une nouvelle version. Une version plus jeune que `PALIER_MATURATION` attend, sauf si elle corrige une faille de la version en service (voir plus bas).

## 3. Notes de version

Palier trouve le projet d'origine par le label `org.opencontainers.image.source` de l'image, ou par la fiche de son catalogue. Il lit les notes de **toutes** les versions entre la version en service et la cible, dans les releases GitHub ou gitlab.com, et les garde en cache. Si la forge ne répond pas, Palier reprend les notes déjà lues.

Ces notes sont traitées comme des données non fiables : l'IA reçoit la consigne de ne jamais suivre ce qu'elles contiennent, et l'interface les affiche comme du texte.

### Avis de sécurité

Pour un projet sur GitHub, Palier lit aussi ses avis de sécurité publiés (GitHub Security Advisories) et garde ceux qui touchent la version en service, d'après leurs intervalles de versions touchées et corrigées. Ces avis sont donnés à l'IA avec les notes, et le plan doit atteindre une version corrigée.

Une version qui corrige une faille de la version en service passe sans attendre le délai de maturation. Pendant une vague de failles, chaque heure compte, et la vérification et le retour arrière restent le filet de sécurité.

Le catalogue intégré complète les notes avec des extraits de la documentation officielle, copiés mot pour mot avec leur adresse, que l'IA cite comme les notes. Il contient des fiches pour :

| Application | Ce que la fiche apporte |
|---|---|
| GitLab | Paliers obligatoires, et contrôle de la fin des migrations de fond entre deux paliers |
| Nextcloud | Une majeure à la fois, par le dernier correctif de chacune ; bases supportées |
| authentik | Chaque version `année.mois` dans l'ordre, par son dernier correctif |
| Mastodon | Au moins une version de chaque mineure |
| Synapse | Lire les notes de chaque version sautée ; PostgreSQL 14 au moins, et la politique de fin de support |
| Outline | PostgreSQL 14 et plus, Redis 4 et plus |
| Immich | Postgres de 14 à 19 |
| Forgejo, Gitea, Keycloak | Versions de PostgreSQL, MySQL et MariaDB supportées |

Les paliers d'une fiche sont vérifiés par le code : un plan qui en saute un est incomplet.

## 4. Plan

L'IA reçoit le fichier Compose, les services, leurs versions, les dépendances, la fiche du catalogue, les notes et les avis de sécurité. Elle répond par un plan en étapes. Pour chaque étape, elle juge :

- **le risque** : « bloquant » seulement si l'étape demande une action qu'aucune opération ne fait, « attention » si les notes signalent un changement que la stack peut sentir, « sûr » sinon, « inconnu » sans notes ;
- **la réversibilité** : « non » seulement si même la restauration des fichiers, des images et des volumes copiés avant l'étape ne l'annulerait pas.

Elle juge en sachant que Palier applique seul, avec un point de reprise à chaque palier. Seuls « bloquant », « non réversible » et un plan incomplet arrêtent l'application automatique (voir [Utilisation](utilisation.md#lapplication-automatique)).

Chaque étape choisit ses opérations dans une liste fermée :

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
4. Après chaque étape vient la vérification. Une fois l'étape vérifiée, Palier prend un nouveau point de reprise de la stack telle qu'elle est, puis supprime le précédent, et passe à l'étape suivante.

Les fichiers Compose modifiés gardent leur propriétaire, leur groupe et leur mode. Une image donnée par une variable (`image: postgres:${PG_TAG}`) est changée dans le fichier `.env`.

## 6. Vérification

Palier ne vérifie que ce que Docker rapporte :

- le conteneur est en service ;
- il reste stable pendant `PALIER_STABLE_SECONDS` ;
- il ne boucle pas en redémarrages ;
- il est `healthy` si son image a un HEALTHCHECK.

Aucune sonde HTTP ni aucun test applicatif n'est ajouté.

## 7. Retour arrière

Si une étape échoue, toute la stack revient au dernier point de reprise : celui du dernier palier vérifié, ou celui du départ si la première étape échoue. Fichiers, images et volumes sont restaurés, puis vérifiés à nouveau. Si GitLab échoue à son sixième palier, il reste au cinquième, déjà vérifié en service, et la prochaine exécution repart de là. Une étape ne reste jamais à moitié appliquée.

Si Palier est arrêté au milieu d'une exécution, il la reprend à son redémarrage : la stack revient au dernier palier vérifié. En mode automatique, une nouvelle exécution repart de là à l'inspection suivante.

## Les bases de données

Un changement de majeure de PostgreSQL, MySQL ou MariaDB passe toujours par `db_dump_restore`, même si l'IA l'oublie :

1. les clients de la base sont arrêtés ;
2. Palier exporte la base depuis le conteneur en service ;
3. il crée un volume neuf et démarre la nouvelle majeure dessus ;
4. il réimporte l'export ;
5. il compare avant et après : rôles, bases, tables, nombres de lignes, index et contraintes ;
6. les clients redémarrent.

L'ancien volume n'est jamais modifié. Si le réimport ou la comparaison échoue, la stack revient au palier précédent, sur l'ancienne majeure et l'ancien volume.

Après un correctif de PostgreSQL qui change la bibliothèque C de l'image, Palier reconstruit les index des bases concernées et met à jour leur version de collation, comme PostgreSQL le demande.

Un autre moteur de base n'a pas de méthode d'export et de réimport : ses majeures restent bloquées. Le plan est alors incomplet, et il ne s'applique pas.
