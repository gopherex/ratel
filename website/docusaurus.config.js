// The site renders docs/ in place: the specification is the documentation,
// there is no second copy to drift from it.
import {themes} from 'prism-react-renderer';

const repo = 'https://github.com/gopherex/ratel';

export default {
  title: 'ratel',
  tagline: 'PostgreSQL for Go: code from SQL, a typed query builder, migrations that never lock production',
  favicon: 'ratel.svg',
  // docs/assets holds the mascot, banner and benchmark chart the README uses.
  staticDirectories: ['../docs/assets'],
  url: 'https://gopherex.github.io',
  baseUrl: '/ratel/',
  organizationName: 'gopherex',
  projectName: 'ratel',
  trailingSlash: false,
  stylesheets: [
    'https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;700;800&family=Noto+Sans:wght@400;600;700&display=swap',
  ],
  onBrokenLinks: 'throw',
  markdown: {
    // docs/ is plain CommonMark: generic signatures such as Col[M, V] must not
    // be parsed as JSX.
    format: 'detect',
    hooks: {onBrokenMarkdownLinks: 'throw'},
  },
  presets: [
    [
      'classic',
      {
        docs: {
          path: '../docs',
          exclude: ['assets/**'],
          sidebarPath: './sidebars.js',
          editUrl: `${repo}/edit/main/docs/`,
        },
        blog: false,
        theme: {customCss: './src/css/custom.css'},
      },
    ],
  ],
  themeConfig: {
    colorMode: {defaultMode: 'dark', respectPrefersColorScheme: true},
    navbar: {
      title: 'ratel',
      logo: {alt: '', src: 'ratel.svg'},
      items: [
        {type: 'docSidebar', sidebarId: 'docs', position: 'left', label: 'Docs'},
        {to: '/#benchmarks', label: 'Benchmarks', position: 'left', activeBaseRegex: '^$'},
        {href: repo, label: 'GitHub', position: 'right'},
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {title: 'Docs', items: [
          {label: 'Runtime', to: '/docs/runtime'},
          {label: 'Migrations', to: '/docs/migrations'},
          {label: 'CLI', to: '/docs/cli'},
        ]},
        {title: 'Project', items: [
          {label: 'GitHub', href: repo},
          {label: 'Releases', href: `${repo}/releases`},
          {label: 'Issues', href: `${repo}/issues`},
        ]},
      ],
      copyright: 'MIT license',
    },
    prism: {
      theme: themes.github,
      darkTheme: themes.vsDark,
      additionalLanguages: ['bash', 'sql', 'go', 'protobuf', 'yaml'],
    },
  },
};
