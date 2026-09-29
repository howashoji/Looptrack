-- トークン情報のスナップショットが「最初に送ろうとした時刻」（サーバの時計に直した値）。
-- 送信に失敗して手元に退避し、後で再送したスナップショットだけが持つ（それ以外は NULL）。
-- クライアントは退避してから再送するまでの経過秒（resend_delay_sec。クライアント自身の時計で測るので時計のずれに左右されない）を送り、
-- サーバは received_at からそれを引いて入れる。付与漏れの突き合わせ（issue_events.at から UsageAttachWindow 以内か）は
-- COALESCE(attempted_at, received_at) で見るので、再送が窓を過ぎても元の操作に付く。received_at は実際に受け取った時刻のまま残す。
ALTER TABLE usage_snapshots
  ADD COLUMN attempted_at DATETIME(6) NULL COMMENT '再送のとき、最初に送ろうとした時刻（received_at - resend_delay_sec）。NULL は再送でない';
