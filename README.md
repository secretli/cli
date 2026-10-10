# secretli

The command-line client for [Secretli](https://secretli.app): share text and files through links that open once and expire, from a terminal. Everything is encrypted here, before it leaves your machine; the server stores ciphertext and never sees the key, which travels in the part of the link after `#`.

It speaks the same format as the web app, so links work in both directions: a link made here opens in the browser, and a link made in the browser opens here. The format is specified and implemented in [secretli/format](https://github.com/secretli/format).

```bash
secretli share                        # type or paste the secret, then Ctrl-D
pbpaste | secretli share -e 1h        # text from a pipe, gone after an hour
secretli share deploy.key notes.pdf   # files; -p adds a password, --reusable lets it open more than once
secretli open <link>                  # text to stdout, files to the current directory
secretli status <link>                # what a link points to, without opening it
secretli delete <owner-link>          # remove a secret for everyone
secretli send <link>                  # hand a link to another device with a code like 7-acid-rocket
secretli receive 7-acid-rocket        # open what another device sends with a code
```

## Install

With [Homebrew](https://brew.sh), on macOS or Linux:

```bash
brew install secretli/tap/secretli
```

`brew upgrade` picks up new releases, and shell completion for bash, zsh and fish comes along. The formula installs the same archives as below, which Homebrew checks against their SHA-256.

Without Homebrew, binaries for macOS, Linux and Windows are on the [releases page](https://github.com/secretli/cli/releases). Pick the archive for your system, check it, and put the binary on your PATH:

```bash
VERSION=v0.12.0; OS=darwin; ARCH=arm64   # OS: darwin or linux; ARCH: arm64 or amd64
BASE="https://github.com/secretli/cli/releases/download/${VERSION}"
curl -fsSLO "${BASE}/secretli_${VERSION}_${OS}_${ARCH}.tar.gz"
gh attestation verify "secretli_${VERSION}_${OS}_${ARCH}.tar.gz" --repo secretli/cli
mkdir -p secretli-cli && tar -xzf "secretli_${VERSION}_${OS}_${ARCH}.tar.gz" -C secretli-cli
sudo install secretli-cli/secretli /usr/local/bin/secretli
```

The verification step checks that the archive was built by this repository's release workflow from that tag; it needs the [GitHub CLI](https://cli.github.com/), logged in. Without it, compare against the release's `checksums.txt` instead:

```bash
curl -fsSLO "${BASE}/checksums.txt"
grep "secretli_${VERSION}_${OS}_${ARCH}.tar.gz" checksums.txt | shasum -a 256 -c -
```

On Windows, download the `.zip`, unpack it, and add the folder containing `secretli.exe` to your PATH.

With a Go toolchain:

```bash
go install github.com/secretli/cli/cmd/secretli@latest
```

For shell completion without Homebrew, `secretli completion zsh --help` (or `bash`, `fish`, `powershell`) shows the line to add to your shell's startup file. It completes commands, flags and `-e`'s lifetimes, and offers no files where a link or a code goes.

## Use

**Sharing.** Files are arguments; text comes from a pipe, from a prompt when you are at a terminal, or from `-t`. The prompt and the pipe keep the secret out of your shell history; `-t` does not. `-` shares stdin as a file named by `--name`:

```bash
pg_dump db | gzip | secretli share --name db.sql.gz
```

Links open once and expire after a day unless you say otherwise: `-e` takes `5m`, `10m`, `15m`, `1h`, `4h`, `12h`, `1d`, `3d` or `7d`, and `--reusable` lets a link open again and again until it expires. `-p` asks for a password; `--password-file` and `SECRETLI_PASSWORD` provide one without asking. `--qr` also draws the link as a QR code, for a phone to scan off the screen, and `-c` copies it to the clipboard.

Every share prints two links: the one to hand out, and the owner link, which can delete the secret and, for a reusable one, tells you whether it has been opened. Keep the owner link to yourself.

**Opening.** Text goes to stdout exactly as it was shared. Files are saved to the current directory or to `--out`, and nothing is overwritten unless you pass `--force`; `--stdout` streams a single file instead. Before anything is opened, the command says what the link points to. Opening a one-time secret uses it up, so for one of those it asks first, and Enter means no; `-y`/`--yes` opens it without asking. Reusable secrets open right away. Without an argument the link is read from stdin, or asked for, which keeps it out of your shell history too.

When a secret holds several files, `open` lists them with their sizes and asks which to save. Enter saves them all; numbers, ranges and names or patterns pick some, as in `2`, `1 3`, `1-2` or `*.jpg`, and a list of more than 30 files shows the first 30, while numbers and patterns reach them all. Of a one-time secret, the files you don't save are gone with it, and it says so before it asks. Only the files you pick have to be new in the directory, unless you pass `--force`. `--stdout` asks for the one file to write. With `--yes` or `--json`, or where there is no terminal, nothing is asked: every file is saved, and `--stdout` needs a secret of a single file.

`-c`/`--copy` puts text on the clipboard instead of the terminal, so it doesn't stay in the scrollback. The final line break is left off, so pasting a password doesn't also press Enter. 45 seconds later the clipboard is cleared, unless you copied something else by then. Before anything is opened, `--copy` checks that there is a clipboard tool (`pbcopy`, `wl-copy`, `xclip`, `xsel` or `clip.exe`) and that the secret is text; should copying still fail, the text is printed after all, so a one-time secret isn't lost.

**Handing a link over with a code.** Instead of copying a link to another device, `send` hands it over with a short code like `7-acid-rocket`. On the other device, type the code at the server's `/c` page (`secretli.app/c`) or run `secretli receive 7-acid-rocket`; `secretli share --code` makes a secret and hands it over in one go. The link travels encrypted, and only after the other device has proved it typed the same code; the words never leave the two devices. A code works once and for ten minutes, and a wrong code ends the transfer on both sides. Given an owner link, `send` hands over only the link to hand out. It works both ways with the web app's "Send with a code".

`receive` opens what it receives like `open` does, with the same `--out`, `--stdout`, `--force`, `--yes`, `--copy` and password options, or prints the link with `--link` (with `--copy`, it copies the link). If you decline to open a one-time secret, it prints the link instead, since the code is used up by then. Typing is forgiving: `7 acid rocket`, `7-ACID-ROCKET` and `7-aci-roc` all work.

**Checking and deleting.** `status` describes a secret from its link without opening it: text or files, one-time or reusable, password or not, when it was sent and when it expires, and whether it has been opened. Once a secret is gone, opened, deleted or expired, the server keeps nothing about it, so all `status` can say then is that it is gone, to the owner as to a recipient, as it does for a link to nothing. The exit code is 4 then, and with `--json` the state is `live` or `gone`. `delete` takes the owner link and asks before it deletes, unless you pass `--yes`.

## Scripts

stdout carries only the result, so `secretli share … | pbcopy` copies exactly the link; descriptions, the owner link and progress go to stderr. Questions are asked on the terminal, so a pipe on stdin doesn't get in the way. Where there is no terminal, as in CI, nothing is asked and a command that would need an answer fails instead: `delete` and opening a one-time secret need `--yes`, and `receive` needs `--yes` or `--link`, which it checks before it takes the code. `--json` prints results and errors as JSON, and `-q` prints only the result.

`send` and `share --code` print the code as their result as soon as there is one, then wait. With `--json` they print two lines: the code (`{"code": …, "code_expires_at": …}`, plus the new secret's fields for `share --code`), then `{"delivered": true}` once the link was handed over, or an error.

The exit code tells a script what happened:

| Code | Meaning |
|---|---|
| 0 | done |
| 1 | an error |
| 2 | wrong usage |
| 3 | a password is needed, or it is wrong; a transfer code did not match |
| 4 | the secret is gone or not found: opened, deleted, expired or no such link; a transfer code is unknown, used, expired or stopped |
| 5 | the server could not be reached, did not answer within a minute, or is limiting requests |

```bash
link=$(printf '%s' "$DB_PASSWORD" | secretli share -e 1h -q)
secretli status "$OWNER_LINK" --json | jq -r .state   # live or gone
```

## Your own server

New secrets go to `https://secretli.app` unless `--server` or `SECRETLI_SERVER` names another Secretli server. Opening, checking, deleting and `send` need no setting, since every link carries its server. A code does not, so `receive` asks the same server as `share`: pass `--server` there too when the code comes from another one.

## Development

```bash
make build   # bin/secretli
make test    # go test -race ./...
make lint
```

The tests run against a fake server that enforces the real one's rules on uploads, tokens, one-time secrets and short-code transfers. The questions are also tested at a real terminal, on Linux and macOS: those tests run the command in a pseudo-terminal, type the answers and Ctrl-C, and check what it shows, echo included. CI runs the same tests against the real server too, the latest published image, so the fake cannot drift from it; checks that look inside the fake are skipped there. CI also runs the whole of Secretli with each change, from [secretli/e2e](https://github.com/secretli/e2e): the server, the web app and the browser with this client, and a release is only made when that passes for its tag. To do that locally, start a server with raised rate limits (the tests send many requests from one address) and point the tests at it:

```bash
# Postgres and SeaweedFS from a checkout of secretli/server, then the server image
docker compose -f ../server/docker/docker-compose.yml up -d postgres seaweedfs createbucket
docker run -d --name secretli-test --network host -e RATE_LIMIT_MULTIPLIER=100 \
  -e DATABASE_URL='postgres://secretli:secretli@localhost:5432/secretli?sslmode=disable' \
  -e S3_ENDPOINT=http://localhost:8333 -e S3_BUCKET=secretli -e S3_ACCESS_KEY=admin -e S3_SECRET_KEY=admin \
  ghcr.io/secretli/server:main
SECRETLI_TEST_SERVER=http://localhost:8080 make test
```

On macOS, where `--network host` does not reach the host, run the server on the compose network instead and publish its port.

Releases are made by pushing an annotated tag such as `v0.12.0` on `main`. The release workflow builds the binaries and runs the whole of Secretli with the tag's client; only if that passes does it write `checksums.txt`, attest every file and create the GitHub release. Its last job writes the new formula to [secretli/homebrew-tap](https://github.com/secretli/homebrew-tap), except for pre-releases (tags with a dash). Afterwards, bump the version in the install commands above.

## License

MIT. The code words come from the [EFF short word list 2.0](https://www.eff.org/dice) by the Electronic Frontier Foundation, licensed [CC BY 3.0 US](https://creativecommons.org/licenses/by/3.0/us/).
