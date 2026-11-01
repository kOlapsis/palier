# Limites

## Ce que Palier ne fait pas

- Pas de comptes ni de rôles : un seul jeton protège l'interface.
- Pas d'agent ni de machine distante : un Palier par machine Docker.
- Pas d'autre alerte que le webhook.
- Pas de nettoyage des anciennes images.
- Pas de mise à jour de Palier lui-même.
- Pas de traduction des notes de version.
- Pas de fiches de catalogue locales : seules les fiches intégrées sont lues.

## Cas non pris en charge

- **Bases autres que PostgreSQL, MySQL et MariaDB.** Leurs majeures restent bloquées.
- **Identifiants de registre dans un `credsStore` ou des `credHelpers`.** Seule la section `auths` de `config.json` est lue.
- **Conteneurs hors Compose.** Ils sont seulement observés.
- **Notes de version hors GitHub et gitlab.com.** Une instance GitLab auto-hébergée, Codeberg ou une autre forge ne sont pas lues ; seules les fiches du catalogue complètent alors les notes.
- **Versions sans notes.** Sans notes de version, le plan est de niveau « inconnu » : il s'applique seul, protégé par la vérification et le retour arrière, mais personne n'a lu ce qui change.

## Injection de prompt

Les notes de version et les avis de sécurité sont des textes écrits par d'autres. Palier les traite comme des données, l'IA ne choisit que dans un vocabulaire fermé, et le code vérifie le plan. Mais les valeurs libres de `compose_edit` (commande, healthcheck, variable, volume) ne sont pas encore vérifiées contre les notes. La [feuille de route](roadmap.md#se-proteger-contre-linjection-de-prompt) décrit les protections à venir.

## Ce que le cache des plans implique

Un plan est gardé tant que ses entrées ne changent pas. Si l'IA juge mal une stack la première fois, ce jugement reste jusqu'à l'arrivée d'une nouvelle version, de nouvelles notes ou d'une nouvelle version de Palier.
