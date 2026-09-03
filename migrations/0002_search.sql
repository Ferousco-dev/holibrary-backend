-- 0002_search.sql
-- Full-text search over the three catalogue access points the card catalogue
-- has always provided: author, title and subject (DOM-007, REQ-028..031).
--
-- Postgres' own text search is used rather than a separate search service.
-- Article 10 of the process: no component without a measured need. If NFR-001
-- (95th percentile under 500 ms) is ever missed, revisit with numbers in hand.

ALTER TABLE books ADD COLUMN search_vector tsvector;

-- Weights rank a title match above an author match above a subject match, so
-- searching "algorithms" surfaces books called Algorithms before books merely
-- classified under it.
CREATE OR REPLACE FUNCTION books_refresh_search_vector(target_book uuid)
RETURNS void AS $$
BEGIN
    UPDATE books b SET search_vector =
        setweight(to_tsvector('english', coalesce(b.title, '')), 'A') ||
        setweight(to_tsvector('english', coalesce(b.subtitle, '')), 'B') ||
        setweight(to_tsvector('english', coalesce(
            (SELECT string_agg(a.name, ' ')
               FROM book_authors ba JOIN authors a ON a.id = ba.author_id
              WHERE ba.book_id = b.id), '')), 'B') ||
        setweight(to_tsvector('english', coalesce(
            (SELECT string_agg(s.heading, ' ')
               FROM book_subjects bs JOIN subjects s ON s.id = bs.subject_id
              WHERE bs.book_id = b.id), '')), 'C')
    WHERE b.id = target_book;
END;
$$ LANGUAGE plpgsql;

CREATE INDEX books_search_idx ON books USING gin (search_vector);

-- Supporting indexes for the non-full-text access points.
CREATE INDEX books_isbn13_idx      ON books (isbn13) WHERE isbn13 IS NOT NULL;
CREATE INDEX books_call_number_idx ON books (call_number);
CREATE INDEX books_lcc_class_idx   ON books (lcc_class);
CREATE INDEX authors_name_trgm_idx ON authors (lower(name));
