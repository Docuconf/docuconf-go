// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// SITE and BASE are set by the deploy workflow. Locally the site
// serves from the root.
export default defineConfig({
	site: process.env.SITE ?? 'https://docuconf.github.io',
	base: process.env.BASE ?? '/',
	integrations: [
		starlight({
			title: 'docuconf',
			description:
				'Typed environment contracts for every language, validated by your Kubernetes platform before anything deploys.',
			logo: { src: './src/assets/logo.svg', alt: 'docuconf' },
			favicon: '/favicon.svg',
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/docuconf' },
			],
			customCss: ['./src/styles/theme.css'],
			sidebar: [
				{
					label: 'Project',
					items: [
						{ label: 'Vision', slug: 'vision' },
						{ label: 'How it works', slug: 'how-it-works' },
						{ label: 'Languages', slug: 'languages' },
						{ label: 'Config is not feature flags', slug: 'feature-flags' },
						{ label: 'Roadmap', slug: 'roadmap' },
						{ label: 'Get involved', slug: 'community' },
					],
				},
			],
		}),
	],
});
