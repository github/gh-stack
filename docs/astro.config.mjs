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
			description: 'Break large changes into small, reviewable pull requests that build on each other — with native GitHub support and the gh stack CLI.',
			favicon: '/favicon.svg',
			logo: {
				src: './src/assets/github-invertocat.svg',
				alt: 'GitHub',
			},
			head: [
				{ tag: 'meta', attrs: { property: 'og:type', content: 'website' } },
				{ tag: 'meta', attrs: { property: 'og:site_name', content: 'GitHub Stacked PRs' } },
				{ tag: 'meta', attrs: { property: 'og:image', content: 'https://github.github.com/gh-stack/github-social-card.jpg' } },
				{ tag: 'meta', attrs: { property: 'og:image:alt', content: 'GitHub Stacked PRs — Break large changes into small, reviewable pull requests' } },
				{ tag: 'meta', attrs: { property: 'og:image:width', content: '1200' } },
				{ tag: 'meta', attrs: { property: 'og:image:height', content: '630' } },
				{ tag: 'meta', attrs: { name: 'twitter:card', content: 'summary_large_image' } },
				{ tag: 'meta', attrs: { name: 'twitter:site', content: '@github' } },
				{ tag: 'meta', attrs: { name: 'twitter:image', content: 'https://github.github.com/gh-stack/github-social-card.jpg' } },
			],
			components: {
				SocialIcons: './src/components/CustomHeader.astro',
			},
			customCss: [
				'./src/styles/custom.css',
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
