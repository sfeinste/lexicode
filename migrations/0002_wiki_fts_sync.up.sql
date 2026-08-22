-- S33: keep wiki_fts (external-content FTS5, 0001 §5) in step with wiki_pages on every
-- write path. S15 synced inserts by hand in WikiRepo.CreatePage; the wiki service now
-- updates and archives pages too, so the sync moves into triggers — one mechanism, no
-- write path left to forget. The manual insert in CreatePage is removed with this
-- migration (the insert trigger would double-index otherwise).
--
-- The UPDATE trigger fires only when an indexed column changes: position drags and
-- front-matter edits do not churn the index.

CREATE TRIGGER wiki_pages_fts_ai AFTER INSERT ON wiki_pages BEGIN
  INSERT INTO wiki_fts(rowid, title, body, tags)
  VALUES (new.rowid, new.title, new.body, new.tags);
END;

CREATE TRIGGER wiki_pages_fts_au AFTER UPDATE OF title, body, tags ON wiki_pages BEGIN
  INSERT INTO wiki_fts(wiki_fts, rowid, title, body, tags)
  VALUES ('delete', old.rowid, old.title, old.body, old.tags);
  INSERT INTO wiki_fts(rowid, title, body, tags)
  VALUES (new.rowid, new.title, new.body, new.tags);
END;

CREATE TRIGGER wiki_pages_fts_ad AFTER DELETE ON wiki_pages BEGIN
  INSERT INTO wiki_fts(wiki_fts, rowid, title, body, tags)
  VALUES ('delete', old.rowid, old.title, old.body, old.tags);
END;

-- Reconcile any pre-trigger drift: rebuild reads the whole content table back into the index.
INSERT INTO wiki_fts(wiki_fts) VALUES ('rebuild');
