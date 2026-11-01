import { lysConfig } from 'lys-docs-theme/config'

export default lysConfig({
  name: 'lys-splash',
  description: "The lys desktop's boot splash, in place of Plymouth.",
  nav: [
    { text: 'Guide', link: '/guide/getting-started' },
    { text: 'Install', link: '/guide/install' },
    { text: 'How it works', link: '/guide/how-it-works' },
  ],
  sidebar: [
    {
      text: 'Guide',
      items: [
        { text: 'Getting started', link: '/guide/getting-started' },
        { text: 'Install', link: '/guide/install' },
        { text: 'Configuration and themes', link: '/guide/configuration' },
        { text: 'Command line', link: '/guide/command-line' },
      ],
    },
    {
      text: 'Internals',
      items: [
        { text: 'How it works', link: '/guide/how-it-works' },
        { text: 'Development', link: '/guide/development' },
      ],
    },
  ],
})
