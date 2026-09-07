-- Optional physical-catalogue metadata; absent values remain NULL.
ALTER TABLE books ADD COLUMN edition text, ADD COLUMN language text,
    ADD COLUMN faculty text, ADD COLUMN department text;
CREATE INDEX books_public_created_idx ON books (created_at DESC, id) WHERE status = 'active';
CREATE INDEX books_public_year_idx ON books (published_year, id) WHERE status = 'active';
CREATE INDEX books_public_language_idx ON books (lower(language)) WHERE status = 'active';
CREATE INDEX books_public_faculty_department_idx ON books (lower(faculty), lower(department)) WHERE status = 'active';
CREATE INDEX book_subjects_subject_idx ON book_subjects (subject_id, book_id);
CREATE INDEX book_authors_author_idx ON book_authors (author_id, book_id);
