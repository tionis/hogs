ALTER TABLE users ADD COLUMN oidc_issuer TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN oidc_subject TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN preferred_username TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_users_oidc_identity
    ON users(oidc_issuer, oidc_subject)
    WHERE oidc_issuer <> '' AND oidc_subject <> '';
