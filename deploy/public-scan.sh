#!/usr/bin/env bash
# 公開対象（git archive で書き出す中身）に、会社・運用環境・個人に固有の語と旧名が残っていないかを調べる。
#
#   bash deploy/public-scan.sh            # HEAD + 作業ツリーの未コミットの変更を調べる（既定）
#   bash deploy/public-scan.sh --head     # HEAD だけを調べる（コミット済みの内容の確認）
#   bash deploy/public-scan.sh --summary  # 件数だけ
#   bash deploy/public-scan.sh --rev <tree-ish> [--only company|names|links|python|ids|aiconf]
#
# 公開物は git archive で作るので、.gitattributes の export-ignore を付けたファイル
# （社内の運用に要るもの。private/ の下）は調べない。見つかれば一覧を出して 1 で終わる。0 件なら 0。
# リンク切れ: 公開物の Markdown の相対リンクの先が公開物に無いもの（private/ の中を指すリンクはここで見つかる）。
# AI ツールの設定: このリポジトリで作業する AI 向けの設定・指示（.claude/・.codex/・CLAUDE.md・AGENTS.md など）が
# 公開物に入っていないか（導入先へ配るものは kit/ にあり、名前が違うので当たらない）。
# 内部管理番号は、テスト（_test.go・_test.mjs）では「説明のコメントに書いた ID」だけを見る
# （入力・期待値としてデータに使う ID は見ない）。既にある分より増えたら落ちる（ids_comments_baseline）。
#
# 既定で作業ツリーの未コミットの変更を HEAD の書き出しに重ねる（--head / --rev では重ねない）。
# git archive だけを見ていた頃は、コミット前に走らせると必ず 0 件になり、これから入る混入を見逃した。
# 重ねる対象には未追跡のファイルも含める（git add の前に混入を捕まえるため）。
# 何を調べたかは出力の冒頭と末尾に出す（緑の根拠の範囲が分かるように）。未追跡を含めた件数も併せて出す
# （赤になったとき、原因が未追跡のファイルかどうかがその場で分かるように）。
#
# 例外（公開物に残してよいもの）:
#   - 公開リポジトリとイメージの名前と、配布元の識別子: github.com/howashoji/looptrack・ghcr.io/howashoji/looptrack・net.howashoji.looptrack
#   - 配布元・著作権者としての社名の表記: 行に「配布元」「copyright」「Developer ID」を含むもの
set -euo pipefail

rev=HEAD
summary=0
only=all
overlay=1
while [ $# -gt 0 ]; do
  case "$1" in
    --rev) rev=$2; overlay=0; shift 2 ;;
    --head) overlay=0; shift ;;
    --summary) summary=1; shift ;;
    --only) only=$2; shift 2 ;;
    -h|--help) sed -n '2,25p' "$0"; exit 0 ;;
    *) echo "public-scan: 知らない引数: $1" >&2; exit 2 ;;
  esac
done

root=$(git rev-parse --show-toplevel)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
work=$tmp/tree
mkdir -p "$work"

