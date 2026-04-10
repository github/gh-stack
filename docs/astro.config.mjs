// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// https://astro.build/config
export default defineConfig({
	site: 'https://github.github.com',
	base: '/gh-stack/',
	trailingSlash: 'always',
	devToolbar: {
		enabled: false
	},
	integrations: [
		starlight({
			title: 'GitHub Stacked PRs',
			description: 'Manage stacked branches and pull requests with the gh stack CLI extension.',
			favicon: '/favicon.svg',
			head: [
				{ tag: 'meta', attrs: { name: 'robots', content: 'noindex, nofollow' } },
			],
			components: {
				SocialIcons: './src/components/CustomHeader.astro',
			},
			customCss: [
				'./src/styles/custom.css',
			],
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/github/gh-stack' },
			],
			tableOfContents: {
				minHeadingLevel: 2,
				maxHeadingLevel: 4
			},
			pagination: true,
			expressiveCode: {
				frames: {
					showCopyToClipboardButton: true,
				},
			},
			sidebar: [
				{
					label: 'Introduction',
					items: [
						{ label: 'Overview', slug: 'introduction/overview' },
					],
				},
				{
					label: 'Getting Started',
					items: [
						{ label: 'Quick Start', slug: 'getting-started/quick-start' },
					],
				},
				{
					label: 'Guides',
					items: [
						{ label: 'Working with Stacked PRs', slug: 'guides/stacked-prs' },
						{ label: 'Stacked PRs in the GitHub UI', slug: 'guides/ui' },
						{ label: 'Typical Workflows', slug: 'guides/workflows' },
					],
				},
				{
					label: 'Reference',
					items: [
						{ label: 'CLI Commands', slug: 'reference/cli' },
					],
				},
				{
					label: 'FAQ',
					slug: 'faq',
				},
			],
		}),
	],
});
