package loop

// pre-tool-secrets-guard: 資格情報を読む・表示する・git に入れる操作を、利用者に確認する（ask）。
//
//   - Bash: キーチェーン・パスワードストアの読み出し（security find-generic-password・secret-tool lookup ほか）
//   - Bash: 中身を出すコマンド（cat・less・head・grep・jq など）の引数が秘密のファイル
//   - Bash: git add / git commit の対象が秘密のファイル
//   - Read / Edit / Write: file_path が秘密のファイル
//
// 秘密のファイルは名前で見分ける（.env・credentials.json・鍵・.netrc など。.env.example のような雛形と
// 公開鍵（.pub）は除く）。判定は「実際に実行される語」だけを見るので、引用符やヒアドキュメントの中に
// 同じ語があっても反応しない（pre-tool-scope-guard と同じ作法）。
//
// コマンド名と引数（パス）で見る文字列を分ける。コマンド名は引用符の中を落とした文字列にだけ当てるので、
// `echo 'cat .env は禁止'` では鳴らない。パスは引用符の中も見るが、**引用符の組の中身がまるごと 1 つの
// 秘密のパスのときだけ**数える（quotedSecretPaths）。引用符はシェルの語の区切りを避けるために付くもので
// （空白を含むパス・$HOME の展開）、`cat "$HOME/.env"` は `cat ~/.env` と同じ行為だが、
// `git commit -m ".env を .gitignore に足した"` のような文は「実行される語」ではないため数えない。
//
// deny ではなく ask にする。秘密のファイルを開く正当な作業（設定を直す・鍵を入れ替える）はあるので、
// 通すかどうかは人が決める。例外は LOOPTRACK_LOOP_SECRETS_ALLOW（空白区切り。名前の一部に当たれば通す）。
//
// なぜ要るか: AI に実機の確認を任せると、認証情報の在りかを調べる過程でその中身を出力に残す事故が起きる
// （2026-09-20 に 2 回。キーチェーンの出力をそのまま表示した例と、使い捨てのトークンを出力に残した例）。
// 出力は会話の記録に残り、取り消せない。要るのは有無・長さ・ハッシュの先頭だけで、中身ではない。

import (
	"context"
	"regexp"
	"strings"

	"github.com/howashoji/looptrack/internal/client/hook/hookcmd"
	"github.com/howashoji/looptrack/internal/hookio"
	"github.com/howashoji/looptrack/internal/i18n"
)

// コマンド名の当て方の部品。
//
//   - cmdAnyPos: 語の位置に依らない前置（行頭・区切りの直後・空白の直後）。sudo や env を挟む形を拾う。
//   - cmdHeadPos: コマンドの位置だけの前置（行頭・区切りの直後と、前置の語の直後）。大小を区別しない照合に使う。
//     行頭に空白を許すのは、hookcmd の前置の語の段が語を空白に替えた後もコマンドの位置として読むため。
//   - cmdPath: コマンド名の前に付く実行ファイルのパス（/bin/cat・C:\Windows\System32\findstr.exe）。
//   - cmdEnd: コマンド名の後ろ（.exe を許し、語の終わりを求める）。RE2 に先読みは無いので区切りを 1 文字含めて数える。
//     \b ではなく区切りを求めるのは、`npm run copy:env` のような語（直後が :）で誤発火させないため。
const (
	cmdAnyPos  = hookcmd.CmdAnyPos
	cmdHeadPos = hookcmd.CmdHeadPos + cmdPrefixRun
	cmdPath    = hookcmd.CmdPath
	cmdEnd     = `(?:\.exe)?(?:[\s;&|)<>]|$)`
)

// cmdPrefixRun は「後ろにコマンドが続く語」（前置の語）とその選択肢の並び。この直後もコマンドの位置として扱う。
//
// 実測: xargs cat …（小文字）は確認になるのに xargs CAT … は素通りしていた。大小を区別しない照合は
// コマンドの位置だけに限ってあり（誤発火の歯止め）、その「位置」が行頭と区切りの直後しか無かったため。
// macOS では CAT がそのまま /bin/cat として実行され、中身が実際に出る。
//
// **限る**のは前置の語の直後だけ。引数の位置の語まで大小を無視すると
// `go test -run Type … .env` のような日常の操作で誤発火する。
//
// 語の一覧と選択肢の飲み方は hookcmd.PrefixRun（git ガード・待ちループのガードと共有する）。
// **限界**: 値を別の語で取る選択肢（sudo -u deploy CAT …）は前置として読み切れないので、そこは拾えない。
const cmdPrefixRun = hookcmd.PrefixRun

