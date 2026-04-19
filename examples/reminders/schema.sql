-- Schema for the reminder-capture bot. Idempotent: safe to re-apply before
-- every tool call so the DB auto-creates on first use.

CREATE TABLE IF NOT EXISTS users (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  phone_number TEXT    NOT NULL UNIQUE,
  slug         TEXT    NOT NULL UNIQUE,
  created_at   TEXT    NOT NULL,
  updated_at   TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS index_users_on_phone_number ON users(phone_number);
CREATE UNIQUE INDEX IF NOT EXISTS index_users_on_slug         ON users(slug);

CREATE TABLE IF NOT EXISTS reminders (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  body         TEXT    NOT NULL,
  remind_at    TEXT    NOT NULL,
  days_before  INTEGER NOT NULL DEFAULT 5,
  cadence      TEXT    NOT NULL DEFAULT 'once',
  user_id      INTEGER NOT NULL REFERENCES users(id),
  message_id   INTEGER,
  created_at   TEXT    NOT NULL,
  updated_at   TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS index_reminders_on_user_id    ON reminders(user_id);
CREATE INDEX IF NOT EXISTS index_reminders_on_message_id ON reminders(message_id);
