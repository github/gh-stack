// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// https://astro.build/config
export default defineConfig({
	site: 'https://github.github.com',
	base: '/gh-stack/',
	trailingSlash: 'always',
	redirects: {
		'/': 'https://docs.github.com/en/pull-requests/how-tos/stacked-pull-requests',
		'/introduction/overview/': 'https://docs.github.com/en/pull-requests/get-started/about-stacked-prs',
		'/getting-started/quick-start/': 'https://docs.github.com/en/pull-requests/get-started/stacked-prs-quickstart',
		'/guides/stacked-prs/': 'https://docs.github.com/en/pull-requests/tutorials/stack-code-changes-in-pull-requests',
		'/guides/ui/': 'https://docs.github.com/en/pull-requests/how-tos/create-pull-requests/creating-stacked-pull-requests',
		'/guides/workflows/': 'https://docs.github.com/en/pull-requests/how-tos/create-pull-requests/managing-stacked-pull-requests',
		'/guides/modify/': 'https://docs.github.com/en/pull-requests/how-tos/create-pull-requests/managing-stacked-pull-requests',
		'/reference/cli/': 'https://docs.github.com/en/pull-requests/reference/stacked-prs-cli-commands',
		'/reference/webhooks/': 'https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/optimizing-ci-for-stacked-pull-requests',
		'/reference/graphql-api/': 'https://docs.github.com/en/pull-requests/reference/stacked-pull-requests-rest-and-graphql-apis',
		'/reference/rest-api/': 'https://docs.github.com/en/pull-requests/reference/stacked-pull-requests-rest-and-graphql-apis',
		'/reference/merge-api/': 'https://docs.github.com/en/pull-requests/reference/stacked-pull-requests-rest-and-graphql-apis',
		'/faq/': 'https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-stacked-pull-requests',
	},
	devToolbar: {
		enabled: false
	},
	integrations: [
		starlight({
			title: 'GitHub Stacked PRs',
			description: 'Break large changes into small, reviewable pull requests. Manage your stacks on GitHub, with the gh stack CLI, or via our APIs.',
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
						{ label: 'Restructuring Stacks', slug: 'guides/modify' },
					],
				},
				{
					label: 'Reference',
					items: [
						{ label: 'CLI Commands', slug: 'reference/cli' },
						{ label: 'Webhooks', slug: 'reference/webhooks' },
						{ label: 'GraphQL API', slug: 'reference/graphql-api' },
						{ label: 'REST API', slug: 'reference/rest-api' },
						{ label: 'Merge API', slug: 'reference/merge-api' },
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
