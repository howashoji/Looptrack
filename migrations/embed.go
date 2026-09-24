// Package migrations は DB スキーマの変更履歴（NNNN_名前.sql）をバイナリに埋め込む。
// 適用は internal/store の Migrate が行う。適用済みのファイルは書き換えず、変更は新しい番号で足す。
//
// 0001_init.sql は 1.0.0 のスキーマ全体。1.0.0 より前の開発版の 0001〜0013 はここに統合した
// （1.0.0 より前の開発版の適用記録を持つ DB は、migrate が拒否して書き換えを求める）。以後の変更は 0002 から足す。
//
// MySQL 用は直下、SQLite 用は sqlite/ に置く。規則:
//   - 直下に NNNN_名前.sql を足したら、sqlite/ に同じファイル名で SQLite の方言に直したものを足す
//     （型は INTEGER / TEXT / BLOB / DATETIME、ALTER TABLE は 1 列ずつ、KEY は CREATE INDEX、
//     ON UPDATE CURRENT_TIMESTAMP はトリガ、追記専用の表は UPDATE / DELETE を拒否するトリガ。sqlite/0001_init.sql の注記を参照）。
//   - 同じ番号の対が欠けている・列の構成が MySQL と違うと internal/store のテストが失敗する
//     （TestSQLiteMigrationsMirrorMySQL・TestSQLiteSchemaMatchesMySQL）。
package migrations

import "embed"

//go:embed *.sql sqlite/*.sql
var FS embed.FS
