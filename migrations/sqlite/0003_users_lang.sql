-- 0003_users_lang.sql（MySQL）の SQLite 版。ALTER TABLE は 1 列ずつ。
-- SQLite の ALTER TABLE ADD COLUMN では CHECK 制約を足せないので、値の制限（ja / en / NULL）は
-- 書き込み側（store.SetUserLang）で守る。SetUserLang は空文字を NULL に直し、ja / en 以外は
-- エラーにして書かない（MySQL 版の CHECK と同じ集合）。読む側も、読めない値は「設定なし」として
-- 次の段へ落ちるので、万一入っても表示は壊れないが、設定は黙って効かなくなる。
ALTER TABLE users ADD COLUMN lang TEXT NULL;
