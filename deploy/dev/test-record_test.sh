#!/bin/bash
# deploy/dev/test-record.sh の集計（--parse）と並走の数え方（--count-ps）を、合成のログと ps の出力で確かめる。
# go test も DB も使わない。
#
#   bash deploy/dev/test-record_test.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

SRC=deploy/dev/test-record.sh
fails=0
ok() { echo "  ok: $*"; }
ng() {
  echo "  NG: $*" >&2
  fails=$((fails + 1))
}
check() { # <名前> <実際> <期待>
  if [ "$2" = "$3" ]; then
    ok "$1"
  else
    ng "${1}
    実際: ${2}
    期待: ${3}"
  fi
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
tab=$(printf '\t')

# --- 1. 緑のログ: PASS・SKIP・サブテスト・TestSQLiteSchemaMatchesMySQL の PASS・
#        パッケージの (cached) 1 件（数える側の対照）と、テストの出力の文中の "(cached)"（数えない側）。
cat >"$work/green.log" <<EOF
test-record: started=2026-09-29T15:00:00+09:00
test-record: sha=abc1234
test-record: dirty=0
test-record: tree=/work/verify-abc1234
test-record: test_db=mysql
test-record: command=go test -count=1 -v -timeout 30m ./...
test-record: parallel=0
=== RUN   TestAlpha
--- PASS: TestAlpha (0.00s)
=== RUN   TestBeta
=== RUN   TestBeta/sub_one
    beta_test.go:10: the result was (cached) in the message
    beta_test.go:11: ok  ${tab}example.invalid/fake${tab}(cached)
    --- PASS: TestBeta/sub_one (0.01s)
--- PASS: TestBeta (0.01s)
=== RUN   TestNeedsDocker
    docker_test.go:5: docker がありません
--- SKIP: TestNeedsDocker (0.00s)
PASS
ok  ${tab}github.com/howashoji/looptrack/internal/alpha${tab}0.512s
=== RUN   TestSQLiteSchemaMatchesMySQL
--- PASS: TestSQLiteSchemaMatchesMySQL (1.20s)
PASS
ok  ${tab}github.com/howashoji/looptrack/internal/store${tab}12.345s
ok  ${tab}github.com/howashoji/looptrack/internal/server${tab}101.432s
ok  ${tab}github.com/howashoji/looptrack/internal/server/static${tab}0.100s
ok  ${tab}github.com/howashoji/looptrack/internal/cached${tab}(cached)
?   ${tab}github.com/howashoji/looptrack/cmd/none${tab}[no test files]
test-record: elapsed=144
test-record: exit=0
EOF
got=$(bash "$SRC" --parse "$work/green.log")
want="2026-09-29T15:00:00+09:00 | SHA abc1234 | /work/verify-abc1234 | -count=1 あり | 2:24 (144s) | internal/server 101.432s | PASS 4 FAIL 0 SKIP 1 (TestNeedsDocker) | パッケージ ok 5 FAIL 0 cached 1 | 並走 0 | MySQL: LOOPTRACK_TEST_DB=mysql・TestSQLiteSchemaMatchesMySQL PASS | 終了コード 0 | log: $work/green.log"
check "緑のログ（文中の (cached) は数えず、パッケージの (cached) は数える）" "$got" "$want"

# --- 2. 赤のログ: FAIL（テストとパッケージ・ビルドの失敗）・TestSQLiteSchemaMatchesMySQL が無い・
#        dirty・並走 1 本（一覧つき）・所要時間が 1 時間以上。
cat >"$work/red.log" <<EOF
test-record: started=2026-09-29T16:00:00+09:00
test-record: sha=def5678
test-record: dirty=1
test-record: tree=/work/wt
test-record: test_db=mysql
test-record: command=go test -count=1 -v -timeout 30m ./...
test-record: parallel=1
test-record: parallel_proc=4242 03:10 go test ./internal/server
test-record: parallel_proc=4243 03:05 /tmp/go-build1/b001/server.test -test.v=true
=== RUN   TestGamma
    gamma_test.go:3: want 1, got 2
--- FAIL: TestGamma (0.00s)
=== RUN   TestDelta
--- PASS: TestDelta (0.00s)
FAIL
FAIL${tab}github.com/howashoji/looptrack/internal/server${tab}98.100s
FAIL${tab}github.com/howashoji/looptrack/internal/broken [build failed]
ok  ${tab}github.com/howashoji/looptrack/internal/fine${tab}0.200s
FAIL
test-record: elapsed=3725
test-record: exit=1
EOF
got=$(bash "$SRC" --parse "$work/red.log")
want="2026-09-29T16:00:00+09:00 | SHA def5678 (dirty: 未コミットの変更あり) | /work/wt | -count=1 あり | 1:02:05 (3725s) | internal/server FAIL 98.100s | PASS 1 FAIL 1 (TestGamma) SKIP 0 | パッケージ ok 1 FAIL 2 cached 0 | 並走 1 [4242 03:10 go test ./internal/server, 4243 03:05 /tmp/go-build1/b001/server.test -test.v=true] | MySQL: LOOPTRACK_TEST_DB=mysql・TestSQLiteSchemaMatchesMySQL 無し | 終了コード 1 | log: $work/red.log"
check "赤のログ（FAIL の名前・ビルドの失敗・MySQL の根拠が無い・dirty・並走の一覧）" "$got" "$want"

# --- 3. 中断したログ（末尾の記録が無い）と、TestSQLiteSchemaMatchesMySQL の SKIP。
cat >"$work/cut.log" <<EOF
test-record: started=2026-09-29T17:00:00+09:00
test-record: sha=0123abc
test-record: dirty=0
test-record: tree=/work/wt
test-record: test_db=mysql
test-record: command=go test -count=1 -v -timeout 30m ./...
test-record: parallel=0
=== RUN   TestSQLiteSchemaMatchesMySQL
    sqlite_test.go:20: LOOPTRACK_TEST_DSN がありません
--- SKIP: TestSQLiteSchemaMatchesMySQL (0.00s)
EOF
got=$(bash "$SRC" --parse "$work/cut.log")
want="2026-09-29T17:00:00+09:00 | SHA 0123abc | /work/wt | -count=1 あり | 不明（末尾の記録なし） | internal/server ok の行なし | PASS 0 FAIL 0 SKIP 1 (TestSQLiteSchemaMatchesMySQL) | パッケージ ok 0 FAIL 0 cached 0 | 並走 0 | MySQL: LOOPTRACK_TEST_DB=mysql・TestSQLiteSchemaMatchesMySQL SKIP | 終了コード 不明 | log: $work/cut.log"
check "中断したログ（所要時間と終了コードは不明・MySQL の根拠は SKIP）" "$got" "$want"

# --- 4. 並走の数え方。数える側（go test の本体。パスつきも）と数えない側（ラッパのシェル・grep・
#        go build・文中に go test があるだけの命令）を同じ一覧に置く。テストの実行ファイルは一覧にだけ出す。
cat >"$work/ps.txt" <<'EOF'
  PID ELAPSED COMMAND
  101    05:00 /bin/zsh -c env LOOPTRACK_TEST_DB=mysql go test -count=1 ./...
  102    04:59 go test -count=1 -v ./...
  103    04:50 /var/folders/xx/T/go-build123/b001/server.test -test.v=true -test.count=1
  104    00:01 grep -E [g]o test
  105    00:01 bash -c go test ./internal/store
  106    01:00 /usr/local/go/bin/go test ./internal/store
  107    00:02 go build ./...
  108    00:03 vim go test notes.txt
EOF
got=$(bash "$SRC" --count-ps "$work/ps.txt")
want="2
go test: 102 04:59 go test -count=1 -v ./...
テストの実行ファイル（本数に入れない）: 103 04:50 /var/folders/xx/T/go-build123/b001/server.test -test.v=true -test.count=1
go test: 106 01:00 /usr/local/go/bin/go test ./internal/store"
check "並走（go test の本体 2 本だけを数え、ラッパのシェル・grep・go build を数えない）" "$got" "$want"

printf '  PID ELAPSED COMMAND\n  201    00:01 /bin/bash -c go test ./...\n  202    00:01 grep go test\n' >"$work/ps0.txt"
got=$(bash "$SRC" --count-ps "$work/ps0.txt")
check "並走 0（ラッパのシェルと grep だけ）" "$got" "0"

if [ "$fails" -eq 0 ]; then
  echo "test-record_test: OK"
else
  echo "test-record_test: NG（${fails} 件）" >&2
  exit 1
fi
