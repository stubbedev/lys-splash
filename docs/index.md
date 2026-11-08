---
layout: home

hero:
  name: lys-splash
  text: A boot splash in place of Plymouth
  tagline: One static Go binary that starts in the initrd, draws on every output, asks for disk passwords, survives the switch to the real root and hands its last frame to the display manager.
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
  - title: plymouthd's protocol
    details: Listens on plymouthd's socket and answers to plymouth's command line, so systemd, its password agents and cryptsetup's scripts work unchanged.
  - title: Self-contained
    details: Static, pure Go, with theme, font and logo embedded. The initrd needs no libraries for it.
  - title: Disk passwords
    details: A password typed once is tried on the next disk. Without a display the prompts are text on the console.
  - title: Every output
    details: Firmware framebuffer to native driver, with the firmware's logo, panel rotation and HiDPI scale.
  - title: Flicker-free hand-over
    details: A display manager takes the cards while the frame is on screen; the splash exits behind it.
---
