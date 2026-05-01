<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Network, Hammer, FolderCode, AlertCircle, ChevronRight, ChevronDown, X } from 'lucide-svelte';
	import {
		listGraphs,
		buildGraph,
		getGraphContext,
		setGraphContext,
		type GraphRepoRow,
		type GraphPhase
	} from '$lib/api';
	import { events } from '$lib/events';

	type LiveBuild = {
		phase: GraphPhase;
		percent: number;
		error?: string;
	};

	let rows: GraphRepoRow[] = $state([]);
	let loading = $state(true);
	let live: Record<string, LiveBuild> = $state({});
	let buildErrors: Record<string, string> = $state({});

	// Build modal state. Open via the "Build All" / "Rebuild All" dropdown
	// option; submitting saves the context and kicks off graph + traces.
	let buildModalSlug: string | null = $state(null);
	let buildModalRepoName = $state('');
	let buildModalContext = $state('');
	let buildModalContextLoaded = $state('');
	let buildModalSubmitting = $state(false);
	let buildModalError: string | null = $state(null);
	let buildModalTextarea: HTMLTextAreaElement | undefined = $state(undefined);

	$effect(() => {
		if (buildModalSlug && buildModalTextarea) buildModalTextarea.focus();
	});

	// Per-row dropdown state — only one row's menu is ever open.
	let openDropdownSlug: string | null = $state(null);

	function toggleDropdown(slug: string) {
		openDropdownSlug = openDropdownSlug === slug ? null : slug;
	}

	$effect(() => {
		if (!openDropdownSlug) return;
		function onPointerDown(e: PointerEvent) {
			const target = e.target as HTMLElement | null;
			if (!target) return;
			if (target.closest('[data-dropdown-anchor]')) return;
			openDropdownSlug = null;
		}
		window.addEventListener('pointerdown', onPointerDown);
		return () => window.removeEventListener('pointerdown', onPointerDown);
	});

	async function openBuildModal(row: GraphRepoRow) {
		openDropdownSlug = null;
		buildModalSlug = row.slug;
		buildModalRepoName = row.repoName;
		buildModalContext = '';
		buildModalContextLoaded = '';
		buildModalError = null;
		try {
			const res = await getGraphContext(row.slug);
			buildModalContext = res.context;
			buildModalContextLoaded = res.context;
		} catch {
			// new repo, no context yet — leave empty
		}
	}

	function closeBuildModal() {
		buildModalSlug = null;
		buildModalRepoName = '';
		buildModalContext = '';
		buildModalContextLoaded = '';
		buildModalError = null;
	}

	async function submitBuildModal() {
		if (!buildModalSlug) return;
		const slug = buildModalSlug;
		buildModalSubmitting = true;
		buildModalError = null;
		try {
			// Save context only if it changed — keeps the row's metadata
			// stable when the user opens and closes without edits.
			if (buildModalContext !== buildModalContextLoaded) {
				await setGraphContext(slug, buildModalContext);
			}
			closeBuildModal();
			await runBuild(slug, ['graph', 'traces'], true);
		} catch (err) {
			buildModalError = err instanceof Error ? err.message : 'build failed';
		}
		buildModalSubmitting = false;
	}

	function handleBuildModalKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			e.preventDefault();
			closeBuildModal();
		} else if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
			e.preventDefault();
			submitBuildModal();
		}
	}

	const phaseLabels: Record<GraphPhase, string> = {
		started: 'starting…',
		extracting: 'extracting',
		building: 'building',
		clustering: 'clustering',
		writing: 'writing',
		tracing: 'tracing',
		complete: 'complete',
		failed: 'failed'
	};

	onMount(async () => {
		try {
			rows = await listGraphs();
			// If a build is mid-flight when the page loads, the SSE history
			// is lost — but the DB row's status reflects it. Hydrate live[]
			// from any non-ready/failed row so the in-flight indicator
			// shows immediately. Subsequent SSE events overwrite this with
			// real phase info as they arrive.
			for (const row of rows) {
				if (row.latestStatus && row.latestStatus !== 'ready' && row.latestStatus !== 'failed') {
					live[row.slug] = { phase: 'started', percent: 0 };
				}
			}
		} catch {
			rows = [];
		}
		loading = false;
	});

	const unsub = events.on('graphs:build', async (e) => {
		live[e.slug] = { phase: e.phase, percent: e.percent, error: e.error };
		if (e.phase === 'complete' || e.phase === 'failed') {
			// Trace failures are soft on the backend (graph still ready),
			// so surface them on the row instead of swallowing silently.
			if (e.trace_error) {
				buildErrors[e.slug] = `traces failed: ${e.trace_error}`;
			} else if (e.error) {
				buildErrors[e.slug] = e.error;
			}
			try {
				rows = await listGraphs();
			} catch {
				// transient; leave rows alone
			}
			setTimeout(() => {
				delete live[e.slug];
				live = { ...live };
			}, 1500);
		}
	});

	onDestroy(unsub);

	async function runBuild(slug: string, targets: ('graph' | 'traces')[], force: boolean) {
		buildErrors[slug] = '';
		live[slug] = { phase: 'started', percent: 0 };
		try {
			const res = await buildGraph(slug, { force, targets });
			if (res.status === 'ready') {
				rows = await listGraphs();
				delete live[slug];
				live = { ...live };
			}
		} catch (err) {
			buildErrors[slug] = err instanceof Error ? err.message : 'build failed';
			delete live[slug];
			live = { ...live };
		}
	}

	function buildGraphOnly(row: GraphRepoRow) {
		openDropdownSlug = null;
		runBuild(row.slug, ['graph'], row.snapshotCount > 0);
	}

	function shortSha(sha: string | null | undefined): string {
		return sha ? sha.slice(0, 7) : '';
	}

	function timeAgo(iso: string | null | undefined): string {
		if (!iso) return '';
		const date = new Date(iso);
		const seconds = Math.floor((Date.now() - date.getTime()) / 1000);
		if (seconds < 60) return 'just now';
		const minutes = Math.floor(seconds / 60);
		if (minutes < 60) return `${minutes}m ago`;
		const hours = Math.floor(minutes / 60);
		if (hours < 24) return `${hours}h ago`;
		return `${Math.floor(hours / 24)}d ago`;
	}
