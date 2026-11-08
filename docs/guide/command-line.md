# Command line

One binary has every role, picked by the name it runs under and its first
argument:

| Invoked as | Role |
| --- | --- |
| `plymouthd` | Daemon; detaches once ready |
| `lys-splash --mode=...` (`--mode` first) | Daemon, in the foreground |
| `plymouth`, or `lys-splash` with anything else | Client |

The units and initramfs scripts start the daemon; you normally do not.

## Daemon

| Option | Default | Effect |
| --- | --- | --- |
| `--mode=MODE` | `boot` | `boot`, `shutdown`, `reboot`, `kexec` or `updates` |
| `--theme=FILE` | `/etc/lys-splash/theme.yaml` | Theme file |
| `--pid-file=FILE` | none | Writes the pid here; removed on exit |
| `--no-daemon` | off | As `plymouthd`, stays in the foreground |
| `--debug` | off | Logs at debug level |
| `--attach-to-session` | | Accepted and ignored; the splash always runs on the console's session |

If a splash is already running, a new daemon reports ready and exits 0.

## Client

Requests are served for root only; other users get exit status 1.

### Options

Exactly one runs, instead of a command:

| Option | Effect |
| --- | --- |
| `--ping` | Checks whether a daemon is running |
| `--wait` | Waits until the daemon quits; exits 0 at once if none runs |
| `--quit` | Tells the daemon to quit |
| `--has-active-vt` | Checks whether the daemon has an active VT |
| `--sysinit` | Tells the daemon the root is writable |
| `--show-splash`, `--hide-splash` | Shows or hides the splash |
| `--ask-for-password` | Asks for a password and prints it |
| `--details` | Reports a boot error |
| `--newroot=DIR` | Tells the daemon the new root is mounted at DIR |
| `--update=STATUS` | Sends a boot status |
| `--ignore-keystroke=KEYS` | Stops waiting for these keys |

### Commands

| Command | Effect |
| --- | --- |
| `show-splash`, `hide-splash` | Shows or hides the splash |
| `display-message --text=TEXT`, `message --text=TEXT` | Shows a message |
| `hide-message --text=TEXT` | Hides that message if it is the one showing |
| `change-mode --MODE` | `--updates`, `--system-upgrade`, `--firmware-upgrade`: update progress bar. `--boot-up`, `--shutdown`, `--reboot`, `--system-reset`: throbber |
| `system-update --progress=N` | Update progress bar at N percent |
| `update --status=STATUS` | Sends a boot status |
| `update-root-fs --new-root-dir=DIR` | Moves the daemon into the root at DIR |
| `update-root-fs --read-write` | Tells the daemon the root is writable |
| `deactivate` | Drops DRM master, keeps the frame on screen ([hand-over](/guide/how-it-works#hand-over-to-a-display-manager)) |
| `reactivate` | Takes DRM master back and shows the splash |
| `quit` | Quits and restores the console with the boot messages |
| `quit --retain-splash` | Quits and leaves the frame on screen |
| `reload` | Re-reads the theme file |
| `pause-progress`, `unpause-progress` | Pauses or resumes the animation |
| `report-error` | Reports a boot error, which the daemon logs |
| `ignore-keystroke --keys=KEYS` | Stops waiting for these keys |
| `ask-for-password` | Asks for a password and prints it |
| `ask-question` | Asks a question; the answer is echoed while typed |
| `watch-keystroke --keys=KEYS` | Waits for one of these keys, or any key without `--keys` |

Prompt options:

| Option | On | Effect |
| --- | --- | --- |
| `--prompt=TEXT` | `ask-for-password`, `ask-question` | Prompt text |
| `--dont-pause-progress` | `ask-for-password`, `ask-question` | Keeps the animation running while asking |
| `--command=CMD` | `ask-for-password`, `ask-question`, `watch-keystroke` | Runs CMD with the answer on standard input instead of printing it. CMD is split on whitespace, not by a shell |
| `--number-of-tries=N` | `ask-for-password` | With `--command`, how often to ask; `0` (default) is no limit |

`ask-for-password --command` first tries every password already typed, then
asks until the command succeeds or the tries run out, and exits with the
command's status. With no daemon running it runs the command with no standard
input.

## Exit status

| Status | Meaning |
| --- | --- |
| 0 | Done |
| 1 | Refused request, unanswered prompt or bad usage |
| 2 | The daemon hung up mid-request |
| 3 | No daemon is running |
