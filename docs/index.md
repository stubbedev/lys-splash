---
layout: home

hero:
  name: lys-splash
  text: A boot splash in place of Plymouth
  tagline: One static Go binary that starts in the initrd, draws on every output, asks for disk passwords, survives the switch to the real root and leaves its last frame for the greeter.
  image:
    src: /lys-splash.svg
    alt: lys-splash
  actions:
    - theme: brand
      text: Get started
      link: /guide/getting-started
    - theme: alt
      text: Install
      link: /guide/install
    - theme: alt
      text: GitHub
      link: https://github.com/stubbedev/lys-splash

features:
  - title: Speaks plymouthd's protocol
    details: It listens on plymouthd's socket and answers to plymouth's command line, so systemd, its password agents and cryptsetup's scripts talk to it unchanged.
  - title: Nothing else in the initrd
    details: The binary is static and pure Go, with its theme, font and logo embedded, so the initrd carries no libraries for it.
  - title: Disk passwords
    details: A password typed once is tried on the next disk. A machine without a display gets the same prompts as text on the console.
  - title: Every output
    details: From the firmware's framebuffer to the native driver, with the firmware's logo, panel rotation and a scale for high-resolution screens.
  - title: No flicker into the greeter
    details: A display manager takes the card while the frame is on screen and the splash exits behind it. lys-login's greeter does this.
  - title: Tested on whole boots
    details: CI boots NixOS VMs from a LUKS passphrase typed at the splash to lys-login's greeter and back down through the reboot splash.
---