</script>

<div class="mb-6">
	<h1 class="font-display text-3xl font-bold text-bourbon-100 lowercase">graphs</h1>
	<p class="text-bourbon-600 mt-1">Knowledge graphs of your repos</p>
</div>

{#if loading}
	<div class="flex items-center justify-center gap-3 text-bourbon-600 py-12">
		<div class="w-4 h-4 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
		<span class="font-display text-xs uppercase tracking-widest">Loading</span>
	</div>
{:else}
	<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6">
		<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500 mb-4">
			<span class="flex items-center gap-2"><Network size={12} /> Repos</span>
		</h2>

		{#if rows.length === 0}
			<p class="text-bourbon-600 text-sm">
				No monitored repos yet. Add one in
				<a href="/settings" class="text-cmd-400 hover:text-cmd-300 no-underline">settings</a>.
			</p>
		{:else}
			<div class="flex flex-col gap-1.5">
				{#each rows as row (row.slug)}
					{@const inFlight = live[row.slug]}
					{@const error = buildErrors[row.slug]}
					{@const hasSnapshot = row.snapshotCount > 0 && !!row.latestSha}
					{@const isFailed = row.latestStatus === 'failed'}
					{@const isReady = hasSnapshot && row.latestStatus === 'ready'}
					{@const dropdownOpen = openDropdownSlug === row.slug}
					<div class="group bg-bourbon-950/30 border border-bourbon-800 rounded-lg px-5 py-3.5">
						<div class="flex items-center justify-between gap-4">
							<div class="flex items-start gap-3 min-w-0">
								<FolderCode size={14} class="text-cmd-400 shrink-0 mt-1.5" />
								<div class="flex flex-col gap-1 min-w-0">
									<span class="text-bourbon-200 truncate">{row.repoName}</span>
									{#if hasSnapshot}
										<div class="flex items-baseline gap-3 text-xs">
											<span class="font-mono text-bourbon-300 bg-bourbon-800/60 border border-bourbon-700/40 px-1.5 py-0.5 rounded">
												{shortSha(row.latestSha)}
											</span>
											{#if isFailed}
												<span class="flex items-center gap-1 text-red-400 font-mono">
													<AlertCircle size={11} />
													last build failed
												</span>
											{:else}
												<span class="text-bourbon-600">
													{row.latestNodeCount ?? 0} nodes
													{#if row.latestBuiltAt}
														· built {timeAgo(row.latestBuiltAt)}
													{/if}
												</span>
											{/if}
										</div>
									{:else}
										<span class="text-xs text-bourbon-600">no snapshots</span>
									{/if}
								</div>
							</div>

							<div class="flex items-center gap-2 shrink-0">
								{#if inFlight}
									<span class="font-display text-[10px] uppercase tracking-widest text-run-500">
										{phaseLabels[inFlight.phase]}
									</span>
								{:else}
									<div class="relative" data-dropdown-anchor>
										<button
											onclick={() => toggleDropdown(row.slug)}
											title={hasSnapshot ? 'Rebuild options' : 'Build options'}
											class="flex items-center gap-1.5 px-3 py-1.5 rounded-md
												text-xs font-display font-bold uppercase tracking-widest
												border backdrop-blur-sm transition-colors cursor-pointer
												{hasSnapshot
													? 'bg-bourbon-800/40 border-bourbon-700/40 text-bourbon-400 hover:bg-bourbon-800/60 hover:border-bourbon-600/50 hover:text-bourbon-200'
													: 'bg-cmd-700/40 border-cmd-600/30 text-cmd-400 hover:bg-cmd-700/60 hover:border-cmd-500/50 hover:text-cmd-300'}"
										>
											<Hammer size={12} />
											{hasSnapshot ? 'Rebuild' : 'Build'}
											<ChevronDown size={11} class="opacity-70" />
										</button>

										{#if dropdownOpen}
											<div class="absolute right-0 top-full mt-1 z-20 w-56 rounded-md
												bg-bourbon-900 border border-bourbon-700 shadow-xl overflow-hidden">
												<button
													onclick={() => buildGraphOnly(row)}
													class="w-full text-left px-3 py-2 hover:bg-bourbon-800/60 transition-colors cursor-pointer
														flex flex-col gap-0.5"
												>
													<span class="text-xs text-bourbon-200">Build graph only</span>
													<span class="text-[10px] text-bourbon-600">Tree-sitter extraction. Fast.</span>
												</button>
												<button
													onclick={() => openBuildModal(row)}
													class="w-full text-left px-3 py-2 border-t border-bourbon-800/60
														hover:bg-bourbon-800/60 transition-colors cursor-pointer
														flex flex-col gap-0.5"
												>
													<span class="text-xs text-bourbon-200">Build everything…</span>
													<span class="text-[10px] text-bourbon-600">Graph + LLM traces. ~1-3 min.</span>
												</button>
											</div>
										{/if}
									</div>

									{#if isReady}
										<a
											href="/graphs/{row.slug}/{row.latestSha}"
											class="flex items-center gap-1.5 px-3 py-1.5 rounded-md
												text-xs font-display font-bold uppercase tracking-widest no-underline
												border backdrop-blur-sm transition-colors cursor-pointer
												bg-run-700/30 border-run-700/40 text-run-400
												hover:bg-run-700/50 hover:border-run-500/50 hover:text-run-300"
										>
											<ChevronRight size={12} />
											Open
										</a>
									{/if}
								{/if}
							</div>
						</div>

						{#if inFlight}
							<div class="mt-3 h-1 bg-bourbon-800 rounded overflow-hidden">
								<div
									class="h-full bg-cmd-500 transition-all duration-300"
									class:animate-pulse={inFlight.phase === 'tracing'}
									style:width="{inFlight.percent}%"
								></div>
							</div>
						{/if}

						{#if error}
							<div class="mt-3 flex items-center gap-2 text-xs text-red-400">
								<AlertCircle size={12} />
								<span class="font-mono">{error}</span>
							</div>
						{/if}
					</div>
				{/each}
			</div>
		{/if}
	</div>
{/if}

{#if buildModalSlug}
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<div
		class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
		onmousedown={(e) => { if (e.target === e.currentTarget) closeBuildModal(); }}
		onkeydown={handleBuildModalKeydown}
		role="dialog"
		tabindex="-1"
	>
		<div class="bg-bourbon-900 border border-bourbon-800 rounded-2xl w-[90vw] max-w-3xl min-h-[60vh] max-h-[85vh] flex flex-col overflow-hidden">
			<div class="flex items-center justify-between px-6 py-4 border-b border-bourbon-800 shrink-0">
				<div class="flex items-center gap-3">
					<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">Build</h2>
					<span class="text-bourbon-200 text-sm">{buildModalRepoName}</span>
				</div>
				<button
					onclick={closeBuildModal}
					class="text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
				>
					<X size={18} />
				</button>
			</div>

			<div class="px-6 py-3 border-b border-bourbon-800/50 shrink-0">
				<p class="text-xs text-bourbon-500 leading-relaxed">
					Guidance for how traces should be generated — architecture, entry points, notable flows the LLM should focus on. Saved on Build.
				</p>
			</div>

			<div class="flex-1 overflow-y-auto bg-bourbon-950 px-6 py-4">
				<textarea
					bind:this={buildModalTextarea}
					bind:value={buildModalContext}
					placeholder={`# Architecture\nDescribe what this repo does and how requests flow through it.\n\n# Entry points\n- src/index.ts — bootstrap\n- src/handlers/* — route handlers\n\n# Notable flows\n- generate-image: Vision → OpenAI → S3`}
					class="w-full h-full min-h-[40vh] bg-transparent border-none
						text-sm font-mono text-bourbon-200 placeholder:text-bourbon-700
						focus:outline-none resize-none leading-relaxed"
				></textarea>
			</div>

			{#if buildModalError}
				<div class="px-6 py-2 border-t border-bourbon-800/50 shrink-0 flex items-center gap-2 text-xs text-red-400">
					<AlertCircle size={12} />
					<span class="font-mono">{buildModalError}</span>
				</div>
			{/if}

			<div class="flex items-center justify-between gap-4 px-6 py-3 border-t border-bourbon-800 shrink-0">
				<span class="text-[9px] text-bourbon-700">⌘+Enter to build</span>
				<button
					onclick={submitBuildModal}
					disabled={buildModalSubmitting}
					class="flex items-center gap-1.5 text-[10px] font-mono text-cmd-400 hover:text-cmd-300 transition-colors cursor-pointer disabled:opacity-50"
				>
					<Hammer size={12} />
					{buildModalSubmitting ? 'building…' : 'Build'}
				</button>
			</div>
		</div>
	</div>
{/if}
