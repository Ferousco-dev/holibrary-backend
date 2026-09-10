-- Catalogue validation accepts historical titles from 1000 onward. The service
-- enforces the moving upper bound of the current year plus one.
ALTER TABLE books DROP CONSTRAINT published_year_is_plausible;
ALTER TABLE books ADD CONSTRAINT published_year_is_plausible
    CHECK (published_year IS NULL OR published_year BETWEEN 1000 AND 2100);
