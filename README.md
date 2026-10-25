# Procly

Save commands and manage running processes from a terminal dashboard or the command line. Procly currently supports Windows.

## Get started

When a Windows build is available on [Releases](https://github.com/Sny4m/Procly/releases), extract it and put `procly.exe` in a folder on your `PATH`. Until the first release is published, build from source. Install the Go version specified in [go.mod](go.mod), then run:

```powershell
go build -o procly.exe ./cmd/procly
.\procly.exe
```

Run `procly` to open the dashboard. Press `a` to add a command, or run `procly add` for guided setup in your terminal. Both use the same fields and defaults:

- **Name:** a short name such as `api`.
- **Command:** the full command, including arguments, such as `node server.js`.
- **Mode:** `direct` for executables; `shell` for npm scripts, pipes, or `&&`. Shell mode uses Windows cmd.exe.
- **Working directory:** the folder where the command runs. Uses `default-dir` when configured, otherwise your current folder.
- **Required environment variables:** comma-separated names. Values come from the environment and are never saved.
- **Restart:** `never`, `on-failure`, or `always`. Automatic restarts stop after five retries.

Review before saving. In terminal setup, Enter keeps the displayed value and `-` clears it. In the dashboard, Shift+Tab goes back and Esc cancels.

## Save and run commands

```powershell
procly add api "node server.js"
procly add web --shell "npm run dev"
procly api
```

For precise argument boundaries, use `--`. Put Procly options before it:

```powershell
procly add api --dir C:\projects\api -- node server.js --port 3000
```

| Command | Action |
| --- | --- |
| `procly` | Open the dashboard |
| `procly add [NAME]` | Open guided setup |
| `procly list` | List saved commands |
| `procly NAME` or `procly run NAME` | Run a command in this terminal |
| `procly edit NAME` | Edit a command with guided setup |
| `procly remove NAME` | Remove a saved command |
| `procly info NAME` | Show command settings |
| `procly ps` | Show processes in the active session |
| `procly stop NAME` | Stop a running command |
| `procly restart NAME` | Restart a command |
| `procly logs NAME -f` | Follow live output |
| `procly logs NAME` | Read saved output |
| `procly history [NAME]` | Show previous runs |
| `procly settings [KEY VALUE]` | View or change settings |
| `procly update` | Install an available signed update |

Shortcuts `ls`, `rm`, `save`, `start`, and `config` remain available. Use `procly COMMAND --help` for options. If a saved name matches a built-in command, use `procly run NAME`.

A dashboard or foreground run owns its processes. Keep that session open; use another terminal for `ps`, `stop`, `restart`, and live logs. Only one session can run per data directory. Ctrl+C stops a foreground run; exiting the dashboard stops its processes.

## Groups

Create a group and launch its commands together:

```powershell
procly group create dev api web
procly group run dev
procly group stop dev
```

Use `procly group add dev NAME` to add more commands, `procly group list` to view groups, and `procly group remove dev` to remove one. Running a group opens the dashboard if no session is active.

## Schedules

Schedules run only while the dashboard is open. Missed runs are not replayed when you reopen it.

```powershell
procly schedule add backup --run backup --at "2026-11-01 21:00"
procly schedule add worker --run worker --every 30m
procly schedule list
procly schedule disable worker
procly schedule cancel worker
```

Dates use local time unless you supply an RFC3339 timezone. Dashboard timing accepts `every 30m` or `at YYYY-MM-DD HH:MM`, with the same formats and validation as the CLI.

## Dashboard keys

| Key | Action |
| --- | --- |
| Up / Down | Select a row |
| Enter | View details |
| a / e | Add / edit a command |
| r / s | Run or restart / stop |
| l | View logs |
| / | Search |
| Tab | Switch sections |
| ? | Show help |
| Esc | Go back |
| q | Quit |

## Development and releases

```powershell
gofmt -w cmd internal
go vet ./...
go test -race -count=1 -timeout=3m ./...
go build ./cmd/procly
```

CI checks formatting, runs vet and tests, and builds Windows AMD64 and ARM64 binaries. Pushing a `v*` tag runs the same checks, verifies the configured release source and signing key, then creates a draft release with signed checksums. Configure `PROCLY_SIGNING_KEY` in the release environment. Review the draft before publishing.

`procly update --check` checks for updates without installing. Updates require the official source in `internal/release/source.json` and a release signed with its configured key.

If no stable release has been published, the updater reports that and leaves your installation unchanged. After an update, run `procly update --status` to confirm installation. Windows completes the replacement after the update command exits and restores the previous executable if validation fails.

## License

[MIT](LICENSE)