// showCmdWords は posix の「中身を出す・別の場所へ写す」コマンド。
//
// tar・zip・unzip は書庫に入れて持ち出す経路、curl・wget は外へ送る経路。どれも**秘密のパスが
// 引数に明示されているときだけ**鳴る（curl https://… 単体・tar cvf out.tar dir/ のような日常の使い方は通る）。
// ディレクトリごと固める形（tar czf out.tgz .）は秘密の名前が引数に現れないので、名前で見分ける方式では拾えない。
//
// source は中身を表示しないが、値がそのまま環境に入る（source .env）ので同じ扱いにする。
// `.`（ドット）は足さない。1 文字を語として正規表現で扱うと、どこで当たるかが読めず誤発火の予測が立たない
// （`. /p/.env` は捕まらない。rules の secrets-discipline.md に「捕まえないもの」として書いてある）。
const showCmdWords = `cat|bat|less|more|head|tail|strings|xxd|od|hexdump|grep|egrep|fgrep|rg|ag|jq|yq|openssl|` +
	`sed|awk|nl|tac|base64|dd|cp|mv|install|tee|rsync|scp|tar|zip|unzip|curl|wget|source`

// showCmdWinWords は Windows / PowerShell の同等品（PowerShell の別名を含む）。
const showCmdWinWords = `type|get-content|gc|select-string|sls|findstr|copy|copy-item|cpi|xcopy|robocopy|` +
	`format-hex|certutil|out-file`

// secretStoreRe は資格情報の保管庫を読み出すコマンド（macOS のキーチェーン・freedesktop の secret-tool・
// Windows の資格情報マネージャ）。語は長く紛れが無いので、位置に依らず大小も区別しない。
var secretStoreRe = regexp.MustCompile(cmdAnyPos + cmdPath +
	`(?i:security\s+(?:find-generic-password|find-internet-password|dump-keychain)|secret-tool\s+lookup|cmdkey\s+/list)\b`)

// showCmdRe は引数の中身を出すコマンド。これらの引数に秘密のファイルがあると、中身が出力に残る。
// 大小を区別する（(?i) を全体に掛けると `go test -run Type … .env` のような引数の位置の語で誤発火する）。
var showCmdRe = regexp.MustCompile(cmdAnyPos + cmdPath + `(?:` + showCmdWords + `)` + cmdEnd)

// showCmdHeadRe は同じコマンドを**コマンドの位置**でだけ、大小を区別せずに当てる（Windows のシェルは
// 大小を区別しないので TYPE・GET-CONTENT・CAT も拾う必要がある）。位置をコマンドに限るのが誤発火の歯止めで、
// これにより `go test -run Type … .env`（Type は引数の位置）や `npm run gc -- .env` は当たらない。
var showCmdHeadRe = regexp.MustCompile(cmdHeadPos + cmdPath + `(?i:` + showCmdWords + `|` + showCmdWinWords + `)` + cmdEnd)

// gitStageRe は git の追加・コミット（秘密のファイルを版管理に入れてしまう経路）。
var gitStageRe = regexp.MustCompile(cmdAnyPos + cmdPath + `(?i:git)(?:\.exe)?(?:\s+-[^\s]+)*\s+(?i:add|commit)\b`)

// secretFileRe は名前だけで秘密と分かるファイル（パスの最後の要素に当てる）。
//
// id_* は SSH の鍵の型だけに限る（id_token のような別物を秘密扱いしないため）。
// .pem・.key・.asc はここに入れない（利用者の決定 2026-09-21。公開の証明書鎖・署名・Keynote の書類のほうが
// 多く、無条件に秘密扱いすると誤発火が日常の操作で出る）。condSecretExtRe で条件つきに見る。
//
// 大小は区別しない。macOS と Windows のファイルシステムは大小を区別しないので、.ENV・ID_RSA・CREDENTIALS.JSON は
// 実在のファイルを指し、区別すると中身が実際に出てしまう（引数の位置の**コマンド名**を大小で区別するのとは別の話で、
// ここはパスの当て方）。
var secretFileRe = regexp.MustCompile(`(?i)^(?:\.env(?:\.[A-Za-z0-9_-]+)?|\.?credentials\.json|\.netrc|_netrc|\.pgpass|\.npmrc|\.htpasswd|` +
	`id_(?:rsa|dsa|ecdsa|ed25519|xmss)(?:_sk)?(?:[._-][A-Za-z0-9_-]+)?|.*\.(?:p12|pfx|p8|jks|keystore|gpg))$`)

