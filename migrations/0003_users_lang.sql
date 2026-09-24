-- 利用者ごとの表示の言語（Web と、ログイン済みの CLI・MCP に効く）。
-- NULL = 設定なし。設定なしのときは要求の Accept-Language（無ければ英語）へ落ちる。
-- 空文字は保存しない（「設定なし」は NULL だけで表す。store.SetUserLang が空文字を NULL に直す）。
ALTER TABLE users
  ADD COLUMN lang VARCHAR(8) NULL COMMENT '表示の言語（ja / en）。NULL は設定なし',
  ADD CONSTRAINT chk_users_lang CHECK (lang IS NULL OR lang IN ('ja', 'en'));
