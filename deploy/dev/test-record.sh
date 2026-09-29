#!/bin/bash
# DB つきの全検査（go test ./...）を回し、「緑・赤を人に伝えるときに添える項目」を機械的に集めて記録の 1 行を出す。
#
#   deploy/dev/test-record.sh [--log <ログのパス>] [--allow-dirty]   全検査を回して記録の 1 行を出す
#   deploy/dev/test-record.sh --parse <ログのパス>                    go test を走らせず、保存したログから 1 行を作り直す
#   deploy/dev/test-record.sh --count-ps <ps の出力のファイル>         並走の数え方だけを試す（ps -eo pid,etime,command の形）
#
# 集める項目: SHA（未コミットの変更があれば止める。--allow-dirty なら回して「dirty」と明記する）・作業ツリーのパス・
# -count=1 の有無・所要時間（全体の実時間と internal/server の ok の行）・PASS / FAIL / SKIP の件数（-v の出力から。
# SKIP と FAIL はテスト名も）・パッケージの ok / FAIL / (cached) の件数・開始直前の並走本数・
# MySQL で走った根拠（--- PASS: TestSQLiteSchemaMatchesMySQL の有無）。
#
# 実行するのは次の 1 本（CLAUDE.md / CONTRIBUTING.md の「テスト」と同じ）。DSN は LOOPTRACK_TEST_DSN で上書きできる。
#   env -u LOOPTRACK_API_URL -u LOOPTRACK_PROJECT LOOPTRACK_TEST_DB=mysql LOOPTRACK_TEST_DSN=… \
#     go test -count=1 -v -timeout 30m ./...
#
# 検査するのはこのスクリプトが置かれた作業ツリー。検査したい SHA を固定した作業ツリー（git worktree add --detach）で回す。
# ログは作業ツリーの外に置く（既定は ${TMPDIR:-/tmp}）。作業ツリーの中に置くと、それ自体が未追跡のファイルになる。
# 終了コードは go test のもの（赤でも記録の 1 行は出す）。回す前に止めたときは 2。
#
# ログの先頭と末尾には「test-record: キー=値」の行を書く（--parse はそこから SHA・所要時間などを読む）。
# DB を使う検査は同じ MySQL を取り合うので、同時に 1 本だけ回す（並走の数はその確かめのために記録する）。
set -euo pipefail

DEFAULT_DSN='root:imdev@tcp(127.0.0.1:13306)/?parseTime=true'
GO_TEST_ARGS=(-count=1 -v -timeout 30m ./...)

die() {
  echo "test-record: $*" >&2
  exit 2
}

# count_parallel は ps -eo pid,etime,command の出力（標準入力）から、go test の実体を数える。
# 1 行目に本数、2 行目以降に数えたものの一覧（pid etime command）を出す。
#   - 数えるのは、命令の先頭の語の basename が go で次の語が test のもの（go test の本体。パスつきも）
#   - go test が起こしたテストの実行ファイル（先頭の語が *.test）は、本体と二重に数えないよう本数には入れず、一覧にだけ出す
#   - 先頭の語がシェル（bash -c 'go test …' のようなラッパ）・grep・その他の命令のものは、文中に go test があっても数えない
count_parallel() {
  awk '
    NR == 1 && $1 == "PID" { next }
    {
      first = $3; n = split(first, parts, "/"); base = parts[n]
      line = $1 " " $2
      for (i = 3; i <= NF; i++) line = line " " $i
      if (base == "go" && $4 == "test") { runs++; listed[++m] = "go test: " line }
      else if (base ~ /\.test$/) { listed[++m] = "テストの実行ファイル（本数に入れない）: " line }
    }
    END {
      print runs + 0
      for (i = 1; i <= m; i++) print listed[i]
    }
  '
}

