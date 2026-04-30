<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Network, Hammer, FolderCode, AlertCircle, ChevronRight, BookOpen, X } from 'lucide-svelte';
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

	// Context-edit modal state.
	let editingSlug: string | null = $state(null);
	let editingRepoName = $state('');
	let editingContext = $state('');
	let savingContext = $state(false);

	async function openContextEditor(row: GraphRepoRow) {
		editingSlug = row.slug;
		editingRepoName = row.repoName;
		editingContext = '';
		try {
			const res = await getGraphContext(row.slug);
			editingContext = res.context;
		} catch {
			// new repo, no context yet — leave empty
		}
	}

	function closeContextEditor() {
		editingSlug = null;
		editingRepoName = '';
		editingContext = '';
	}

	async function saveContext() {
		if (!editingSlug) return;
		savingContext = true;
		try {
			await setGraphContext(editingSlug, editingContext);
			closeContextEditor();
		} catch (err) {
			buildErrors[editingSlug] = err instanceof Error ? err.message : 'save failed';
		}
		savingContext = false;
	}

	const phaseLabels: Record<GraphPhase, string> = {
		started: 'starting…',
		extracting: 'extracting',
		building: 'building',
		clustering: 'clustering',
		writing: 'writing',
		complete: 'complete',
		failed: 'failed'
	};

	onMount(async () => {
		try {
			rows = await listGraphs();
		} catch {
			rows = [];
		}
		loading = false;
	});

	const unsub = events.on('graphs:build', async (e) => {
		live[e.slug] = { phase: e.phase, percent: e.percent, error: e.error };
		if (e.phase === 'complete' || e.phase === 'failed') {
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

	async function handleBuild(slug: string, force = false) {
		buildErrors[slug] = '';
		live[slug] = { phase: 'started', percent: 0 };
		try {
			const res = await buildGraph(slug, { force });
			if (res.status === 'ready') {
				// SHA already had a snapshot — refresh and clear inline state
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
					<div class="group bg-bourbon-950/30 border border-bourbon-800 rounded-lg px-5 py-3.5">
						<div class="flex items-center justify-between gap-4">
							<div class="flex items-start gap-3 min-w-0">
								<FolderCode size={14} class="text-cmd-400 shrink-0 mt-1.5" />
								<div class="flex flex-col gap-1 min-w-0">
									<span class="text-bourbon-200 truncate">{row.repoName}</span>
									{#if row.snapshotCount > 0 && row.latestSha}
										<div class="flex items-baseline gap-3 text-xs">
											<span class="font-mono text-bourbon-300 bg-bourbon-800/60 border border-bourbon-700/40 px-1.5 py-0.5 rounded">
												{shortSha(row.latestSha)}
											</span>
											<span class="text-bourbon-600">
												{row.latestNodeCount ?? 0} nodes
												{#if row.latestBuiltAt}
													· built {timeAgo(row.latestBuiltAt)}
												{/if}
											</span>
										</div>
									{:else}
										<span class="text-xs text-bourbon-600">no snapshots</span>
									{/if}
								</div>
							</div>

							<div class="flex items-center gap-2 shrink-0">
								{#if !inFlight}
									<button
										onclick={() => openContextEditor(row)}
										title="Edit graph context"
										class="text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
									>
										<BookOpen size={14} />
									</button>
								{/if}
								{#if inFlight}
									<span class="font-display text-[10px] uppercase tracking-widest text-run-500">
										{phaseLabels[inFlight.phase]}
									</span>
								{:else if row.snapshotCount > 0 && row.latestSha}
									<button
										onclick={() => handleBuild(row.slug, true)}
										title="Rebuild for current HEAD"
										class="flex items-center gap-1.5 px-3 py-1.5 rounded-md
											text-xs font-display font-bold uppercase tracking-widest
											border backdrop-blur-sm transition-colors cursor-pointer
											bg-bourbon-800/40 border-bourbon-700/40 text-bourbon-400
											hover:bg-bourbon-800/60 hover:border-bourbon-600/50 hover:text-bourbon-200"
									>
										<Hammer size={12} />
										Rebuild
									</button>
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
								{:else}
									<button
										onclick={() => handleBuild(row.slug)}
										class="flex items-center gap-1.5 px-3 py-1.5 rounded-md
											text-xs font-display font-bold uppercase tracking-widest
											border backdrop-blur-sm transition-colors cursor-pointer
											bg-cmd-700/40 border-cmd-600/30 text-cmd-400
											hover:bg-cmd-700/60 hover:border-cmd-500/50 hover:text-cmd-300"
									>
										<Hammer size={12} />
										Build graph
									</button>
								{/if}
							</div>
						</div>

						{#if inFlight}
							<div class="mt-3 h-1 bg-bourbon-800 rounded overflow-hidden">
								<div
									class="h-full bg-cmd-500 transition-all duration-300"
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

<!-- Context editor modal — markdown guidance for the LLM trace pipeline.
     Stored in repos.graph_context, loaded fresh on each open. -->
{#if editingSlug}
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div
		class="fixed inset-0 z-40 flex items-center justify-center bg-bourbon-950/80 backdrop-blur-sm"
		onclick={closeContextEditor}
	>
		<!-- svelte-ignore a11y_click_events_have_key_events -->
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="w-[600px] max-h-[80vh] flex flex-col bg-bourbon-900 border border-bourbon-700 rounded-2xl shadow-2xl overflow-hidden"
			onclick={(e) => e.stopPropagation()}
		>
			<header class="flex items-center justify-between px-5 py-3 border-b border-bourbon-800">
				<div class="flex items-center gap-3">
					<span class="font-display text-xs font-bold uppercase tracking-widest text-run-500">
						context
					</span>
					<span class="text-bourbon-200">{editingRepoName}</span>
				</div>
				<button
					onclick={closeContextEditor}
					class="text-bourbon-500 hover:text-bourbon-200 transition-colors cursor-pointer"
				>
					<X size={16} />
				</button>
			</header>

			<div class="px-5 py-3 border-b border-bourbon-800">
				<p class="text-xs text-bourbon-500 leading-relaxed">
					Markdown describing this repo's architecture, entry points, and
					notable flows. The LLM trace pipeline anchors on this when
					generating data-flow visualizations.
				</p>
			</div>

			<div class="flex-1 min-h-0 px-5 py-3">
				<textarea
					bind:value={editingContext}
					placeholder={`# Architecture\nDescribe what this repo does and how requests flow through it.\n\n# Entry points\n- src/index.ts — bootstrap\n- src/handlers/* — route handlers\n\n# Notable flows\n- generate-image: Vision → OpenAI → S3`}
					class="w-full h-[40vh] bg-bourbon-950 border border-bourbon-800 rounded-lg px-3 py-2
						text-sm font-mono text-bourbon-200 placeholder:text-bourbon-700
						focus:outline-none focus:border-cmd-500 transition-colors resize-none leading-relaxed"
				></textarea>
			</div>

			<footer class="flex items-center justify-end gap-3 px-5 py-3 border-t border-bourbon-800">
				<button
					onclick={closeContextEditor}
					class="px-3 py-1.5 text-xs font-display font-bold uppercase tracking-widest
						text-bourbon-500 hover:text-bourbon-300 transition-colors cursor-pointer"
				>
					Cancel
				</button>
				<button
					onclick={saveContext}
					disabled={savingContext}
					class="flex items-center gap-1.5 px-3 py-1.5 rounded-md
						text-xs font-display font-bold uppercase tracking-widest
						border backdrop-blur-sm transition-colors cursor-pointer
						bg-cmd-700/40 border-cmd-600/30 text-cmd-400
						hover:bg-cmd-700/60 hover:border-cmd-500/50 hover:text-cmd-300
						disabled:opacity-40 disabled:cursor-default"
				>
					{savingContext ? 'Saving…' : 'Save'}
				</button>
			</footer>
		</div>
	</div>
{/if}
