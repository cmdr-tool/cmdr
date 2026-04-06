<script lang="ts">
	import '../app.css';
	import { page } from '$app/stores';
	import { LayoutDashboard, ListChecks, Settings } from 'lucide-svelte';
	import { onDestroy } from 'svelte';
	import { events } from '$lib/events';
	import type { DaemonStatus } from '$lib/api';

	let { children } = $props();
	let status: DaemonStatus | null = $state(null);
	const isApp = typeof navigator !== 'undefined' && navigator.userAgent === 'cmdr-app';

	const nav = [
		{ href: '/', label: 'Dashboard', icon: LayoutDashboard },
		{ href: '/tasks', label: 'Tasks', icon: ListChecks },
		{ href: '/settings', label: 'Settings', icon: Settings }
	];

	const unsub = events.on('status', (data) => {
		status = data;
	});

	onDestroy(unsub);

	const pageTitles: Record<string, string> = {
		'/': 'Dashboard',
		'/tasks': 'Tasks',
		'/settings': 'Settings'
	};

	let pageTitle = $derived(pageTitles[$page.url.pathname] ?? 'cmdr');
</script>

<svelte:head>
	<title>⌘R {pageTitle}</title>
</svelte:head>

<div class="relative min-h-screen bg-bourbon-950 text-bourbon-300 font-body bg-crosshair">
	<div class="pointer-events-none absolute inset-x-0 top-0 h-80 z-0 bg-linear-to-b from-bourbon-950 from-40% via-bourbon-950/85 via-50% to-transparent"></div>
	<div class="relative z-10 max-w-7xl mx-auto px-6 py-4" class:pt-8={isApp}>
		<nav class="flex items-center justify-between mb-6">
			<div class="flex items-center gap-5">
				<a href="/" class="no-underline">
					<img src="/cmdr-logo.svg" alt="cmdr" class="h-10" />
				</a>
				<span class="flex items-center gap-1.5 px-2.5 py-1 rounded-md text-[10px] font-mono
					border backdrop-blur-sm
					{status
						? 'bg-green-950/40 border-green-800/30 text-green-400'
						: 'bg-bourbon-800/40 border-bourbon-700/30 text-bourbon-500'}">
					<span class="w-1.5 h-1.5 rounded-full {status ? 'bg-green-500 shadow-[0_0_6px_var(--color-green-500)]' : 'bg-bourbon-600'}"></span>
					{status ? `pid ${status.pid}` : 'offline'}
				</span>
				{#if status?.version}
					<span class="text-[10px] font-mono text-bourbon-700">build {status.version}</span>
				{/if}
			</div>
			<ul class="flex list-none gap-1 p-0">
				{#each nav as item}
					<li>
						<a
							href={item.href}
							class="flex items-center gap-2 px-3 py-1.5 font-display text-xs font-bold uppercase tracking-widest rounded-md no-underline transition-colors
								{$page.url.pathname === item.href
									? 'text-run-400 bg-bourbon-900'
									: 'text-bourbon-600 hover:text-bourbon-400 hover:bg-bourbon-900/50'}"
						>
							<span class={$page.url.pathname === item.href ? 'text-run-500' : 'text-bourbon-500'}>
								<item.icon size={14} strokeWidth={2.5} />
							</span>
							{item.label}
						</a>
					</li>
				{/each}
			</ul>
		</nav>

		<main>
			{@render children()}
		</main>
	</div>
</div>
