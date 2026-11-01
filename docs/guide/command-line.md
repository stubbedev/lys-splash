# Command line

One binary has every role. The name it runs under and its first argument pick
the role:

| Run as | Role |
| --- | --- |
| `plymouthd` | The daemon, which detaches once it is ready, as plymouthd does |
| `lys-splash --mode=...`, with `--mode` first | The daemon, in the foreground |
| `plymouth`, or `lys-splash` with anything else | The client |

## The daemon

| Option | What it does |
| --- | --- |
| `--mode=MODE` | What the splash shows for: `boot` (the default), `shutdown`, `reboot`, `kexec` or `updates` |
| `--theme=FILE` | The theme file, by default `/etc/lys-splash/theme.yaml` |
| `--pid-file=FILE` | Writes the daemon's pid here, and removes it on exit |
| `--no-daemon` | As `plymouthd`, stays in the foreground |
| `--debug` | Logs at debug level |
| `--attach-to-session` | Accepted for plymouthd's command line; the splash always runs on the console's session |

When a splash is already running, a new daemon reports ready and exits 0. That
is how the initrd's splash carries on into the real root while
`lys-splash-start.service` starts there too.

The units and scripts start the daemon; you should not need to.

## The client

The client talks to the daemon as plymouth's does, for scripts that call
`plymouth`. Only root's requests are served; any other user's are refused, which
is exit status 1.

These take the place of the command, and only one runs:

| Option | What it does |
| --- | --- |
| `--ping` | Checks whether a daemon is running |
| `--wait` | Waits until the daemon quits; exits 0 at once if none is running |
| `--quit` | Tells the daemon to quit |
| `--has-active-vt` | Checks whether the daemon has an active VT |
| `--sysinit` | Tells the daemon the root is writable |
| `--show-splash`, `--hide-splash` | Shows or hides the splash |
| `--ask-for-password` | Asks for a password and prints it |
| `--details` | Tells the daemon the boot hit an error |
| `--newroot=DIR` | Tells the daemon the new root is mounted at DIR |
| `--update=STATUS` | Tells the daemon a boot status |
| `--ignore-keystroke=KEYS` | Stops waiting for these keys |

These are commands:

| Command | What it does |
| --- | --- |
| `show-splash`, `hide-splash` | Shows or hides the splash |
| `display-message --text=TEXT`, `message --text=TEXT` | Shows a message |
| `hide-message --text=TEXT` | Hides that message, if it is the one showing |
| `change-mode --MODE` | `--updates`, `--system-upgrade` and `--firmware-upgrade` show the update's progress bar; `--boot-up`, `--shutdown`, `--reboot` and `--system-reset` show the throbber |
| `system-update --progress=N` | Shows the update's progress bar at N percent |
| `update --status=STATUS` | Tells the daemon a boot status |
| `update-root-fs --new-root-dir=DIR` | Moves the daemon into the root at DIR |
| `update-root-fs --read-write` | Tells the daemon the root is writable |
| `deactivate` | Drops DRM master and keeps the frame on screen, for the [hand-over](/guide/how-it-works#handing-the-screen-over) |
| `reactivate` | Takes DRM master back and shows the splash again |
| `quit` | Quits and gives the console back with the boot messages |
| `quit --retain-splash` | Quits and leaves the frame on screen |
| `reload` | Reads the theme file again |
| `pause-progress`, `unpause-progress` | Pauses or resumes the animation |
| `report-error` | Tells the daemon the boot hit an error, which it logs |
| `ignore-keystroke --keys=KEYS` | Stops waiting for these keys |
| `ask-for-password` | Asks for a password |
| `ask-question` | Asks a question, whose answer is echoed as it is typed |
| `watch-keystroke --keys=KEYS` | Waits for one of these keys, or any key without `--keys` |

`ask-for-password` takes `--prompt`, `--dont-pause-progress`, `--command` and
`--number-of-tries`. `--command` runs a command with the password on its
standard input, first trying every password already typed, then asking until
the command succeeds or the tries run out; 0 tries is no limit. With no daemon
running it runs the command without a password on standard input.
`ask-question` takes `--prompt`, `--command` and `--dont-pause-progress`, and
`watch-keystroke` takes `--command`.

## Exit status

| Status | Meaning |
| --- | --- |
| 0 | Done |
| 1 | A refused request, an unanswered prompt or bad usage |
| 2 | The daemon hung up in the middle of a request |
| 3 | No daemon is running |
