CREATE TABLE project_guides (
  project_id INTEGER NOT NULL PRIMARY KEY,
  content    TEXT    NOT NULL,
  source     TEXT    NOT NULL DEFAULT '',
  updated_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000'),
  CONSTRAINT fk_project_guides_project FOREIGN KEY (project_id) REFERENCES projects (id)
);
CREATE TRIGGER trg_project_guides_updated_at AFTER UPDATE ON project_guides FOR EACH ROW WHEN NEW.updated_at IS OLD.updated_at BEGIN UPDATE project_guides SET updated_at = (strftime('%Y-%m-%d %H:%M:%f', 'now') || '000') WHERE project_id = NEW.project_id; END;
