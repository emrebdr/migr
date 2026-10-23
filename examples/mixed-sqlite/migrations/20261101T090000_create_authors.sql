-- migr:up
CREATE TABLE authors (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL
);

-- migr:down
DROP TABLE authors;
