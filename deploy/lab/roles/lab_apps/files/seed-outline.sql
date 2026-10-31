-- Three documents written before any upgrade, to check after each one that they are still there.
INSERT INTO teams (id, name, "createdAt", "updatedAt")
VALUES ('00000000-0000-4000-8000-0000000000a1', 'Palier lab', now(), now()) ON CONFLICT DO NOTHING;
INSERT INTO users (id, name, email, "teamId", role, "createdAt", "updatedAt")
VALUES ('00000000-0000-4000-8000-0000000000b1', 'Lab admin', 'admin@palier.lab', '00000000-0000-4000-8000-0000000000a1', 'admin', now(), now()) ON CONFLICT DO NOTHING;
INSERT INTO collections (id, name, "teamId", "createdById", "createdAt", "updatedAt")
VALUES ('00000000-0000-4000-8000-0000000000c1', 'Banc', '00000000-0000-4000-8000-0000000000a1', '00000000-0000-4000-8000-0000000000b1', now(), now()) ON CONFLICT DO NOTHING;
INSERT INTO documents (id, "urlId", title, text, "teamId", "collectionId", "createdById", "lastModifiedById", "publishedAt", "createdAt", "updatedAt")
VALUES
  ('00000000-0000-4000-8000-0000000000d1', 'labdoc0001', 'Premier document du banc', 'Écrit avant toute mise à jour.', '00000000-0000-4000-8000-0000000000a1', '00000000-0000-4000-8000-0000000000c1', '00000000-0000-4000-8000-0000000000b1', '00000000-0000-4000-8000-0000000000b1', now(), now(), now()),
  ('00000000-0000-4000-8000-0000000000d2', 'labdoc0002', 'Deuxième document du banc', 'Il doit survivre à chaque palier.', '00000000-0000-4000-8000-0000000000a1', '00000000-0000-4000-8000-0000000000c1', '00000000-0000-4000-8000-0000000000b1', '00000000-0000-4000-8000-0000000000b1', now(), now(), now()),
  ('00000000-0000-4000-8000-0000000000d3', 'labdoc0003', 'Troisième document du banc', 'Et au retour arrière.', '00000000-0000-4000-8000-0000000000a1', '00000000-0000-4000-8000-0000000000c1', '00000000-0000-4000-8000-0000000000b1', '00000000-0000-4000-8000-0000000000b1', now(), now(), now())
ON CONFLICT DO NOTHING;
