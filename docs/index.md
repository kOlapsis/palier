# Palier

Palier met à jour seul les stacks Docker Compose d'une machine, une stack à la fois et par paliers. Il ne met pas à jour à l'aveugle : il lit d'abord ce qui change, avec une IA, puis décide et applique sans attendre personne.

Pour chaque stack qui a une version plus récente, il lit les notes de version de toutes les versions sautées et les avis de sécurité qui touchent la version en service, et fait proposer un plan par une IA. Il vérifie ce plan, puis l'applique seul, étape par étape, avec un point de reprise à chaque palier. Il contrôle chaque étape avec ce que Docker rapporte, et si une étape échoue, il remet toute la stack au dernier palier vérifié.

## En quelques mots

- **Autonome par défaut.** Palier applique seul tout plan complet, majeures comprises. Il ne te demande une décision que pour ce qu'il ne peut pas faire lui-même : une étape bloquante, une étape irréversible ou un plan incomplet.
- **Les failles d'abord.** Palier lit les avis de sécurité des projets. Une version qui corrige une faille de la version en service passe sans délai de maturation.
- **Un seul conteneur.** Palier parle au Docker de sa machine par le socket. Il n'a pas d'agent, pas de compte et pas de licence.
- **La stack entière.** Une application et ses dépendances (base, cache) se mettent à jour ensemble, dans le bon ordre. Une base ne monte jamais plus haut que ce que son application déclare supporter.
- **Un plan vérifié.** L'IA choisit des opérations dans une liste fermée, et chaque affirmation du plan doit citer mot pour mot une note de version. Le code refuse tout plan qui ne passe pas ses contrôles.
- **Un retour au dernier palier vérifié.** Fichiers Compose, images et volumes reviennent à l'état du dernier palier qui a passé ses vérifications, y compris si Palier est arrêté au milieu d'une exécution.
- **Les majeures de bases.** PostgreSQL, MySQL et MariaDB changent de majeure par export et réimport sur un volume neuf. L'ancien volume n'est jamais touché.

## Par où commencer

1. [Installation](installation.md) : lancer le conteneur et ouvrir l'interface.
2. [Utilisation](utilisation.md) : lire un plan, l'appliquer, régler l'application automatique.
3. [Configuration](configuration.md) : variables d'environnement et labels.
4. [Fonctionnement](fonctionnement.md) : ce que Palier fait à chaque étape.
5. [Limites](limites.md) : ce que Palier ne fait pas.
6. [Feuille de route](roadmap.md) : ce qui vient ensuite.
