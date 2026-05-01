<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Network, Hammer, FolderCode, AlertCircle, ChevronRight } from 'lucide-svelte';
	import {
		listGraphs,
		buildGraph,
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
			if (e.error) {
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

	async function runBuild(row: GraphRepoRow) {
		const slug = row.slug;
		buildErrors[slug] = '';
		live[slug] = { phase: 'started', percent: 0 };
		try {
			const res = await buildGraph(slug, { force: row.snapshotCount > 0 });
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
									<button
										onclick={() => runBuild(row)}
										title={hasSnapshot ? 'Rebuild graph' : 'Build graph'}
										class="flex items-center gap-1.5 px-3 py-1.5 rounded-md
											text-xs font-display font-bold uppercase tracking-widest
											border backdrop-blur-sm transition-colors cursor-pointer
											{hasSnapshot
												? 'bg-bourbon-800/40 border-bourbon-700/40 text-bourbon-400 hover:bg-bourbon-800/60 hover:border-bourbon-600/50 hover:text-bourbon-200'
												: 'bg-cmd-700/40 border-cmd-600/30 text-cmd-400 hover:bg-cmd-700/60 hover:border-cmd-500/50 hover:text-cmd-300'}"
									>
										<Hammer size={12} />
										{hasSnapshot ? 'Rebuild graph' : 'Build graph'}
									</button>

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