// credsBareRe は拡張子の無い credentials（AWS の ~/.aws/credentials・gcloud の credentials）。
//
// **名前だけでは秘密扱いにしない。** credentials は英語のふつうの語で、grep の検索語（grep -rn credentials src/）や
// URL の一部（curl https://example.invalid/v1/credentials）として日常的に現れる。名前だけで秘密にすると
// 実測で 15 形の日常の操作が確認になり、誤発火が常態化してガードが形骸化する（塞いだ分より失うものが大きい）。
// そこで .pem / .key / .asc と同じ「条件つき」にして、**秘密の置き場（secretDirRe）の中にあるときだけ**秘密とする。
// credentials.json は従来どおり名前だけで秘密（拡張子まで揃った名前は資格情報以外に使われない）。
var credsBareRe = regexp.MustCompile(`(?i)^\.?credentials$`)

// condSecretExtRe は「秘密のことも公開のこともある」拡張子。置き場か名前で秘密と分かるときだけ秘密扱いする。
var condSecretExtRe = regexp.MustCompile(`(?i)\.(?:pem|key|asc)$`)

// secretDirRe は秘密の鍵を置く慣習のディレクトリ（パスの要素として当てる）。
// .ssh / .gnupg は鍵そのものの置き場、.aws はクラウドの認証情報の置き場、private / secrets / keys / pki /
// letsencrypt は TLS の秘密鍵を置く慣習の名前（/etc/ssl/private・/etc/letsencrypt/live）。
// 大小は区別しない（Private/・Secrets/・Keys/・.SSH/ のような大文字始まりの置き場も同じ慣習の名前）。
var secretDirRe = regexp.MustCompile(`(?i)(?:^|[/\\])(?:\.ssh|\.gnupg|\.aws|private|secrets|keys|pki|letsencrypt)[/\\]`)

// macPrivateRootRe は macOS の実パスの頭。/tmp・/var・/etc の実体は /private/tmp・/private/var・/private/etc なので、
// 実パス（realpath の出力・一時ディレクトリ）を書くと必ず先頭に private/ が現れ、置き場の判定が誤発火する
// （実測: /private/etc/ssl/cert.pem は実在する公開の CA 束なのに確認になった）。
// **先頭のこの 1 か所だけ**を置き場の判定から外す。/private の子はこの 3 つだけなので範囲が閉じ、
// 2 つ目以降の private/ は従来どおり当たる（/private/etc/ssl/private/server.key は確認する）。
//
// 大小は無視する。macOS のファイルシステムは大小を区別しないので /PRIVATE/TMP/x.pem は /private/tmp/x.pem と
// 同じ実体を指し、片方だけ確認にする意味が無い。
// **限界**: そのぶん、Linux で /Private/tmp/ や /private/etc/ を本当に秘密の置き場として作っている場合は拾えない。
// 判定に環境分岐（実行している OS で振る舞いを変えること）を入れない方針なので、この限界は受け入れる。
var macPrivateRootRe = regexp.MustCompile(`(?i)^/private/(?:tmp|var|etc)/`)

// inSecretDir はパス p が秘密の置き場にあるか（macOS の実パスの頭を外してから見る）。
func inSecretDir(p string) bool {
	return secretDirRe.MatchString(macPrivateRootRe.ReplaceAllString(p, "/"))
}

// pubStemRe は公開の証明書・署名に使う慣習の名前。
//
// **秘密の語より先には見ない**（先に見ると、cert・chain・bundle を 1 つ足すだけで private・secret・vault が
// 無効になり、client-cert.pem や secret.cert.pem のような本物の秘密が通ってしまう）。使い道は 2 つだけ:
//   - 拡張子が .key のとき: .key は鍵そのものを名乗る拡張子で、cert.key・tls-chain.key は TLS の秘密鍵なので
//     **公開の語も秘密の側に数える**（通るのは slides.key のような、どちらの語も持たない書類だけ）。
//   - 拡張子が .pem / .asc のとき: 公開の語があれば、重なりやすい語を秘密の語から降ろす。ただし**降ろすのは片方だけ**で、
//     public / pubkey があるときは key だけ（public-key は公開鍵）、ほかの公開の語（cert・chain・bundle…）のときは
//     ca だけ（ca-bundle は公開の CA 束）。列挙ではなく語の組み合わせで見るので ca-bundle-2026 のような変形にも効く。
//     private・secret・vault は降りない。
var pubStemRe = regexp.MustCompile(`(?i)(?:^|[._-])(?:fullchain|chain|bundle|intermediate|certificate|cert|cacert|pubkey|public)(?:$|[._-])`)

// pubKeyStemRe は「公開鍵」を名乗る語。key を秘密の語から降ろしてよいのは、この語があるときだけ。
var pubKeyStemRe = regexp.MustCompile(`(?i)(?:^|[._-])(?:pubkey|public)(?:$|[._-])`)

