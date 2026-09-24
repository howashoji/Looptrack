CREATE TABLE system_settings (
  name       TEXT NOT NULL PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000')
);
CREATE TRIGGER trg_system_settings_updated_at AFTER UPDATE ON system_settings FOR EACH ROW WHEN NEW.updated_at IS OLD.updated_at BEGIN UPDATE system_settings SET updated_at = (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000') WHERE name = NEW.name; END;

CREATE TABLE setting_changes (
  id            INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
  at            DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  name          TEXT    NOT NULL,
  old_value     TEXT    NOT NULL DEFAULT '',
  new_value     TEXT    NOT NULL,
  actor_user_id INTEGER NULL,
  via           TEXT    NOT NULL,
  note          TEXT    NOT NULL DEFAULT '',
  ip            TEXT    NOT NULL DEFAULT '',
  CONSTRAINT fk_setting_changes_actor FOREIGN KEY (actor_user_id) REFERENCES users (id)
);
CREATE INDEX k_setting_changes_name_at ON setting_changes (name, at);
CREATE TRIGGER trg_setting_changes_no_update BEFORE UPDATE ON setting_changes WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: setting_changes は書き換えられません'); END;
CREATE TRIGGER trg_setting_changes_no_delete BEFORE DELETE ON setting_changes WHEN NOT EXISTS (SELECT 1 FROM append_only_unlock) BEGIN SELECT RAISE(ABORT, 'append-only: setting_changes は削除できません'); END;

ALTER TABLE web_sessions ADD COLUMN totp_verified INTEGER NOT NULL DEFAULT 0;

UPDATE web_sessions SET totp_verified = mfa_passed;

INSERT INTO system_settings (name, value)
SELECT 'two_factor', 'required' WHERE EXISTS (SELECT 1 FROM users);

INSERT INTO setting_changes (name, old_value, new_value, via, note)
SELECT 'two_factor', '', 'required', 'migration', '既存の運用（TOTP 必須）を引き継ぐ' WHERE EXISTS (SELECT 1 FROM users);