# parse_log <ログのパス> はログから記録の 1 行を作って標準出力に出す。
parse_log() {
  local log=$1
  [ -f "$log" ] || die "ログがありません: ${log}"
  awk -v logpath="$log" '
    function meta(k, v) { m[k] = v }
    function join(arr, n,   s, i, lim) {
      s = ""; lim = (n > 10 ? 10 : n)
      for (i = 1; i <= lim; i++) s = s (i > 1 ? ", " : "") arr[i]
      if (n > lim) s = s " ほか " (n - lim) " 件"
      return s
    }
    /^test-record: [a-z_]+=/ {
      rest = substr($0, 14); eq = index(rest, "=")
      k = substr(rest, 1, eq - 1); v = substr(rest, eq + 1)
      if (k == "parallel_proc") { procs[++np] = v } else { meta(k, v) }
      next
    }
    # テストの結果の行。字下げはサブテスト。行頭から（空白だけを挟んで）始まるものだけを数える。
    /^[ ]*--- PASS: / { pass++; if ($3 == "TestSQLiteSchemaMatchesMySQL") schema = "PASS"; next }
    /^[ ]*--- FAIL: / { fail++; failed[fail] = $3; if ($3 == "TestSQLiteSchemaMatchesMySQL") schema = "FAIL"; next }
    /^[ ]*--- SKIP: / { skip++; skipped[skip] = $3; if ($3 == "TestSQLiteSchemaMatchesMySQL") schema = "SKIP"; next }
    # パッケージの結果の行（go test が行頭に出す ok / FAIL）。テストの出力の文中の "(cached)" は行頭が ok でないので数えない。
    /^ok[ \t]/ {
      pkgok++
      if ($3 == "(cached)") cached++
      if ($2 ~ /\/internal\/server$/) server = $3
      next
    }
    /^FAIL[ \t]+[^ \t]/ {
      pkgfail++
      if ($2 ~ /\/internal\/server$/) server = "FAIL " $3
      next
    }
    END {
      started = ("started" in m) ? m["started"] : "開始時刻不明"
      sha = ("sha" in m) ? m["sha"] : "不明"
      if (m["dirty"] == "1") sha = sha " (dirty: 未コミットの変更あり)"
      tree = ("tree" in m) ? m["tree"] : "不明"
      if (!("command" in m)) cnt = "不明"
      else if (m["command"] ~ /(^| )-count=1( |$)/) cnt = "あり"
      else cnt = "なし"
      if ("elapsed" in m) {
        s = m["elapsed"] + 0
        el = (s >= 3600 ? sprintf("%d:%02d:%02d", int(s / 3600), int(s % 3600 / 60), s % 60) : sprintf("%d:%02d", int(s / 60), s % 60)) " (" s "s)"
      } else el = "不明（末尾の記録なし）"
      srv = (server == "" ? "ok の行なし" : server)
      skipnames = (skip > 0 ? " (" join(skipped, skip) ")" : "")
      failnames = (fail > 0 ? " (" join(failed, fail) ")" : "")
      par = ("parallel" in m) ? m["parallel"] : "不明"
      if (np > 0) par = par " [" join(procs, np) "]"
      db = ("test_db" in m) ? "LOOPTRACK_TEST_DB=" m["test_db"] : "LOOPTRACK_TEST_DB 不明"
      sch = (schema == "" ? "無し" : schema)
      ex = ("exit" in m) ? m["exit"] : "不明"
      printf "%s | SHA %s | %s | -count=1 %s | %s | internal/server %s | PASS %d FAIL %d%s SKIP %d%s | パッケージ ok %d FAIL %d cached %d | 並走 %s | MySQL: %s・TestSQLiteSchemaMatchesMySQL %s | 終了コード %s | log: %s\n", \
        started, sha, tree, cnt, el, srv, pass, fail, failnames, skip, skipnames, pkgok, pkgfail, cached, par, db, sch, ex, logpath
    }
  ' "$log"
}

run() {
  local log="" allow_dirty=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --log) [ $# -ge 2 ] || die "--log にはパスが要ります"; log=$2; shift 2 ;;
      --allow-dirty) allow_dirty=1; shift ;;
      *) die "知らない引数: ${1}（使い方はこのファイルの先頭）" ;;
    esac
  done

  cd "$(dirname "$0")/../.."
  local tree sha dirty=0 status
  tree=$(git rev-parse --show-toplevel)
  sha=$(git rev-parse --short HEAD)
  status=$(git status --porcelain)
  if [ -n "$status" ]; then
    if [ "$allow_dirty" = 1 ]; then
      dirty=1
    else
      echo "$status" >&2
      die "作業ツリーに未コミットの変更（未追跡のファイルを含む）があります。記録は SHA ${sha} の根拠になりません。
コミットするか、SHA を固定した作業ツリーで回してください（git worktree add --detach <パス> <SHA>）。
それでも回すなら --allow-dirty（記録に dirty と出ます）。"
    fi
  fi

  local stamp
  stamp=$(date +%Y-%m-%dT%H:%M:%S%z | sed 's/\([+-][0-9][0-9]\)\([0-9][0-9]\)$/\1:\2/')
  if [ -z "$log" ]; then
    log="${TMPDIR:-/tmp}"
    log="${log%/}/looptrack-test-record-${sha}-$(date +%Y%m%d-%H%M%S).log"
  fi
  local logdir
  logdir=$(cd "$(dirname "$log")" 2>/dev/null && pwd -P) || die "ログの置き場がありません: $(dirname "$log")"
  log="${logdir}/$(basename "$log")"
  case "$logdir/" in
    "$(cd "$tree" && pwd -P)/"*) die "ログは作業ツリーの外に置いてください（${tree} の中です: ${log}）" ;;
  esac

  # 並走は go test を起こす直前に数える（数えた一覧もログに残す）。
  local counted parallel
  counted=$(ps -eo pid,etime,command | count_parallel)
  parallel=$(printf '%s\n' "$counted" | head -n 1)

  {
    echo "test-record: started=${stamp}"
    echo "test-record: sha=${sha}"
    echo "test-record: dirty=${dirty}"
    echo "test-record: tree=${tree}"
    echo "test-record: test_db=mysql"
    echo "test-record: command=go test ${GO_TEST_ARGS[*]}"
    echo "test-record: parallel=${parallel}"
    printf '%s\n' "$counted" | tail -n +2 | sed 's/^/test-record: parallel_proc=/'
  } >"$log"

  echo "test-record: SHA ${sha} を ${tree} で検査します（並走 ${parallel}）。ログ: ${log}" >&2
  local start end rc=0
  start=$(date +%s)
  env -u LOOPTRACK_API_URL -u LOOPTRACK_PROJECT \
    LOOPTRACK_TEST_DB=mysql \
    LOOPTRACK_TEST_DSN="${LOOPTRACK_TEST_DSN:-$DEFAULT_DSN}" \
    go test "${GO_TEST_ARGS[@]}" >>"$log" 2>&1 || rc=$?
  end=$(date +%s)
  {
    echo "test-record: elapsed=$((end - start))"
    echo "test-record: exit=${rc}"
  } >>"$log"

  parse_log "$log"
  return "$rc"
}

case "${1:-}" in
  --parse)
    [ $# -eq 2 ] || die "使い方: $0 --parse <ログのパス>"
    parse_log "$2"
    ;;
  --count-ps)
    [ $# -eq 2 ] || die "使い方: $0 --count-ps <ps -eo pid,etime,command の出力のファイル>"
    count_parallel <"$2"
    ;;
  *)
    run "$@"
    ;;
esac
