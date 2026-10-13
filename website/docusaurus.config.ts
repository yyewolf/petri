import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// The docs on main describe the release line version.go declares: Kubernetes
// 1.N is petri 0.N. Older lines are frozen under versioned_docs/ (see
// scripts/version-docs.sh).
function currentLine(): string {
  const versionFile = fileURLToPath(new URL('../version.go', import.meta.url));
  const match = readFileSync(versionFile, 'utf8').match(
    /^const KubernetesVersion = "1\.(\d+)"$/m,
  );
  if (!match) {
    throw new Error(`cannot read 'const KubernetesVersion = "1.N"' from ${versionFile}`);
  }
  return `0.${match[1]}`;
}

const repo = 'https://github.com/yyewolf/petri';

const config: Config = {
  title: 'petri',
  tagline: 'A real Kubernetes control plane inside go test',
  favicon: 'img/favicon.svg',

  url: 'https://yyewolf.github.io',
  baseUrl: '/petri/',
  trailingSlash: false,
  organizationName: 'yyewolf',
  projectName: 'petri',

  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',
  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          editUrl: `${repo}/edit/main/website/`,
          lastVersion: 'current',
          versions: {
            current: {
              label: currentLine(),
              path: '',
            },
          },
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    colorMode: {
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'petri',
      logo: {
        alt: 'petri',
        src: 'img/favicon.svg',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docs',
          position: 'left',
          label: 'Docs',
        },
        {
          type: 'docsVersionDropdown',
          position: 'right',
        },
        {
          href: 'https://pkg.go.dev/github.com/yyewolf/petri',
          label: 'API',
          position: 'right',
        },
        {
          href: repo,
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Docs',
          items: [
            {label: 'Getting started', to: '/docs/getting-started'},
            {label: 'API reference', href: 'https://pkg.go.dev/github.com/yyewolf/petri'},
          ],
        },
        {
          title: 'Project',
          items: [
            {label: 'GitHub', href: repo},
            {label: 'Releases', href: `${repo}/releases`},
            {label: 'Contributing', href: `${repo}/blob/main/CONTRIBUTING.md`},
          ],
        },
      ],
      copyright: 'petri is licensed under Apache-2.0.',
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'go', 'yaml'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;
