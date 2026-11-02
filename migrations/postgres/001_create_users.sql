-- PostgreSQL 12+. Times are stored as UTC in TIMESTAMP columns, like DATETIME on MySQL.
CREATE TABLE users (
    id            BIGINT       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         VARCHAR(254) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    created_at    TIMESTAMP(6) NOT NULL,
    CONSTRAINT uq_users_email UNIQUE (email)
);
