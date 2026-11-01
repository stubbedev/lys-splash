# How it works

## A boot

1. **In the initrd,** `lys-splash-start.service` runs
   `lys-splash --mode=boot --pid-file=/run/plymouth/pid`, after udev and the
   console setup. On initramfs-tools the `init-premount` script runs `plymouthd`
   instead. The splash re-executes itself with `@` leading `argv[0]`, which makes
   systemd spare it across the switch to the real root.
2. **Password requests** reach it through plymouth's interfaces:
   `lys-splash-ask-password.path` starts `systemd-tty-ask-password-agent
   --watch --plymouth` when a request appears, and cryptsetup's initramfs
   scripts call `plymouth`.
3. **Before the switch,** `lys-splash-switch-root.service` or the `init-bottom`
   script runs `lys-splash update-root-fs --new-root-dir=...`, and the splash
   chroots into the new root.
4. **In the real root,** `lys-splash-start.service` runs again, finds the socket
   taken and exits 0. The splash that started in the initrd is still the one on
   screen. `lys-splash-read-write.service` tells it the root is writable, and it
   takes the VT back after systemd reset the console.
5. **At the end,** a display manager [takes the screen
   over](#handing-the-screen-over), or with none `lys-splash-quit.service` quits
   the splash at `multi-user.target` and the console comes back with the boot
   messages. `lys-splash-quit-wait.service` holds logins until then.

The splash records how long it showed in `/var/lib/lys-splash/boot-duration`
when it quits, if the root became writable. The next boot's progress bar runs
against that time, and appears at the switch to the real root when the initrd
does not hold the file. A crash leaves the previous value, because the file is
replaced whole.

## The units

Each unit conflicts with the Plymouth unit it stands in for and follows [the
kernel command line](/guide/install#the-kernel-command-line).

| Unit | Wanted by | What it does |
| --- | --- | --- |
| `lys-splash-start.service` | `sysinit.target`, `initrd-switch-root.target` | Starts the boot splash |
| `lys-splash-ask-password.path`, `.service` | `sysinit.target` | Forwards systemd's password requests to the splash |
| `lys-splash-switch-root.service` | `initrd-switch-root.target` | Moves the splash into the real root; only in the initrd |
| `lys-splash-read-write.service` | `sysinit.target` | Tells the splash the root is writable; only outside the initrd |
| `lys-splash-quit.service` | `multi-user.target` | Quits the splash |
| `lys-splash-quit-wait.service` | `multi-user.target` | Holds `getty-pre.target` until the splash quits |
| `lys-splash-reboot.service` | `reboot.target` | The reboot splash |
| `lys-splash-poweroff.service` | `poweroff.target` | The shutdown splash |
| `lys-splash-halt.service` | `halt.target` | The shutdown splash |
| `lys-splash-kexec.service` | `kexec.target` | The kexec splash |

## Drawing

Every connected output of every DRM card is lit on two linear XRGB8888 dumb
buffers, flipped with atomic commits, one per vblank, copying only what changed.

- **Mode.** An output keeps the mode it already shows, so the first frame needs
  no modeset. Otherwise it takes the sink's preferred mode, or else its first.
- **Firmware framebuffers.** The splash draws on simpledrm, efidrm, vesadrm and
  ofdrm, and follows kernel uevents as the native driver takes over. It does not
  draw on a firmware framebuffer at 640x480, 800x600 or 1024x768, which the
  firmware sets up in place of the panel's own resolution; it waits for the
  native driver, as Plymouth does.
- **Scale.** Each output gets scale 1 or 2 by Plymouth's rules, from the size
  EDID reports. A firmware framebuffer that does not know its display's size
  guesses from the resolution, and later outputs keep that guess, so the picture
  does not jump when the native driver arrives.
- **Rotation.** The connector's panel orientation turns the frame, so a panel
  mounted sideways or upside down shows it upright.
- **Firmware logo.** With `logo: firmware` the logo comes from the ACPI BGRT
  table in `/sys/firmware/acpi/bgrt`, if its status says it is still on screen.
  It stays at the firmware's offsets when they centre it on this mode, so it does
  not move, and otherwise goes at the height Plymouth places it.

## Boot messages

While the splash shows, it redirects the console into a pseudo-terminal with
`TIOCCONS`, so the kernel's and systemd's text does not draw over the frame. It
keeps that text for two uses: <kbd>Esc</kbd> shows it, and a quit without
`--retain-splash` writes it back to the console.

## Passwords

Prompts queue and show one at a time, oldest first. Every password typed is
cached for the next disk, so one passphrase can unlock several. Every buffer that
held a secret is zeroed once it is no longer needed.

The keyboard comes from the active virtual terminal. On a kernel without virtual
terminals, prompts get no answer.

## Without a display

With no graphical output the splash is text on the virtual terminal: each message
on its own line, the update's progress as a percentage, and the prompt on the last
line, a question's answer echoed and a password's not.

## Handing the screen over

The kernel's fbdev emulation (`drm_kms_helper.fbdev_emulation=1`, the default)
restores its console's framebuffer with a full modeset whenever the last open
file of a card closes (`drm_lastclose` → `drm_client_dev_restore`). Closing the
splash's framebuffer with `CLOSEFB` does not prevent it: if lys-splash holds the
last fd on a card when it exits, the console's frame replaces the splash's before
the next compositor opens the card.

lys-splash therefore hands over in Plymouth's and GDM's order, so its close is
never the last one:

1. `lys-splash deactivate` drops DRM master on every card and keeps the cards
   open, with the frame on screen.
2. The compositor opens the cards and takes master without a modeset.
3. `lys-splash quit --retain-splash` closes the splash's framebuffers with
   `CLOSEFB` and exits. The compositor's fd keeps the card open, so nothing
   restores the console.

A display manager follows that order through this contract, as GDM does with
Plymouth:

- Its unit has `After=lys-splash-start.service getty@tty1.service
  lys-splash-quit.service` and `Conflicts=lys-splash-quit.service
  getty@tty1.service`, so systemd neither quits the splash nor puts tty1 back
  into text mode under it.
- Before it starts its compositor it runs `lys-splash deactivate`
  (`ExecStartPre=-lys-splash deactivate`; without a splash the call fails and is
  ignored).
- Once its first frame has been presented (`wp_presentation` feedback), it runs
  `lys-splash quit --retain-splash`.

lys-login's greeter, on lys-wm, which keeps an inherited frame without a modeset,
implements it.

On a kernel without `CLOSEFB`, the splash logs a warning and its last frame goes
away with it.

Neither of the alternatives is used, and the NixOS module leaves fbdev emulation
on: turning it off removes the kernel console from every card, and parking the
card fds in systemd's fd store keeps the card open after the compositor exits, so
the console would never come back.

## Shutdown

The shutdown, reboot and kexec splashes never close their cards. They re-execute
with `@` leading `argv[0]` like the initrd's splash, so `systemd-shutdown` spares
them, and the power goes with their frame still on screen. A `SIGTERM` in these
modes keeps the frame too; in `boot` and `updates` it quits as `quit` does.
