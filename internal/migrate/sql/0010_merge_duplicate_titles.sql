-- 0010_merge_duplicate_titles.sql
-- One ISBN is one title. Merge the rows that say otherwise, then make the
-- database refuse to accept them again.
--
-- The activity simulator imported the same book on several passes and created a
-- NEW title each time, so a single work appeared up to six times, each with its
-- own call number and its own copies. That is precisely the mistake this
-- schema exists to prevent: a book is bibliographic and a copy is a physical
-- object, and six copies of one work are one books row and six copies rows,
-- not six books (DOM-002, DEF-028).
--
-- A reader searching the catalogue saw the same title repeated with different
-- shelf marks and different availability, which is worse than a missing book:
-- it is a library that contradicts itself.

-- The keeper for each ISBN is the earliest row: it is the one whose call number
-- a librarian would have assigned first, and the one any external reference is
-- most likely to point at.
CREATE TEMP TABLE title_merge AS
SELECT b.id AS duplicate_id,
       first_value(b.id) OVER (PARTITION BY b.isbn13 ORDER BY b.created_at, b.id) AS keeper_id
  FROM books b
 WHERE b.isbn13 IS NOT NULL;

DELETE FROM title_merge WHERE duplicate_id = keeper_id;

-- Copies move to the keeper. Loans reference copies rather than books, so a
-- borrowing history follows its physical volume automatically and no loan is
-- orphaned (invariant I-07).
UPDATE copies c
   SET book_id = m.keeper_id
  FROM title_merge m
 WHERE c.book_id = m.duplicate_id;

-- Authors and subjects are unioned onto the keeper. A duplicate import may have
-- recorded an author the first did not, and losing it would make the catalogue
-- worse than before the merge.
INSERT INTO book_authors (book_id, author_id, position)
SELECT m.keeper_id, ba.author_id, ba.position
  FROM book_authors ba JOIN title_merge m ON m.duplicate_id = ba.book_id
ON CONFLICT DO NOTHING;

INSERT INTO book_subjects (book_id, subject_id)
SELECT m.keeper_id, bs.subject_id
  FROM book_subjects bs JOIN title_merge m ON m.duplicate_id = bs.book_id
ON CONFLICT DO NOTHING;

-- Reservations move too, so nobody loses their place in a queue because the
-- title they were waiting for was merged into another row.
UPDATE reservations r
   SET book_id = m.keeper_id
  FROM title_merge m
 WHERE r.book_id = m.duplicate_id
   AND NOT EXISTS (
       SELECT 1 FROM reservations existing
        WHERE existing.book_id = m.keeper_id
          AND existing.user_id = r.user_id
          AND existing.status IN ('pending','ready'));

DELETE FROM reservations r USING title_merge m WHERE r.book_id = m.duplicate_id;

DELETE FROM book_authors ba USING title_merge m WHERE ba.book_id = m.duplicate_id;
DELETE FROM book_subjects bs USING title_merge m WHERE bs.book_id = m.duplicate_id;
DELETE FROM books b USING title_merge m WHERE b.id = m.duplicate_id;

DROP TABLE title_merge;

-- And now the database will not accept the mistake again. A partial index,
-- because a title legitimately has no ISBN: Africana material, OAU
-- Publications and older Nigerian imprints frequently predate the scheme, and
-- refusing them would exclude exactly the collections this library is known for.
CREATE UNIQUE INDEX books_isbn13_unique ON books (isbn13) WHERE isbn13 IS NOT NULL;

-- The old non-unique index is now redundant: a unique index serves lookups too.
DROP INDEX IF EXISTS books_isbn13_idx;
