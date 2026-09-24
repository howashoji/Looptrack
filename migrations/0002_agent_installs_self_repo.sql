-- 導入済み通知に「looptrack 自身のリポジトリ（kit の正本）からの通知」の印を足す。
-- この印が立っている導入では、kit（core / loop）を配布物と比べない（手元が正本なので差が出るのが当たり前で、
-- 促される init の再実行はクライアントが拒否する）。
ALTER TABLE agent_installs
  ADD COLUMN self_repo TINYINT(1) NOT NULL DEFAULT 0
  COMMENT '1 は looptrack 自身のリポジトリからの通知（kit を配布物と比べない）' AFTER workspace;
