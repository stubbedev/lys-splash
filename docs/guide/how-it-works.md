# How it works

## Boot sequence

1. **Initrd.** `lys-splash-start.service` runs
   `lys-splash --mode=boot --pid-file=/run/plymouth/pid` after udev and
   `systemd-vconsole-setup`. On initramfs-tools the `init-premount` script runs
   `plymouthd` instead. The daemon re-executes itself with `@` prefixed to
   `argv[0]` so systemd spares it across the switch to the real root.
2. **Passwords.** `lys-splash-ask-password.path` starts
   `systemd-tty-ask-password-agent --watch --plymouth` when a request appears;
   cryptsetup's initramfs scripts call `plymouth` directly.
3. **Switch root.** `lys-splash-switch-root.service` (or the `init-bottom`
   script) runs `update-root-fs --new-root-dir=...`, and the daemon chroots
   into the new root.
4. **Real root.** `lys-splash-start.service` runs again, finds the socket taken
   and exits 0; the initrd's splash stays on screen.
   `lys-splash-read-write.service` reports the root writable, and the splash
   retakes the VT after systemd resets the console.
5. **End.** A display manager [takes over](#hand-over-to-a-display-manager), or
   `lys-splash-quit.service` quits the splash at `multi-user.target` and the
   console returns with the boot messages. `lys-splash-quit-wait.service` holds
   `getty-pre.target` until then.

### Boot duration

On quit, the splash writes how long it showed to
`/var/lib/lys-splash/boot-duration`, but only if it came through the switch to
the real root and the root became writable. The file is replaced atomically and
written after the screen, VT and console are released, so a slow disk delays
only the daemon's exit (and `--wait`), not the hand-over. The next boot's
progress bar runs against it; if the initrd lacks the file, the bar appears
after the switch to the real root.

## Units

Each unit conflicts with the Plymouth unit of the same role and carries the
[kernel command line](/guide/install#kernel-command-line) conditions.

| Unit | Wanted by | Role |
| --- | --- | --- |
| `lys-splash-start.service` | `sysinit.target`, `initrd-switch-root.target` | Starts the boot splash |
| `lys-splash-ask-password.path`, `.service` | `sysinit.target` | Forwards systemd's password requests |
| `lys-splash-switch-root.service` | `initrd-switch-root.target` | Moves the splash into the real root (initrd only) |
| `lys-splash-read-write.service` | `sysinit.target` | Reports the root writable (outside the initrd) |
| `lys-splash-quit.service` | `multi-user.target` | Quits the splash |
| `lys-splash-quit-wait.service` | `multi-user.target` | Holds `getty-pre.target` until the splash quits |
| `lys-splash-reboot.service` | `reboot.target` | Reboot splash |
| `lys-splash-poweroff.service` | `poweroff.target` | Shutdown splash |
| `lys-splash-halt.service` | `halt.target` | Shutdown splash |
| `lys-splash-kexec.service` | `kexec.target` | Kexec splash |

## Drawing

Every connected output of every DRM card gets two linear XRGB8888 dumb buffers,
flipped with atomic commits at most once per vblank.

- **Frame loop.** One goroutine draws and flips and never blocks on anything
  else. Per-card workers open cards, probe connectors and allocate buffers;
  each client connection has a writer behind a bounded queue and is dropped if
  it stops reading; theme and boot duration load on their own goroutine. The
  loop wakes only on change and reports damaged rectangles with
  `FB_DAMAGE_CLIPS`.
- **Mode.** An output keeps its current mode, so the first frame needs no
  modeset; otherwise it takes the preferred mode, else the first.
- **Firmware framebuffers.** Draws on simpledrm, efidrm, vesadrm and ofdrm and
  follows uevents as the native driver takes over. A firmware framebuffer at
  640x480, 800x600 or 1024x768 is skipped in favour of the native driver.
- **Scale.** 1 or 2 per output, from the physical size in EDID. A firmware
  framebuffer without a known size guesses from the resolution, and later
  outputs keep that guess so the picture does not jump.
- **Rotation.** The connector's `panel orientation` rotates the frame.
- **Firmware logo.** With `logo: firmware`, read from `/sys/firmware/acpi/bgrt`
  if its status says it is still on screen. It keeps the firmware's offsets when
  they centre it on the current mode, otherwise it is placed at a fixed height.

## Boot messages

While shown, the splash redirects the console into a pseudo-terminal with
`TIOCCONS` so kernel and systemd output does not draw over the frame. The
captured text is shown on <kbd>Esc</kbd> and written back to the console on a
`quit` without `--retain-splash`.

## Passwords

- Prompts queue and show one at a time, oldest first.
- Every typed password is cached and tried on the next disk.
- Buffers that held a secret are zeroed after use.
- Throbber and progress hold still while a prompt waits; it redraws only on a
  key or a Caps Lock change.
- With `prompt-hints`, the keyboard LEDs (`KDGKBLED`) are read when the prompt
  appears, after each key and four times a second. The keymap shown is
  `KEYMAP` from `/etc/vconsole.conf`, else `XKBLAYOUT` from
  `/etc/default/keyboard`, read at start, after the switch to the real root and
  once the root is writable. A root that names none keeps the one shown.
- Keys come from the active virtual terminal. On a kernel without virtual
  terminals, prompts get no answer.

## Without a display

With no graphical output the splash is text on the virtual terminal: one line
per message, update progress as a percentage, and the prompt on the last line
(a question's answer echoed, a password's not), with the keymap in brackets and
"(Caps Lock is on)" when applicable.

## Hand-over to a display manager

With fbdev emulation on (the kernel default), closing the last open file of a
card restores the kernel console with a modeset. So the splash's close must not
be the last one:

1. `lys-splash deactivate` drops DRM master on every card and keeps them open,
   frame on screen.
2. The compositor opens the cards and takes master without a modeset.
3. `lys-splash quit --retain-splash` closes the splash's framebuffers with
   `CLOSEFB` and exits. The compositor still holds the cards, so the console is
   not restored.

A display manager implements this contract:

- Its unit has `After=lys-splash-start.service getty@tty1.service
  lys-splash-quit.service` and `Conflicts=lys-splash-quit.service
  getty@tty1.service`.
- It runs `ExecStartPre=-lys-splash deactivate` before starting its compositor.
- After its first frame is presented (`wp_presentation` feedback), it runs
  `lys-splash quit --retain-splash`.

lys-login's greeter on lys-wm implements it. On a kernel without `CLOSEFB` the
splash logs a warning and its frame disappears when it exits.

## Shutdown

The shutdown, reboot and kexec splashes never close their cards. They
re-execute with `@` prefixed to `argv[0]` so `systemd-shutdown` spares them,
and the power goes with the frame on screen. `SIGTERM` in these modes keeps the
frame; in `boot` and `updates`, `SIGTERM` and `SIGINT` quit as `quit` does.

Exception: when a card is driven by `nvidia`, whose shutdown requires all DRM
clients gone, the shutdown splashes do not re-execute; `systemd-shutdown`'s
`SIGTERM` ends them and the console shows for the last moments.
