
```text
                         _
 _ __  _ __ ___   ___  | |_   _
| '_ \| '__/ _ \ / __| | | | | |
| |_) | | | (_) | (__  | | |_| |
| .__/|_|  \___/ \___| |_|\__, |
|_|                       |___/
```

# Procly

**Run it. Track it. Control it.**

Procly is a lightweight terminal process manager built with Go. It lets you save commands, run multiple processes, monitor their activity, and manage everything from one clean terminal dashboard.

No more juggling terminal windows just to keep your development environment running.

Built for **Windows**, with Linux and macOS support planned.

## ✨ Features

- 🖥️ **Interactive Dashboard** — Manage processes from a clean, keyboard-driven terminal interface.
- ⚡ **Command Aliases** — Save frequently used commands and run them with a short name.
- 🔄 **Process Management** — Start, stop, restart, and monitor processes without opening additional terminal windows.
- 📋 **Live Logs** — View output in real time, search logs, and inspect previous runs.
- 📁 **Workspaces** — Group related commands and launch them together.
- ⏰ **Scheduling** — Schedule commands to run at specific times or intervals while Procly is open.
- 🔁 **Auto-Restart** — Automatically restart failed processes using configurable restart policies.
- 📊 **Process Monitoring** — Check process status, uptime, and memory usage.
- 📦 **Self-Updates** — Check for new versions and update directly from GitHub Releases.

## 💡 Why I Built Procly

While working on different projects, I often found myself opening several terminal windows just to keep things running.

One for a development server, another for a background worker, and sometimes a few more for other tasks.

It worked, but keeping track of everything became annoying. I'd forget which terminal was running what, waste time switching between windows, or have to restart commands I'd already typed countless times.

I originally built a small Python utility called **Termy** to make this easier.

Procly is a complete rebuild of that idea in Go, with a bigger focus on performance, reliability, and user experience.

I wanted something simple: one place to save my commands, launch them, see what's running, and shut everything down when I'm done.

That's what Procly is built for.

## 🚀 Getting Started

### Requirements

- Windows 11 (recommended)
- Go 1.27.1 or newer (for building from source)

### Build from Source

Clone the repository and run:

```powershell
go mod download
go build -o procly.exe ./cmd/procly
.\procly.exe
```

You can move the compiled executable to a directory in your `PATH` to use `procly` from anywhere.

### Your First Command

Save a command:

```bash
procly add api -- node server.js
```

Run it:

```bash
procly api
```

For npm scripts on Windows, use shell mode:

```bash
procly add web --shell -- "npm run dev"
procly web
```

Or simply launch the dashboard:

```bash
procly
```

From there, you can manage multiple commands without opening separate terminal windows.

## 🛠️ Commands

| Command | Description |
|---------|-------------|
| `procly` | Open the interactive dashboard |
| `procly add` | Add a new command |
| `procly list` | List saved commands |
| `procly NAME` | Run a saved command |
| `procly edit NAME` | Edit a saved command |
| `procly remove NAME` | Remove a command |
| `procly ps` | View active processes |
| `procly stop NAME` | Stop a process |
| `procly restart NAME` | Restart a process |
| `procly logs NAME -f` | Follow live logs |
| `procly history` | View previous runs |
| `procly update` | Check for and install updates |

Use `procly help` to explore all available commands and options.

### 📁 Workspaces

Working on a project with multiple services?

Group them together:

```bash
procly group create dev
procly group add dev api web
procly group run dev
```

Stop the entire workspace:

```bash
procly group stop dev
```

### ⏰ Scheduling

Schedule a command for a specific time:

```bash
procly schedule add backup --at "2026-11-01 21:00" --run backup
```

Or run a command at regular intervals:

```bash
procly schedule add worker --every 30m --run worker
```

View schedules:

```bash
procly schedule ls
```

**Note:** Schedules only execute while the Procly dashboard is running.

## ⌨️ Keyboard Shortcuts

Procly's dashboard is designed to be navigated entirely from your keyboard.

| Key | Action |
|-----|--------|
| `↑` / `↓` | Navigate |
| `Enter` | View details |
| `a` | Add a command |
| `e` | Edit a command |
| `r` | Run or restart |
| `s` | Stop a process |
| `l` | View logs |
| `/` | Search |
| `Tab` | Switch sections |
| `?` | Show help |
| `Esc` | Go back |
| `q` | Quit |

When you exit Procly, it asks for confirmation before stopping active processes.

## 🔄 Updating Procly

Procly includes a built-in updater.

Check for updates:

```bash
procly update --check
```

Download and install an available release:

```bash
procly update
```

Updates are fetched from the official GitHub repository and verified before installation.

**Note:** Online updates require the official repository source to be configured and signed releases to be published.

## 🧰 Built With

- **Go** — Core application and process management
- **Cobra** — CLI commands and argument handling
- **Bubble Tea** — Interactive terminal interface
- **Lip Gloss** — Terminal styling and layout
- **Windows APIs** — Process lifecycle management

## 🗺️ Roadmap

Procly is still evolving. Some things I'd like to work on next:

- [ ] Linux and macOS support
- [ ] Improved process resource monitoring
- [ ] More dashboard customization
- [ ] Better workspace management
- [ ] Shell autocomplete and command suggestions
- [ ] Additional scheduling options
- [ ] Easier installation and package manager support

## 🤝 Contributing

Contributions, bug reports, and feature suggestions are welcome.

If you find something that could be better, feel free to open an issue or submit a pull request.

## 📄 License

Procly is open source under the [MIT License](LICENSE).