// keyStemRe は秘密を名乗る語（拡張子を除いた部分の中に、語として現れるもの）。
// 語の境界（先頭・末尾・. _ -）で見るので、design（sign）・pubkey（key）・cacert（ca）のような
// 語の途中の一致では当たらない。
var keyStemRe = regexp.MustCompile(`(?i)(?:^|[._-])(?:` + keyStemWords + `|ca|key)(?:$|[._-])`)

// keyStemWords は秘密を名乗る語のうち、公開の名前と重ならないもの（ca と key を除いたもの）。
const keyStemWords = `server|client|host|domain|site|privkey|private|priv|tls|ssl|id|` +
	`secret|master|encryption|encrypted|encrypt|rootca|root|signing|sign|deploy|vault|backup|token|credentials?`

// keyStemNoCaRe は ca だけを降ろしたもの（cert・chain・bundle などの公開の語があるときに使う）。
// key は降ろさない。key-cert.pem・ca-key-bundle.pem のような**本物の秘密鍵**を通さないため。
var keyStemNoCaRe = regexp.MustCompile(`(?i)(?:^|[._-])(?:` + keyStemWords + `|key)(?:$|[._-])`)

// keyStemNoKeyRe は key だけを降ろしたもの（public・pubkey があるときに使う。public-key は公開鍵）。
// ca は降ろさない。pubkey-ca-key.pem のような CA の鍵を通さないため。
var keyStemNoKeyRe = regexp.MustCompile(`(?i)(?:^|[._-])(?:` + keyStemWords + `|ca)(?:$|[._-])`)

// secretFileAllowRe は名前が似ていても秘密でないもの（雛形・公開鍵）。
// 秘密の側（secretFileRe・condSecretExtRe）と同じく大小を区別しない。片方だけ大小無視にすると
// .ENV.EXAMPLE や KEY.PUB が秘密扱いになり、誤発火が増える。
var secretFileAllowRe = regexp.MustCompile(`(?i)^(?:\.env\.(?:example|sample|template|dist|defaults)|.*\.pub)$`)

// pubKeyWordRe は「まるごと公開鍵を名乗る語」（pubkey・publickey・pubkeys・publickeys・pubkeyring）。
// compoundKeyStem が合成語を除く条件に使う。当てる相手は . _ - で分けた後の 1 語なので、
// この正規表現に区切りは書かない（書いても構造上一致しない）。
var pubKeyWordRe = regexp.MustCompile(`(?i)^pub(?:lic)?key(?:s|ring)?$`)

// compoundKeyStem は、区切りの無い合成語として key を含む語があるか（privatekey・keypair・sshkey・apikey）。
//
// 語の境界（. _ -）だけで見ると privatekey は 1 語なので private にも key にも当たらない。
// privatekey.pem は「秘密鍵」そのものの慣習的な名前で、keypair.pem は鍵対の既定の名前なので、
// 条件つきの拡張子（.pem・.key・.asc）に限って合成語も見る。
// 除くのは**その語がまるごと公開鍵を名乗る形**（pubKeyWordRe）のときだけ。
// 「pub で始まる」で除くと、pubprivatekey・pubsecretkey のような 1 語の秘密鍵が通ってしまう
// （区切りのある pubkey-privatekey は当たるのに、区切りを外すだけで結果が変わっていた）。
//
// demoteBareKey は、key を降ろす枝（public / pubkey がある名前）で使う。そこでは key・keys の**単独の語**は
// 公開鍵の一部なので数えないが（public-key・public-keys）、合成語は数える
// （public-privatekey・pubkey-sshkey は秘密鍵）。
func compoundKeyStem(stem string, demoteBareKey bool) bool {
	for _, w := range strings.FieldsFunc(stem, func(r rune) bool { return r == '.' || r == '_' || r == '-' }) {
		l := strings.ToLower(w)
		if !strings.Contains(l, "key") || pubKeyWordRe.MatchString(l) {
			continue
		}
		if demoteBareKey && (l == "key" || l == "keys") {
			continue
		}
		return true
	}
	return false
}

// globTailRe は basename の末尾に連続するグロブの文字（* ・ ? ・ […]）。
var globTailRe = regexp.MustCompile(`(?:\*|\?|\[[^\]]*\])+$`)

