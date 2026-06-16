UPDATE relations SET evidence = '' WHERE evidence IS NULL;
ALTER TABLE relations ALTER COLUMN evidence SET NOT NULL;
ALTER TABLE relations ALTER COLUMN evidence SET DEFAULT '';