# 作業ツリーの未コミットの変更を HEAD に重ねる。
# 対象は「HEAD との差分がある追跡ファイル」= 追加（git add 済み）・変更・削除（stage の有無を問わない）と、
# 未追跡のファイル。未追跡を外していた頃は、新しく足したファイルの混入が git add するまで誰にも見えなかった。
#
# 未追跡のうち .gitignore で無視されるものは重ねない（--exclude-standard）。
# 追跡されない限り git archive には入らないので、調べても純粋な偽陽性にしかならない
# （実測で、無視されるものまで含めると他のセッションの作業ツリーが丸ごと入り、検査が常時赤になった）。
# private/ の未追跡のファイルは、重ねたうえで git archive が export-ignore でディレクトリごと外すので、
# ここでの分岐は要らない（重ね合わせは公開物に入るかどうかを git archive 自身に決めさせる形になっている）。
#
# 重ね方: 使い捨ての index に HEAD を読み、差分のあったパスだけを作業ツリーの中身で入れ替えた木を作り、
# その木を git archive に通す。公開物から何を外すか（.gitattributes の export-ignore）の判定を
# git archive 自身に任せるため。展開した後のファイルへ cp で重ね、重ねるかどうかを
# git check-attr export-ignore で決めていた頃は、ディレクトリに付く指定（末尾が / のもの）が
# その配下のファイルに対して unspecified を返す一方、git archive はディレクトリごと外すというずれがあり、
# 公開物に入らないファイルの未コミットの変更が公開物として調べられていた。
# 使い捨ての index は $tmp に置くので、作業ツリーの index には触らない
# （入れ替えたパスの blob だけがオブジェクトとして残る。どこからも参照されないので gc で消える）。
changed=0
scanrev=$rev
# --head / --rev の経路でも空で存在させる（ラチェットが母数から外すために読む）。
: > "$tmp/untracked"
if [ "$overlay" = 1 ]; then
  # --no-renames: 改名（git mv）を「元のパスの削除 + 新しいパスの追加」として両方出させる。
  # 改名の検出が効くと新しいパスしか出ず、公開物から外したつもりの元のパスが HEAD の中身のまま残る。
  git -C "$root" diff --no-renames --name-only -z HEAD > "$tmp/changed"
  git -C "$root" ls-files --others --exclude-standard -z > "$tmp/untracked"
  cat "$tmp/untracked" >> "$tmp/changed"
  changed=$(tr -dc '\0' < "$tmp/changed" | wc -c | tr -d ' ')
  untracked=$(tr -dc '\0' < "$tmp/untracked" | wc -c | tr -d ' ')
  export GIT_INDEX_FILE="$tmp/index"
  git -C "$root" read-tree HEAD
  git -C "$root" update-index --add --remove -z --stdin < "$tmp/changed"
  scanrev=$(git -C "$root" write-tree)
  unset GIT_INDEX_FILE
fi
git -C "$root" archive --format=tar "$scanrev" | tar -x -C "$work"

scanned="${rev}（$(git -C "$root" rev-parse --short "$rev")）だけ。作業ツリーの未コミットの変更は見ていません"
if [ "$overlay" = 1 ]; then
  scanned="HEAD（$(git -C "$root" rev-parse --short HEAD)）+ 作業ツリーの変更 $changed 件"
  if [ "$untracked" -gt 0 ]; then
    # 赤になったときに原因を切り分けられるように、未追跡のファイル名も出す（多いときは先頭だけ）。
    list=$(tr '\0' '\n' < "$tmp/untracked" | sort | sed -n '1,10p' | paste -sd, -)
    if [ "$untracked" -gt 10 ]; then list="$list ほか"; fi
    scanned="${scanned}（未追跡 $untracked 件を含む: ${list}）"
  fi
fi
echo "== 調べた対象: $scanned"

# 語の境界: grep -E の \b は実装で差があるので、英数字以外（行頭・行末を含む）で囲む。
w() { printf '(^|[^A-Za-z0-9_])(%s)([^A-Za-z0-9_]|$)' "$1"; }

# 社内固有の語
company="howashoji|宝和|153\.126\.|devnew|reqweave|dev-infra|$(w 'hpc|hpcm|issui')"
# Python の名残
python_left="$(w 'python[0-9.]*')|issue\.py|issue_freshness\.py|usage_hook\.py|usage_snapshot\.py|hookcmd\.py"
python_left="$python_left|pyjson|\.pyc|__pycache__|PYTHONPATH|PYTHONUTF8|PYTHONIOENCODING|PYTHONDONTWRITEBYTECODE"
python_left="$python_left|difflib|ReportLab"
# 調べない場所（パスの前置きで外す）
python_skip='^\./internal/.*/testdata/' # 凍結した記録。hook が旧い形のコマンドを読めることを担保する入力なので中身を変えない
python_skip="$python_skip"'|^\./migrations/' # 本番に適用済みで変更しない
python_skip="$python_skip"'|^\./go\.sum:' # 依存の名前（go-difflib）。外部のものなので変えられない
python_skip="$python_skip"'|^\./internal/client/worktree/' # 片付けで無視する生成物の名前（__pycache__・.pyc）。Python への依存ではない
python_skip="$python_skip"'|^\./internal/client/hook/loop/worktrees' # 同上（hook 側）
# 内部管理番号（このリポジトリ自身のイシューの ID。公開物に残さない）
ids_bare='(IM|HPC-DEV|HPC-M|REQ|RW)-[0-9]{3,4}'
ids="$(w "$ids_bare")"
# 調べない場所（パスの前置きで外す）
# テスト（_test.go・_test.mjs）は、入力・期待値としてイシューの ID をそのままデータに使うので、この区分では外す。
# ただし _test.go・_test.mjs も公開物として配られるので、丸ごと見ないままにはしない。
# 説明のコメントに書いた ID は下の ids_comments が別に見る（データの ID と説明の ID を分けて扱う）。
ids_skip='^\./[^:]*_test\.(go|mjs):'
ids_skip="$ids_skip"'|^\./internal/.*/testdata/' # 凍結した記録。中身を変えない
# （internal/store/testdata/legacy は 1.0.0 より前のマイグレーションの写しで、適用済みの DB の COMMENT と一致させるため内部の番号を含んだまま残す）
ids_skip="$ids_skip"'|^\./internal/testdata/' # 合成フィクスチャ（旧形式の Markdown）。記録と対なので変えない
# テストの説明コメントに残っている分の基準（ラチェット）: 「<件数> <パス>」。
# この数より増えたら落ちる。減らしたらこの数も下げる（減ったままにすると、同じ数だけ黙って空く）。
# 一覧に ID そのものは書かない（このファイル以外は自分自身も検査の対象で、書けば自己矛盾になる）。
# 中身は片付け済みで、基準は空（0）。以後 1 行でも増えたら落ちる。
ids_comments_baseline='
'

