-- 0004_seed.sql
-- Demonstration data. Safe to skip in production.
--
-- The books below are real HOL-style records using Library of Congress class
-- marks, chosen to exercise every loan policy and both wings of the building.

-- Password for every seeded account is: library2026
-- Argon2id hash, generated with the same parameters the application uses.
INSERT INTO users (identifier, email, full_name, first_name, last_name,
                   department, level, password_hash, role, category, status,
                   must_change_password)
VALUES
  ('LIB/ADMIN/001', 'admin@oauife.edu.ng', 'System Administrator',
   'System', 'Administrator', 'Hezekiah Oluwasanmi Library', NULL,
   '$argon2id$v=19$m=65536,t=1,p=4$YN8HcK2ZWbOpAIKg6v2KPQ$ZwoU2tiylfMlXWswKmUTHHwv7faESSC1Fuh/e99cjiw',
   'admin', NULL, 'active', true),
  ('LIB/STAFF/001', 'librarian@oauife.edu.ng', 'Circulation Librarian',
   'Circulation', 'Librarian', 'Readers Services', NULL,
   '$argon2id$v=19$m=65536,t=1,p=4$YN8HcK2ZWbOpAIKg6v2KPQ$ZwoU2tiylfMlXWswKmUTHHwv7faESSC1Fuh/e99cjiw',
   'librarian', NULL, 'active', true)
ON CONFLICT (identifier) DO NOTHING;

-- Books. Class A-J is shelved in the South wing, K-Z in the North wing.
INSERT INTO books (title, subtitle, isbn13, publisher, place_of_publication,
                   published_year, call_number, lcc_class, description)
VALUES
  ('Clean Code', 'A Handbook of Agile Software Craftsmanship', '9780132350884',
   'Prentice Hall', 'Upper Saddle River', 2008, 'QA76.76 .M37', 'Q',
   'Principles and practices of writing readable, maintainable software.'),
  ('Introduction to Algorithms', NULL, '9780262046305',
   'MIT Press', 'Cambridge, MA', 2022, 'QA76.6 .C662', 'Q',
   'Comprehensive treatment of algorithms and data structures.'),
  ('The Benin-Ife Controversy', 'Clash of Myths of Origin', '9789173747',
   'wadOrm Communications', 'Lagos', 2013, 'DT 515.15 .Ob21', 'D',
   'A study of competing origin narratives. Africana collection.'),
  ('Concise Oxford English Dictionary', NULL, '9780199601080',
   'Oxford University Press', 'Oxford', 2011, 'PE1628 .C67', 'P',
   'Reference collection. Consulted in the Reference Room.')
ON CONFLICT DO NOTHING;

-- Authors and subjects: the access points of the catalogue.
INSERT INTO authors (name) VALUES
  ('Robert C. Martin'), ('Thomas H. Cormen'), ('Wajeed Obomeghie')
ON CONFLICT (name) DO NOTHING;

INSERT INTO subjects (heading) VALUES
  ('Computer programming'), ('Software engineering'), ('Algorithms'),
  ('Benin - Nigeria - History'), ('Ile-Ife - Nigeria - History'),
  ('English language - Dictionaries')
ON CONFLICT (heading) DO NOTHING;

INSERT INTO book_authors (book_id, author_id, position)
SELECT b.id, a.id, 1 FROM books b, authors a
 WHERE (b.title = 'Clean Code' AND a.name = 'Robert C. Martin')
    OR (b.title = 'Introduction to Algorithms' AND a.name = 'Thomas H. Cormen')
    OR (b.title = 'The Benin-Ife Controversy' AND a.name = 'Wajeed Obomeghie')
ON CONFLICT DO NOTHING;

INSERT INTO book_subjects (book_id, subject_id)
SELECT b.id, s.id FROM books b, subjects s
 WHERE (b.title = 'Clean Code' AND s.heading IN ('Computer programming', 'Software engineering'))
    OR (b.title = 'Introduction to Algorithms' AND s.heading = 'Algorithms')
    OR (b.title = 'The Benin-Ife Controversy' AND s.heading IN ('Benin - Nigeria - History', 'Ile-Ife - Nigeria - History'))
    OR (b.title = 'Concise Oxford English Dictionary' AND s.heading = 'English language - Dictionaries')
ON CONFLICT DO NOTHING;

-- Copies. Five of Clean Code, each with its own accession number and one shared
-- call number: the distinction the whole schema exists to express (DOM-002).
INSERT INTO copies (book_id, accession_number, loan_policy)
SELECT id, 'HOL-7482' || gs, 'circulating'
  FROM books, generate_series(11, 15) AS gs
 WHERE title = 'Clean Code'
ON CONFLICT (accession_number) DO NOTHING;

INSERT INTO copies (book_id, accession_number, loan_policy)
SELECT id, 'HOL-61100' || gs, 'circulating'
  FROM books, generate_series(1, 2) AS gs
 WHERE title = 'Introduction to Algorithms'
ON CONFLICT (accession_number) DO NOTHING;

-- Accession numbers are unique across the whole collection, not per title, so
-- these must not collide with the ranges above. The library's own numbering has
-- the same property, which is the point of DOM-002.

-- Africana material is consulted in the building, not borrowed.
INSERT INTO copies (book_id, accession_number, loan_policy)
SELECT id, 'HOL-523153', 'restricted' FROM books
 WHERE title = 'The Benin-Ife Controversy'
ON CONFLICT (accession_number) DO NOTHING;

-- A dictionary that a student must never be able to borrow (DOM-004).
INSERT INTO copies (book_id, accession_number, loan_policy)
SELECT id, 'HOL-441002', 'reference_only' FROM books
 WHERE title = 'Concise Oxford English Dictionary'
ON CONFLICT (accession_number) DO NOTHING;

-- Refresh the weighted search vectors now that authors and subjects are linked.
SELECT books_refresh_search_vector(id) FROM books;
