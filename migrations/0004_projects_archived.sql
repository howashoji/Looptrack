-- プロジェクトのアーカイブ（管理画面の「削除」）。論理削除で、行もイシュー・コメント・イベントも残す。
-- NULL = 使用中。値があればアーカイブした時刻で、一覧（Web・REST・MCP・CLI）から隠し、起票・更新・コメントを拒む
-- （判定は store.AccessibleProjects / store.UserMemberships と service の書き込みの入口）。管理者が NULL に戻せる。
-- slug と prefix は一意のまま残るので、アーカイブしても使い回されない。
ALTER TABLE projects
  ADD COLUMN archived_at DATETIME(6) NULL COMMENT 'アーカイブした時刻。NULL は使用中';