# 旧名（受け入れ条件 1。対応表の左の列）
names="$(w 'imserver')|$(w 'IM_[A-Z][A-Z0-9_]*')|X-IM-|\.im-kit|\.im-work|\.issue-freshness|im-loop|im:(loop|begin|end|inject|init)"
# 調べない場所（旧名）: 「旧名を読まない・持ち込まない」ことを確かめるテストは、旧名を書くのが仕事なので外す
# 旧名を読まないことを確かめるテスト（旧名を書かないと確かめられない）
names_skip='^\./internal/client/env/env_test\.go:|^\./internal/client/verify/verify_test\.go:'
names_skip="$names_skip"'|^\./internal/clitest/env_test\.go:|^\./internal/server/setup_test\.go:'
names_skip="$names_skip"'|^\./internal/client/api/client_test\.go:|^\./internal/client/session/session_test\.go:'
names_skip="$names_skip"'|^\./internal/client/hook/loop/gates_test\.go:'
names_skip="$names_skip"'|^\./internal/client/kitinit/texts_test\.go:' # kit に旧名が無いことを確かめる語の一覧
# 本番に適用済みで変更しない（COMMENT に旧名が残る）
names_skip="$names_skip"'|^\./migrations/|^\./internal/store/testdata/legacy/'
# 例外の語（行から取り除いてから判定する）
allow='github\.com/howashoji/looptrack|ghcr\.io/howashoji/looptrack|net\.howashoji\.looptrack'
allow_line='配布元|[Cc]opyright|Developer ID'
# 生成物だけの残骸の説明（利用者ガイド）。例として __pycache__ を挙げるのは Python の名残ではない
allow_line="$allow_line"'|生成物だけの残骸'

