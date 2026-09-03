-- 0003_student_profile.sql
-- Student profile fields captured at physical registration.
--
-- The librarian already has the applicant's identity card in hand when the
-- account is created, so faculty, department and level cost nothing to record
-- and make the member roll searchable the way a librarian actually thinks:
-- "the 200-level Software Engineering students".
--
-- full_name is kept as the display name and is generated from the parts when
-- they are supplied, so nothing that already reads full_name has to change.

ALTER TABLE users
    ADD COLUMN first_name text,
    ADD COLUMN last_name  text,
    ADD COLUMN faculty    text,
    ADD COLUMN department text,
    ADD COLUMN level      text;

-- Members are looked up by cohort often enough to be worth an index.
CREATE INDEX users_department_level_idx ON users (department, level)
    WHERE role = 'member';
