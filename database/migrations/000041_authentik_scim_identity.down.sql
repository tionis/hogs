DROP INDEX IF EXISTS idx_scim_groups_external_id_unique;
DROP INDEX IF EXISTS idx_users_username_nocase;
DROP INDEX IF EXISTS idx_users_external_id_unique;

ALTER TABLE audit_log RENAME COLUMN user_username TO user_email;
ALTER TABLE sessions RENAME COLUMN user_username TO user_email;
ALTER TABLE game_identities RENAME COLUMN user_username TO user_email;
ALTER TABLE user_whitelists RENAME COLUMN user_username TO user_email;
ALTER TABLE users RENAME COLUMN username TO email;