// secretGlobPath は p の basename が、末尾のグロブ（*・?・[…]）を落とした残りで
// 名前だけで秘密と分かるもの（secretFileRe）に丸ごと一致するか。
//
// 利用者の決定（2026-09-21）: **名前が見えているグロブ**（`.env*`・`id_rsa*`）だけを拾う。
// **名前を伏せるグロブ**（`.e*`・`*`）は捕まえない（rules の secrets-discipline.md に明記）。
// `.env*` は `.env.example` も含むので確認する側へ倒れるが、これは許容する（同じ決定）。
//
// .pem・.key・.asc の条件つきの拡張子（condSecretExtRe）と、拡張子の無い credentials
// （credsBareRe。置き場が要る）には広げない。誤発火が増えるため（同じ決定）。
func secretGlobPath(p string) bool {
	p = fileURLRe.ReplaceAllString(p, "")
	b := p
	if i := strings.LastIndexAny(b, `/\`); i >= 0 {
		b = b[i+1:]
	}
	if !globTailRe.MatchString(b) {
		return false
	}
	head := globTailRe.ReplaceAllString(b, "")
	if head == "" || secretFileAllowRe.MatchString(head) {
		return false
	}
	return secretFileRe.MatchString(head)
}

// fileURLRe は file:// の前置。手元のパスの別の書き方なので、外してから同じように判定する
// （file:///p/.ssh/id_rsa は /p/.ssh/id_rsa として見る。curl file:///etc/passwd は中身を出す）。
//
// 前置を外すのは、名前を見るためだけではない。外さないと macOS の実パスの除外（macPrivateRootRe は
// 先頭の /private/{tmp,var,etc}/ に当てる）が効かず、file:///private/etc/ssl/cert.pem（公開の CA 束）で
// 誤発火する（実測）。
//
// **遠隔の scheme（https:// など）は除外しない。** URL の中の path を一律に「手元のパスではない」として
// 落とすと、遠隔の秘密を手元のディスクへ落とす行為が黙る（実測: aws s3 cp s3://bucket/.env /tmp/・
// scp scp://user@host/…/id_rsa /tmp/・cat sftp://host/.ssh/id_rsa が素通りし、同じ行為でも
// scp user@host:…/id_rsa は確認、という食い違いになった）。URL のパスに credentials があるだけの形は、
// 拡張子の無い credentials を置き場の中だけに狭めてあるので、除外が無くても鳴らない。
var fileURLRe = regexp.MustCompile(`(?i)^file://`)

// secretPath はパス p が秘密を持つファイルか。
//
// .pem・.key・.asc は、置き場（inSecretDir）か名前（keyStemRe）で秘密と分かるときだけ秘密扱いする。
// 名前は秘密の語（keyStemRe）で見る。公開の語（pubStemRe）は、.key では秘密の側に数え、
// .pem / .asc では ca と key の 2 語だけを降ろすのに使う（pubStemRe の注釈を見よ）。
// 末尾にグロブ（*・?・[…]）が付いていても、それを落とした残りが名前だけで秘密と分かれば秘密扱いする
// （secretGlobPath。`.env*`・`id_rsa*` のように名前が丸ごと見えているときだけで、`.e*`・`*` のように
// 名前を伏せる形は拾わない）。
// **限界**: 慣習から外れた名前の秘密鍵を慣習から外れた置き場に置くと（例: backup/2026.pem）拾えない。
// 名前で見分ける方式の限界なので、rules の secrets-discipline.md に明記してある。
func secretPath(p string) bool {
	p = fileURLRe.ReplaceAllString(p, "")
	b := p
	if i := strings.LastIndexAny(b, `/\`); i >= 0 {
		b = b[i+1:]
	}
	if b == "" || secretFileAllowRe.MatchString(b) {
		return false
	}
	if secretFileRe.MatchString(b) {
		return true
	}
	if secretGlobPath(p) {
		return true
	}
	if credsBareRe.MatchString(b) {
		return inSecretDir(p)
	}
	if !condSecretExtRe.MatchString(b) {
		return false
	}
	stem := b
	if i := strings.LastIndexByte(stem, '.'); i > 0 {
		stem = stem[:i]
	}
	if inSecretDir(p) {
		return true
	}
	if strings.HasSuffix(strings.ToLower(b), ".key") {
		// .key は鍵そのものを名乗る拡張子。公開の語（cert・chain・bundle…）も TLS の鍵の名前なので秘密の側。
		return keyStemRe.MatchString(stem) || pubStemRe.MatchString(stem) || compoundKeyStem(stem, false)
	}
	// 公開の語があるときだけ、重なりやすい語を秘密の語から降ろす。**降ろすのは片方だけ**。
	// key を降ろす正当な理由があるのは public / pubkey のときだけで（public-key は公開鍵）、
	// cert・chain・bundle で降ろすと key-cert.pem・ca-key-bundle.pem（CA の秘密鍵）が通ってしまう。
	if pubKeyStemRe.MatchString(stem) {
		return keyStemNoKeyRe.MatchString(stem) || compoundKeyStem(stem, true)
	}
	if pubStemRe.MatchString(stem) {
		return keyStemNoCaRe.MatchString(stem) || compoundKeyStem(stem, false)
	}
	return keyStemRe.MatchString(stem) || compoundKeyStem(stem, false)
}

// secretsAllowed は LOOPTRACK_LOOP_SECRETS_ALLOW の語のどれかが s に含まれるか（例外の指定）。
func secretsAllowed(s string, getenv func(string) string) bool {
	for _, w := range strings.Fields(getenv("LOOPTRACK_LOOP_SECRETS_ALLOW")) {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

// secretPathsIn は run（引用符の中を落とした文字列）と quoted（引用符で囲まれたパス）に現れる
// 秘密のファイルのパス（出現順・重複なし）。allowed に当たるパスは除く。
//
// 例外（LOOPTRACK_LOOP_SECRETS_ALLOW）は**パスごとに**見る。コマンド全体で見て 1 つでも当たれば
// 素通りさせると、許可したパスと許可していないパスを 1 行で扱ったときに（`cat <許可>/.env <許可外>/.env`）
// 許可外の秘密まで一緒に抜けてしまう。
func secretPathsIn(run string, quoted []string, allowed func(string) bool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(w string) {
		if secretPath(w) && !seen[w] && !allowed(w) {
			seen[w] = true
			out = append(out, w)
		}
	}
	for _, w := range wordsIn(run) {
		add(w.text)
	}
	for _, w := range quoted {
		add(w)
	}
	return out
}

// wordAt は語と、run の中でのその位置。
type wordAt struct {
	text string
	at   int
}

// isWordSep は語の区切り（引用符も区切りに数えるので、引用符つきのパスも 1 語になる）。
func isWordSep(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '=', ';', '|', '&', '(', ')', '<', '>', '"', '\'':
		return true
	}
	return false
}

// wordsIn は run を語に分け、それぞれの位置を添えて返す（区切りはすべて ASCII なので、
// UTF-8 の文字の途中で切れることはない）。
func wordsIn(run string) []wordAt {
	var out []wordAt
	start := -1
	for i := 0; i <= len(run); i++ {
		if i < len(run) && !isWordSep(run[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out = append(out, wordAt{text: run[start:i], at: start})
			start = -1
		}
	}
	return out
}

// copyCmdRe / copyCmdHeadRe は複写のコマンド（雛形から作る操作を通すかどうかの判断にだけ使う）。
// 位置と大小の扱いは showCmdRe / showCmdHeadRe と同じ。
var copyCmdRe = regexp.MustCompile(cmdAnyPos + cmdPath + `(?:cp|install)` + cmdEnd)
var copyCmdHeadRe = regexp.MustCompile(cmdHeadPos + cmdPath + `(?i:cp|install|copy|copy-item|cpi|xcopy)` + cmdEnd)

// lineContRe は行末の継続（バックスラッシュ + 改行）と、コマンドの末尾に残るバックスラッシュ。
//
// シェルはこの 2 文字を取り除いて行をつなぐが、取り除かずに判定すると語が `.env\` になり、
// secretPath の basename の切り出し（LastIndexAny(b, "/\\")）が末尾のバックスラッシュを区切りとして拾って
// basename が空になる（＝必ず「秘密ではない」と答える）。長いコマンドを読みやすく折り返しただけで
// ガードが黙るので、シェルと同じ規則でつないでから判定する。
//
// 取り除くのは**改行（か文字列の終わり）が直後に来るバックスラッシュだけ**。Windows のパスの区切り
// （C:\tmp\x.txt）は後ろに改行が無いので壊れない。
var lineContRe = regexp.MustCompile(`\\(?:\r?\n|$)`)

// runSegments は引用符の外の区切り（; & | 改行 かっこ）でコマンド文字列を単純コマンドに分ける。
// 引用符の中の区切りでは切らない。閉じていない引用符があるときは ok = false（呼ぶ側は分けずに扱う）。
//
// 雛形からの複写を通す判断（templateCopy）は、**この単位ごと**に行う。行の全体で判断すると、
// `cp .env.example /tmp/t && cat ~/.ssh/id_rsa` のように、複写と無関係な秘密の読み出しまで一緒に通ってしまう。
//
// 行末のコメント（語の先頭の # から行末まで）は、引用符の数えに入れない。`cp .env.example .env  # don't …`
// のようにコメントの中に ' があると、閉じていない引用符と誤認して雛形の例外が使われず、日常の複写が確認に
// 倒れていた（引用符の外で # が語の先頭に来たときだけコメントとして飛ばす。引用符の中の # はそのまま数える
// ので `echo '# don't'` のような形を誤って特別扱いしない）。
func runSegments(raw string) ([]string, bool) {
	var out []string
	var q byte
	start := 0
	for i := 0; i < len(raw); {
		c := raw[i]
		if q == 0 && c == '#' && (i == 0 || isRunWordBreak(raw[i-1])) {
			nl := strings.IndexByte(raw[i:], '\n')
			if nl < 0 {
				break // コメントが文字列の終わりまで続く。その後ろに区切りは無い
			}
			i += nl // 次に読む位置は改行そのもの（改行は区切りとしてふだんどおり扱う）
			continue
		}
		if q != 0 {
			if c == q {
				q = 0
			}
			i++
			continue
		}
		switch c {
		case '\'', '"':
			q = c
		case ';', '&', '|', '\n', '(', ')':
			out = append(out, raw[start:i])
			start = i + 1
		}
		i++
	}
	if q != 0 {
		return []string{raw}, false
	}
	return append(out, raw[start:]), true
}

// isRunWordBreak は、その直後に # が来たときにコメントの始まりと認めてよい文字（空白と区切り）。
// runSegments でだけ使う（skipInert の isWordBreak と同じ考え方だが、対象の区切りの並びが違うので別に持つ）。
func isRunWordBreak(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', ';', '&', '|', '(', ')':
		return true
	}
	return false
}

// templateCopy は「雛形から秘密のファイルを作る」複写か（利用者の決定 2026-09-21。cp .env.example .env は通す）。
// **単純コマンド 1 つ**に対して呼ぶ（runSegments で分けた単位）。
//
// 通すのは **複写元が雛形（secretFileAllowRe）に当たり、かつそのコマンドの秘密のパスがそれ 1 つだけ**のときだけ。
// 「複写元」は、雛形の語が秘密のパスより**前**に現れることで見る。これにより次はいずれも確認のまま:
//   - cp .env .env.bak    （元も先も秘密。元が読まれる。秘密のパスが 2 つ）
//   - mv .env /tmp/       （複写のコマンドではない。外へ出る）
//   - tee .env < .env.example（複写のコマンドではない。秘密が引数でなくリダイレクトの先）
//   - cp .env /tmp/.env.example（雛形の名前で外へ写す。雛形の語が複写元の位置に無い）
//   - cp .env.example .env; cat .env（別の単純コマンド。そちらは通常どおり判定する）
func templateCopy(run string, paths []string) bool {
	if len(paths) != 1 {
		return false
	}
	if !copyCmdRe.MatchString(run) && !copyCmdHeadRe.MatchString(run) {
		return false
	}
	dst := -1
	for _, w := range wordsIn(run) {
		if w.text == paths[0] {
			dst = w.at
			break
		}
	}
	if dst < 0 {
		return false
	}
	for _, w := range wordsIn(run) {
		if w.at >= dst {
			break
		}
		b := w.text
		if i := strings.LastIndexAny(b, `/\`); i >= 0 {
			b = b[i+1:]
		}
		if secretFileAllowRe.MatchString(b) {
			return true
		}
	}
	return false
}

// quotedSecretPaths は引用符の組のうち、中身がまるごと 1 つの秘密のパスのもの（cat '/p/.env'・
// Get-Content "C:\Users\My Name\proj\.env"）。
//
// 引用符の中身を落とすと（hookcmd.StripQuotes）引用符で囲むだけでガードを抜けられるが、中身を丸ごと
// 語として数えると `echo 'cat .env は禁止'` の類で誤発火する。中身がそれ自体で秘密のパスのときだけ数えて
// 両立させる（文の中に .env が現れても、文の末尾がパスの形でない限り当たらない）。
// 組の取り方は hookcmd.StripQuotes と同じ（先に ' '、次に残りへ " "）。
func quotedSecretPaths(text string) []string {
	var out []string
	for _, q := range []byte{'\'', '"'} {
		var rest strings.Builder
		s := text
		for {
			i := strings.IndexByte(s, q)
			if i < 0 {
				break
			}
			j := strings.IndexByte(s[i+1:], q)
			if j < 0 {
				break
			}
			if inner := strings.TrimSpace(s[i+1 : i+1+j]); secretPath(inner) {
				out = append(out, inner)
			}
			rest.WriteString(s[:i])
			s = s[i+1+j+1:]
		}
		rest.WriteString(s)
		text = rest.String()
	}
	return out
}

// redirectInRe は入力のリダイレクト（`< パス`）。表示のコマンドを伴わなくても、中身はその処理系へ渡る
// （node app.js < .env）。
//
// 対象外にするもの: ヒアドキュメント（<<）・ヒアストリング（<<<）・ファイル記述子の複製（<&3）・
// 出力のリダイレクト（>）。直前の 1 文字が < でないことと、続く語に < & ( 引用符を許さないことで分ける。
var redirectInRe = regexp.MustCompile(`(?:^|[^<])<\s*([^\s;&|()<>'"]+)`)

// redirectsSecret は seg（引用符の中を落とした単純コマンド）に、秘密のファイルからの入力のリダイレクトがあるか。
func redirectsSecret(seg string, allowed func(string) bool) bool {
	for _, m := range redirectInRe.FindAllStringSubmatch(seg, -1) {
		if secretPath(m[1]) && !allowed(m[1]) {
			return true
		}
	}
	return false
}

// PreToolSecretsGuard は `looptrack hook pre-tool-secrets-guard`。
func PreToolSecretsGuard(ctx context.Context, ev hookio.Event) (hookio.Result, error) {
	e := envFrom(ctx)
	if ev.Tool == nil {
		return hookio.Result{}, nil
	}
	if v := ev.Raw["tool_input"]; truthy(v) {
		if _, isMap := v.(map[string]any); !isMap {
			return hookio.Result{}, nil
		}
	}
	lang := e.lang()
	note := i18n.T(lang, "loop.secrets.note")

	switch ev.Tool.Kind {
	case hookio.KindRead, hookio.KindEdit, hookio.KindWrite:
		p := ev.Tool.FilePath()
		if p == "" || !secretPath(p) || secretsAllowed(p, e.env) {
			return hookio.Result{}, nil
		}
		return hookio.Result{Ask: i18n.T(lang, "loop.secrets.open_file", "path", p) + note}, nil
	case hookio.KindBash:
	default:
		return hookio.Result{}, nil
	}

	cmd := toStr(toolInput(ev)["command"])
	if cmd == "" {
		return hookio.Result{}, nil
	}
	// 行末の継続（バックスラッシュ + 改行）をシェルと同じようにつないでから判定する。
	cmd = lineContRe.ReplaceAllString(cmd, "")
	// 判定に掛ける文字列は hookcmd の共通の段で作る（git ガードと同じものを呼ぶ）。
	// 入れ子のシェルをほどき、前置の語（sudo・env・xargs …）を落とす。
	// 位置を問わない（hookcmd.AnyPos）のは、`ask` は人がその場で通せるので、広く当てて
	// 取りこぼしを減らすほうがよいため（git ガードは `deny` なのでコマンドの位置だけ）。
	cmd = hookcmd.Normalize(cmd, hookcmd.AnyPos)
	// コマンド名は「実際に実行される語」だけを見る（引用符・ヒアドキュメントの中の同じ語では反応しない）。
	run := hookcmd.CommandText(cmd, true)
	if run == "" {
		return hookio.Result{}, nil
	}
	if m := secretStoreRe.FindString(run); m != "" {
		// 保管庫の読み出しには見るべきパスが無いので、例外はコマンド全体で見る。
		if secretsAllowed(run, e.env) {
			return hookio.Result{}, nil
		}
		return hookio.Result{Ask: i18n.T(lang, "loop.secrets.store", "match", strings.TrimSpace(m)) + note}, nil
	}
	// パスは引用符の中も見る（ヒアドキュメントの本文だけ落とす）。数えるのは中身がパスそのものの組だけ。
	// 単純コマンドごとに数え、雛形からの複写であるコマンドだけを除く（ほかのコマンドは通常どおり判定する）。
	allowed := func(p string) bool { return secretsAllowed(p, e.env) }
	segs, split := runSegments(hookcmd.CommandText(cmd, false))
	var paths []string
	redirect := false
	seen := map[string]bool{}
	for _, seg := range segs {
		segRun := hookcmd.StripQuotes(seg)
		if redirectsSecret(segRun, allowed) {
			redirect = true
		}
		segPaths := secretPathsIn(segRun, quotedSecretPaths(seg), allowed)
		if split && templateCopy(segRun, segPaths) {
			continue
		}
		for _, p := range segPaths {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	if len(paths) == 0 {
		return hookio.Result{}, nil
	}
	list := strings.Join(paths, i18n.T(lang, "loop.secrets.sep"))
	if gitStageRe.MatchString(run) {
		return hookio.Result{Ask: i18n.T(lang, "loop.secrets.git_stage", "list", list)}, nil
	}
	if redirect || showCmdRe.MatchString(run) || showCmdHeadRe.MatchString(run) {
		return hookio.Result{Ask: i18n.T(lang, "loop.secrets.show", "list", list) + note}, nil
	}
	return hookio.Result{}, nil
}
