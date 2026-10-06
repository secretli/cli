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
```

## Install

Binaries for macOS, Linux and Windows are on the [releases page](https://github.com/secretli/cli/releases). Pick the archive for your system, check it, and put the binary on your PATH:

```bash
VERSION=v0.2.0; OS=darwin; ARCH=arm64   # OS: darwin or linux; ARCH: arm64 or amd64
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

For shell completion, `secretli completion zsh --help` (or `bash`, `fish`, `powershell`) shows the line to add to your shell's startup file.

## Use

**Sharing.** Files are arguments; text comes from a pipe, from a prompt when you are at a terminal, or from `-t`. The prompt and the pipe keep the secret out of your shell history; `-t` does not. `-` shares stdin as a file named by `--name`:

```bash
pg_dump db | gzip | secretli share --name db.sql.gz
```

Links open once and expire after a day unless you say otherwise: `-e` takes `5m`, `10m`, `15m`, `1h`, `4h`, `12h`, `1d`, `3d` or `7d`, and `--reusable` lets a link open again and again until it expires. `-p` asks for a password; `--password-file` and `SECRETLI_PASSWORD` provide one without asking. `--qr` also draws the link as a QR code, for a phone to scan off the screen, and `-c` copies it to the clipboard.

Every share prints two links: the one to hand out, and the owner link, which can delete the secret and tells you whether it was opened. Keep the owner link to yourself.

**Opening.** Text goes to stdout exactly as it was shared. Files are saved to the current directory or to `--out`, and nothing is overwritten unless you pass `--force`; `--stdout` streams a single file instead. Before anything is opened, the command says what the link points to, since opening a one-time secret uses it up. Without an argument the link is read from stdin, or asked for, which keeps it out of your shell history too.

**Checking and deleting.** `status` describes a secret from its link without opening it: one-time or reusable, password or not, when it was sent and when it expires, and whether it has been opened. For a secret that is gone it says what happened: opened and when, expired, or deleted. `delete` takes the owner link and asks before it deletes, unless you pass `--yes`.

## Scripts

stdout carries only the result, so `secretli share … | pbcopy` copies exactly the link; descriptions, the owner link and progress go to stderr. Nothing is asked when stdin is not a terminal; a command that would need an answer fails instead. `--json` prints results and errors as JSON, and `-q` prints only the result.

The exit code tells a script what happened:

| Code | Meaning |
|---|---|
| 0 | done |
| 1 | an error |
| 2 | wrong usage |
| 3 | a password is needed, or it is wrong |
| 4 | the secret is gone: opened, expired or deleted |
| 5 | the server did not answer, or is limiting requests |

```bash
link=$(printf '%s' "$DB_PASSWORD" | secretli share -e 1h -q)
secretli status "$OWNER_LINK" --json | jq -r .state   # live or gone
```

## Your own server

New secrets go to `https://secretli.app` unless `--server` or `SECRETLI_SERVER` names another Secretli server. Opening, checking and deleting need no setting, since every link carries its server.

## Development

```bash
make build   # bin/secretli
make test    # go test -race ./...
make lint
```

The tests run against a fake server that enforces the real one's rules on uploads, tokens and one-time secrets. Releases are made by pushing a tag such as `v0.2.0`; the release workflow builds the binaries, writes `checksums.txt`, attests every file and creates the GitHub release.

## License

MIT
