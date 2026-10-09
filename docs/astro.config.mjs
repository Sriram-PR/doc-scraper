// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';

export default defineConfig({
	site: 'https://sriram-pr.github.io',
	base: '/doc-scraper',
	integrations: [
		starlight({
			title: 'doc-scraper',
			description: 'Turn documentation sites into an offline, searchable corpus for coding agents over MCP.',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/Sriram-PR/doc-scraper' }],
			editLink: { baseUrl: 'https://github.com/Sriram-PR/doc-scraper/edit/main/docs/' },
			plugins: [starlightLinksValidator()],
			sidebar: [
				{ label: 'Getting started', items: [{ autogenerate: { directory: 'getting-started' } }] },
				{ label: 'Use with agents (MCP)', items: [{ autogenerate: { directory: 'mcp' } }] },
				{ label: 'Docs frameworks', items: [{ autogenerate: { directory: 'frameworks' } }] },
				{ label: 'Guides', items: [{ autogenerate: { directory: 'guides' } }] },
				{ label: 'Reference', items: [{ autogenerate: { directory: 'reference' } }] },
				{ label: 'Detection benchmark', slug: 'benchmark' },
			],
		}),
	],
});