scan() { # $1: 区分名 $2: パターン $3: 調べないパスの前置き（省略可） $4: case なら大文字小文字を区別する
  local label=$1 pat=$2 skip=${3:-} case=${4:-} hits gi=-i gq=-i
  # 旧名は大文字小文字が決まっている（環境変数は大文字・ファイル名は小文字）。区別しないと DB のユーザー名
  # im_app・DB 名 im_test などを IM_… と読んでしまう。
  if [ "$case" = case ]; then gi=""; gq=""; fi
  # このファイル自身（語の一覧を持つ）は調べない
  hits=$(cd "$work" && grep -rnIE $gi --exclude=public-scan.sh -- "$pat" . 2>/dev/null |
    grep -vE -- "$allow_line" |
    { if [ -n "$skip" ]; then grep -vE -- "$skip" || true; else cat; fi; } |
    while IFS= read -r line; do
      body=${line#*:*:}
      stripped=$(printf '%s' "$body" | sed -E "s#$allow##g")
      if printf '%s' "$stripped" | grep -qE $gq -- "$pat"; then printf '%s\n' "${line#./}"; fi
    done || true)
  local n=0 files=0
  if [ -n "$hits" ]; then
    n=$(printf '%s\n' "$hits" | wc -l | tr -d ' ')
    files=$(printf '%s\n' "$hits" | cut -d: -f1 | sort -u | wc -l | tr -d ' ')
  fi
  echo "== $label: $n 行・$files ファイル"
  if [ "$n" -gt 0 ] && [ "$summary" = 0 ]; then printf '%s\n' "$hits"; fi
  [ "$n" -eq 0 ]
}

links() { # 公開物の Markdown の相対リンクの先が公開物にあるか（外部の URL・ページ内の #…・testdata の中は見ない）
  local hits
  hits=$(cd "$work" && find . -name '*.md' -type f -not -path '*/testdata/*' | sort | while IFS= read -r f; do
    grep -noE '\]\([^)#[:space:]]+' "$f" 2>/dev/null | while IFS=: read -r ln m; do
      target=${m#](}
      case "$target" in [a-zA-Z]*:*) continue ;; esac
      [ -e "$(dirname "$f")/$target" ] || printf '%s:%s: リンク先が公開物にありません: %s\n' "${f#./}" "$ln" "$target"
    done
  done || true)
  local n=0
  [ -n "$hits" ] && n=$(printf '%s\n' "$hits" | wc -l | tr -d ' ')
  echo "== リンク切れ: $n 件"
  if [ "$n" -gt 0 ] && [ "$summary" = 0 ]; then printf '%s\n' "$hits"; fi
  [ "$n" -eq 0 ]
}

aiconf() { # このリポジトリで作業する AI ツールの設定・指示が公開物に入っていないか（パスの名前で見る）
  # 置き場を変えられない設定（Claude Code・Codex・Cursor・Copilot・Gemini など）は開発側の運用で、公開物には要らない。
  # .gitattributes の export-ignore で外す。外し忘れ（新しい AI ツールの設定を足した・指定を消した）をここで捕まえる。
  # testdata/ の中は見ない（kit の導入を確かめるテストの入力として、これらの名前を持つことがある）。
  local hits n=0
  hits=$(cd "$work" && find . -path '*/testdata' -prune -o \( \
      \( -type d \( -name .claude -o -name .codex -o -name .cursor -o -name .gemini -o -name .windsurf \
         -o -name .junie -o -name .kiro -o -name .roo -o -name .continue -o -name .amazonq -o -name superpowers \) \) \
      -o -name CLAUDE.md -o -name CLAUDE.local.md -o -name AGENTS.md -o -name AGENTS.override.md -o -name GEMINI.md \
      -o -name .cursorrules -o -name .windsurfrules -o -name .clinerules -o -name .mcp.json -o -name '.aider*' \
      -o -path ./.github/copilot-instructions.md -o -path ./.github/instructions -o -path ./.github/prompts \
      -o -path ./.github/chatmodes -o -path ./.github/hooks -o -path ./.github/mcp.json -o -path ./.vscode/mcp.json \
    \) -print | sed 's#^\./##' | sort || true)
  [ -n "$hits" ] && n=$(printf '%s\n' "$hits" | wc -l | tr -d ' ')
  echo "== AI ツールの設定: $n 件"
  if [ "$n" -gt 0 ] && [ "$summary" = 0 ]; then
    printf '%s\n' "$hits" | sed 's#$#: AI ツールの設定・指示は公開物に入れない（.gitattributes に export-ignore を足す）#'
  fi
  [ "$n" -eq 0 ]
}

ids_comments() { # テスト（_test.go・_test.mjs）の説明コメントに書いた内部管理番号
  # _test.go・_test.mjs も公開物として配られるので、ids_skip でパスごと外した中を、コメントだけ見直す。
  # 1 行ずつ、次の順で「データの ID」と「説明の ID」を分ける:
  #   1. URL の scheme（http:// など）を外したうえで、最初の // から後ろをコメントとして取る。
  #   2. そのファイルがデータとして使っている ID（コメントを外した側に出てくるもの）をコメントから取り除く。
  #      テストが自分で作った合成のイシュー（フィクスチャの ID）を指す説明は、これで落ちる。
  #   3. 残りに ID があれば、このリポジトリの実在のイシューを指す説明なので見つけたことにする。
  # 見つけられない形（既知の穴）: /* … */ の中と、文字列リテラルの中の // から後ろ。
  local hits diffs n=0 files=0 total cur=$tmp/ids-comments-cur base=$tmp/ids-comments-base
  # 未追跡のファイルは母数から外す。禁止語の検査（scan・links）は公開物の「内容」を守るので、
  # まだ版管理に入っていないものも見る。一方ここは「総量が増えていないこと」を基準と突き合わせるもので、
  # 未追跡はまだ総量に入っていない。混ぜると、同じ版を調べても作業ツリーに置いてあるものしだいで結果が変わる。
  local skip=$tmp/ids-comments-untracked
  tr '\0' '\n' < "$tmp/untracked" | sed 's#^#./#' > "$skip"
  hits=$(cd "$work" && find . -type f \( -name '*_test.go' -o -name '*_test.mjs' \) -not -path '*/testdata/*' |
    { if [ -s "$skip" ]; then grep -vxF -f "$skip" || true; else cat; fi; } |
    sort | while IFS= read -r f; do
      data=$(sed -E 's#[A-Za-z][A-Za-z0-9+.-]*://##g; s#//.*##' "$f" | grep -oE -- "$ids_bare" | sort -u || true)
      grep -nE -- "$ids" "$f" | while IFS= read -r line; do
        c=$(printf '%s' "${line#*:}" | sed -E 's#[A-Za-z][A-Za-z0-9+.-]*://##g')
        rest=${c#*//}
        # // が無ければ ${c#*//} は元のまま返る = コメントの無い行（case は使わない。
        # bash 3.2 は $( … ) の中の case のパターンの ) を解釈できず、構文エラーになる）。
        if [ "$rest" = "$c" ]; then continue; fi
        c=$rest
        for id in $data; do c=${c//$id/}; done
        if printf '%s' "$c" | grep -qE -- "$ids"; then printf '%s:%s\n' "${f#./}" "$line"; fi
      done || true
    done || true)
  : > "$cur"
  if [ -n "$hits" ]; then
    printf '%s\n' "$hits" | cut -d: -f1 | sort | uniq -c | awk '{print $1" "$2}' > "$cur"
    n=$(printf '%s\n' "$hits" | wc -l | tr -d ' ')
    files=$(wc -l < "$cur" | tr -d ' ')
  fi
  # 調べる木に無いパスの基準は使わない（使い捨てのリポジトリや、--rev で古い版を調べるときに、
  # この一覧のせいで落ちないように）。ファイルごと消えた分は黙って空くが、公開物からも消えている。
  printf '%s\n' "$ids_comments_baseline" | awk 'NF==2 {print $1" "$2}' |
    while read -r bn bp; do
      if [ -f "$work/$bp" ]; then printf '%s %s\n' "$bn" "$bp"; fi
    done > "$base"
  total=$(awk '{s+=$1} END {print s+0}' "$base")
  echo "== 内部管理番号（テストの説明コメント）: $n 行・$files ファイル（基準 $total 行）"
  # 1 つめが空でも「どちらのファイルを読んでいるか」を取り違えないように、NR==FNR ではなく名前で見分ける。
  diffs=$(awk -v basef="$base" 'FILENAME == basef {b[$2]=$1; next} {c[$2]=$1}
    END {
      for (p in c) { k = (p in b) ? b[p] : 0; if (c[p] > k) printf "増えました（基準 %d → 実測 %d）: %s\n", k, c[p], p }
      for (p in b) { k = (p in c) ? c[p] : 0; if (k < b[p]) printf "減りました（基準 %d → 実測 %d）。public-scan.sh の ids_comments_baseline を下げてください: %s\n", b[p], k, p }
    }' "$base" "$cur" | sort)
  [ -z "$diffs" ] && return 0
  printf '%s\n' "$diffs"
  if [ "$summary" = 0 ]; then
    printf '%s\n' "$diffs" | sed -n 's/^増えました[^:]*: //p' | while IFS= read -r p; do
      printf '%s\n' "$hits" | grep -F -- "$p:" || true
    done
  fi
  return 1
}

status=0
if [ "$only" = all ] || [ "$only" = company ]; then scan "社内固有の語" "$company" || status=1; fi
if [ "$only" = all ] || [ "$only" = names ]; then scan "旧名" "$names" "$names_skip" case || status=1; fi
if [ "$only" = all ] || [ "$only" = python ]; then scan "Python の名残" "$python_left" "$python_skip" || status=1; fi
if [ "$only" = all ] || [ "$only" = ids ]; then
  scan "内部管理番号" "$ids" "$ids_skip" case || status=1
  ids_comments || status=1
fi
if [ "$only" = all ] || [ "$only" = links ]; then links || status=1; fi
if [ "$only" = all ] || [ "$only" = aiconf ]; then aiconf || status=1; fi
echo "== 調べた対象: $scanned"
exit $status
