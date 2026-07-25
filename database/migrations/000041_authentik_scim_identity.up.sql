-- Consolidate SCIM rows that duplicate an OIDC user. Authentik's default SCIM
-- externalId is the same stable value as the hashed OIDC subject.
CREATE TEMP TABLE authentik_identity_merge AS
SELECT
    source.external_id,
    (
        SELECT candidate.id
        FROM users AS candidate
        WHERE candidate.external_id = source.external_id
        ORDER BY
            CASE WHEN candidate.oidc_subject = candidate.external_id THEN 0
                 WHEN candidate.oidc_subject <> '' THEN 1
                 ELSE 2 END,
            candidate.id
        LIMIT 1
    ) AS keeper_id,
    (
        SELECT candidate.email
        FROM users AS candidate
        WHERE candidate.external_id = source.external_id
        ORDER BY
            CASE WHEN candidate.oidc_subject = '' THEN 0 ELSE 1 END,
            candidate.id DESC
        LIMIT 1
    ) AS username
FROM users AS source
WHERE external_id <> ''
GROUP BY external_id;

-- Preserve user-owned configuration under Authentik's canonical username.
INSERT OR IGNORE INTO user_whitelists(user_email, server_id, username)
SELECT merge.username, whitelist.server_id, whitelist.username
FROM user_whitelists AS whitelist
JOIN users AS old_user ON old_user.email = whitelist.user_email
JOIN authentik_identity_merge AS merge ON merge.external_id = old_user.external_id;

DELETE FROM user_whitelists
WHERE user_email IN (
    SELECT old_user.email
    FROM users AS old_user
    JOIN authentik_identity_merge AS merge ON merge.external_id = old_user.external_id
    WHERE old_user.email <> merge.username
);

INSERT OR IGNORE INTO game_identities(user_email, game_type, username, external_id, source, updated_at)
SELECT merge.username, identity.game_type, identity.username, identity.external_id, identity.source, identity.updated_at
FROM game_identities AS identity
JOIN users AS old_user ON old_user.email = identity.user_email
JOIN authentik_identity_merge AS merge ON merge.external_id = old_user.external_id;

DELETE FROM game_identities
WHERE user_email IN (
    SELECT old_user.email
    FROM users AS old_user
    JOIN authentik_identity_merge AS merge ON merge.external_id = old_user.external_id
    WHERE old_user.email <> merge.username
);

INSERT OR IGNORE INTO server_access_grants(server_id, subject_type, subject, effect, capabilities)
SELECT grant_row.server_id, grant_row.subject_type, merge.username, grant_row.effect, grant_row.capabilities
FROM server_access_grants AS grant_row
JOIN users AS old_user ON old_user.email = grant_row.subject
JOIN authentik_identity_merge AS merge ON merge.external_id = old_user.external_id
WHERE grant_row.subject_type = 'user';

DELETE FROM server_access_grants
WHERE subject_type = 'user'
  AND subject IN (
      SELECT old_user.email
      FROM users AS old_user
      JOIN authentik_identity_merge AS merge ON merge.external_id = old_user.external_id
      WHERE old_user.email <> merge.username
  );

-- Union group memberships and retain the OIDC-bound row when one exists.
INSERT OR IGNORE INTO scim_group_members(group_id, user_id)
SELECT membership.group_id, merge.keeper_id
FROM scim_group_members AS membership
JOIN users AS old_user ON old_user.id = membership.user_id
JOIN authentik_identity_merge AS merge ON merge.external_id = old_user.external_id;

DELETE FROM sessions
WHERE user_email IN (
    SELECT old_user.email
    FROM users AS old_user
    JOIN authentik_identity_merge AS merge ON merge.external_id = old_user.external_id
);

UPDATE users
SET
    role = CASE
        WHEN EXISTS (
            SELECT 1
            FROM users AS duplicate
            WHERE duplicate.external_id = users.external_id AND duplicate.role = 'admin'
        ) THEN 'admin'
        ELSE role
    END,
    display_name = COALESCE(
        NULLIF((
            SELECT duplicate.display_name
            FROM users AS duplicate
            WHERE duplicate.external_id = users.external_id AND duplicate.display_name <> ''
            ORDER BY CASE WHEN duplicate.oidc_subject = '' THEN 0 ELSE 1 END, duplicate.id DESC
            LIMIT 1
        ), ''),
        display_name
    ),
    preferred_username = (
        SELECT merge.username
        FROM authentik_identity_merge AS merge
        WHERE merge.external_id = users.external_id
    ),
    active = CASE
        WHEN EXISTS (
            SELECT 1
            FROM users AS duplicate
            WHERE duplicate.external_id = users.external_id AND duplicate.active = 1
        ) THEN 1
        ELSE 0
    END
WHERE id IN (SELECT keeper_id FROM authentik_identity_merge);

DELETE FROM users
WHERE external_id <> ''
  AND id NOT IN (SELECT keeper_id FROM authentik_identity_merge);

UPDATE users
SET email = (
    SELECT merge.username
    FROM authentik_identity_merge AS merge
    WHERE merge.keeper_id = users.id
)
WHERE id IN (SELECT keeper_id FROM authentik_identity_merge);

DROP TABLE authentik_identity_merge;

-- The legacy schema called the panel identity an email address. HOGS now uses
-- Authentik usernames throughout; historical values remain unchanged.
ALTER TABLE users RENAME COLUMN email TO username;
ALTER TABLE user_whitelists RENAME COLUMN user_email TO user_username;
ALTER TABLE game_identities RENAME COLUMN user_email TO user_username;
ALTER TABLE sessions RENAME COLUMN user_email TO user_username;
ALTER TABLE audit_log RENAME COLUMN user_email TO user_username;

CREATE UNIQUE INDEX idx_users_external_id_unique
    ON users(external_id)
    WHERE external_id <> '';

CREATE UNIQUE INDEX idx_users_username_nocase
    ON users(username COLLATE NOCASE);

CREATE UNIQUE INDEX idx_scim_groups_external_id_unique
    ON scim_groups(external_id)
    WHERE external_id <> '';
