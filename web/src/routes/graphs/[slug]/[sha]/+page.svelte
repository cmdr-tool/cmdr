<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { X, Hammer, ChevronDown, Info } from 'lucide-svelte';
	import {
		getGraph,
		listSnapshots,
		buildGraph,
		type GraphSnapshot,
		type GraphSnapshotMeta,
		type GraphPhase
	} from '$lib/api';
	import { events } from '$lib/events';
	import NetworkFacet from '$lib/components/graphs/NetworkFacet.svelte';
	import FlowFacet from '$lib/components/graphs/FlowFacet.svelte';
	import GraphSidebar from '$lib/components/graphs/GraphSidebar.svelte';
	import { communityColor } from '$lib/components/graphs/colors';

	type Facet = 'network' | 'flow';

	let slug = $derived(page.params.slug ?? '');
	let sha = $derived(page.params.sha ?? '');

	let snapshot: GraphSnapshot | null = $state(null);
	let snapshotList: GraphSnapshotMeta[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);

	let pickerOpen = $state(false);
	let buildPhase: GraphPhase | null = $state(null);
	let buildError: string | null = $state(null);
	let selectedId: string | null = $state(null);
	let statsExpanded = $state(false);
	let facet: Facet = $state('network');
	let flowDepth = $state(2);

	let repoName = $derived.by(() => {
		const s = snapshot;
		if (!s) return slug;
		return s.snapshot.repo_path.split('/').pop() || slug;
	});

	// Top communities by size — for the legend bottom-left of the canvas.
	let topCommunities = $derived.by(() => {
		if (!snapshot) return [] as { id: number; label: string; size: number }[];
		const list = Object.entries(snapshot.communities).map(([id, c]) => ({
			id: Number(id),
			label: c.label,
			size: c.node_ids.length
		}));
		list.sort((a, b) => b.size - a.size);
		return list.slice(0, 8);
	});

	const phaseLabels: Record<GraphPhase, string> = {
		started: 'starting',
		extracting: 'extracting',
		building: 'building',
		clustering: 'clustering',
		writing: 'writing',
		complete: 'complete',
		failed: 'failed'
	};

	$effect(() => {
		if (!slug || !sha) return;
		loading = true;
		error = null;
		Promise.all([getGraph(slug, sha), listSnapshots(slug)])
			.then(([snap, list]) => {
				snapshot = snap;
				snapshotList = list;
				loading = false;
			})
			.catch((e) => {
				error = e instanceof Error ? e.message : 'failed to load';
				loading = false;
			});
	});

	const unsub = events.on('graphs:build', async (e) => {
		if (e.slug !== slug) return;
		buildPhase = e.phase;
		if (e.phase === 'complete' && e.sha) {
			// Refresh the snapshot list and navigate to the new snapshot
			snapshotList = await listSnapshots(slug);
			buildPhase = null;
			goto(`/graphs/${slug}/${e.sha}`);
		} else if (e.phase === 'failed') {
			buildError = e.error ?? 'build failed';
			buildPhase = null;
		}
	});

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			if (pickerOpen) pickerOpen = false;
			else goto('/graphs');
		}
	}

	async function handleRebuild() {
		buildError = null;
		try {
			const res = await buildGraph(slug);
			if (res.status === 'ready') {
				// Already had a snapshot for current HEAD — just refresh the list
				snapshotList = await listSnapshots(slug);
			} else {
				buildPhase = 'started';
			}
		} catch (err) {
			buildError = err instanceof Error ? err.message : 'build failed';
		}
	}

	function shortSha(s: string) {
		return s.slice(0, 7);
	}

	function timeAgo(iso: string) {
		const date = new Date(iso);
		const seconds = Math.floor((Date.now() - date.getTime()) / 1000);
		if (seconds < 60) return 'just now';
		const minutes = Math.floor(seconds / 60);
		if (minutes < 60) return `${minutes}m ago`;
		const hours = Math.floor(minutes / 60);
		if (hours < 24) return `${hours}h ago`;
		return `${Math.floor(hours / 24)}d ago`;
	}

	onDestroy(unsub);
</script>

<svelte:window onkeydown={handleKeydown} />

<div class="h-screen flex flex-col bg-bourbon-950 overflow-hidden">
	<!-- Header bar — extra pt to clear the macOS title bar / traffic-light area
	     when running inside cmdr.app. Harmless in the browser. -->
	<header class="shrink-0 flex items-center justify-between gap-4 px-5 pt-7 pb-3 border-b border-bourbon-800 bg-bourbon-900/50 backdrop-blur-sm">
		<div class="flex items-center gap-4 min-w-0">
			<a
				href="/graphs"
				class="flex items-center justify-center w-7 h-7 rounded-md text-bourbon-500 hover:text-bourbon-200 hover:bg-bourbon-800/50 transition-colors no-underline"
				title="Back to graphs (Esc)"
			>
				<X size={16} />
			</a>

			<div class="flex items-center gap-3 min-w-0">
				<span class="font-display text-xs font-bold uppercase tracking-widest text-run-500 mr-3">graph</span>
				{#if snapshot}
					<span class="text-bourbon-200 truncate" title={snapshot.snapshot.repo_path}>{repoName}</span>

					<!-- Snapshot picker -->
					<button
						onclick={() => (pickerOpen = !pickerOpen)}
						class="flex items-center gap-1.5 px-2.5 py-1 rounded-md
							text-[10px] font-mono
							border backdrop-blur-sm transition-colors cursor-pointer
							bg-bourbon-800/40 border-bourbon-700/40 text-bourbon-300
							hover:bg-bourbon-800/60 hover:border-bourbon-600/50"
					>
						{shortSha(sha)}
						<ChevronDown size={12} class="text-bourbon-500" />
					</button>
				{/if}
			</div>
		</div>

		<div class="flex items-center gap-3 shrink-0">
			{#if snapshot}
				<!-- Facet tabs -->
				<div class="flex items-center gap-1 p-0.5 rounded-md bg-bourbon-800/40 border border-bourbon-700/40">
					{#each ['network', 'flow'] as f (f)}
						<button
							onclick={() => (facet = f as Facet)}
							class="px-2.5 py-1 rounded font-display text-[10px] font-bold uppercase tracking-widest transition-colors cursor-pointer
								{facet === f
									? 'bg-bourbon-700/60 text-bourbon-200'
									: 'text-bourbon-500 hover:text-bourbon-300'}"
						>
							{f}
						</button>
					{/each}
				</div>

				{#if facet === 'flow'}
					<!-- Depth slider -->
					<div class="flex items-center gap-2 px-2.5 py-1 rounded-md bg-bourbon-800/40 border border-bourbon-700/40">
						<span class="font-display text-[10px] uppercase tracking-widest text-bourbon-500">depth</span>
						<input
							type="range"
							min="1"
							max="5"
							bind:value={flowDepth}
							class="w-20 accent-cmd-500"
						/>
						<span class="font-mono text-[10px] text-bourbon-300 w-3 text-right">{flowDepth}</span>
					</div>
				{/if}
			{/if}

			{#if buildPhase}
				<span class="font-display text-[10px] uppercase tracking-widest text-run-500">
					{phaseLabels[buildPhase]}
				</span>
			{/if}
			<button
				onclick={handleRebuild}
				disabled={buildPhase !== null}
				class="flex items-center gap-1.5 px-3 py-1.5 rounded-md
					text-xs font-display font-bold uppercase tracking-widest
					border backdrop-blur-sm transition-colors cursor-pointer
					bg-bourbon-800/40 border-bourbon-700/40 text-bourbon-400
					hover:bg-bourbon-800/60 hover:border-bourbon-600/50 hover:text-bourbon-200
					disabled:opacity-40 disabled:cursor-default"
			>
				<Hammer size={12} />
				Rebuild
			</button>
		</div>
	</header>

	<!-- Snapshot picker dropdown (overlay) -->
	{#if pickerOpen}
		<div class="absolute top-20 left-32 z-20 w-80 max-h-96 overflow-y-auto bg-bourbon-900 border border-bourbon-700 rounded-lg shadow-xl">
			{#each snapshotList as s}
				<a
					href="/graphs/{slug}/{s.commitSha}"
					onclick={() => (pickerOpen = false)}
					class="flex items-center justify-between px-4 py-2.5 border-b border-bourbon-800 last:border-b-0 no-underline transition-colors
						{s.commitSha === sha ? 'bg-bourbon-800/60' : 'hover:bg-bourbon-800/40'}"
				>
					<div class="flex flex-col gap-0.5 min-w-0">
						<div class="flex items-center gap-2">
							<span class="font-mono text-xs text-bourbon-200">{shortSha(s.commitSha)}</span>
							{#if s.status === 'building'}
								<span class="text-[9px] font-mono text-run-500">building</span>
							{:else if s.status === 'failed'}
								<span class="text-[9px] font-mono text-red-400">failed</span>
							{/if}
						</div>
						<span class="text-[10px] text-bourbon-600">
							{s.nodeCount} nodes · {s.edgeCount} edges · {timeAgo(s.builtAt)}
						</span>
					</div>
				</a>
			{/each}
		</div>
	{/if}

	<!-- Body -->
	<main class="flex-1 min-h-0 flex">
		{#if loading}
			<div class="flex-1 flex items-center justify-center gap-3 text-bourbon-600">
				<div class="w-4 h-4 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
				<span class="font-display text-xs uppercase tracking-widest">Loading graph</span>
			</div>
		{:else if error}
			<div class="flex-1 flex flex-col items-center justify-center gap-2 text-bourbon-500">
				<span class="font-display text-xs uppercase tracking-widest text-red-400">Error</span>
				<span class="text-sm font-mono">{error}</span>
			</div>
		{:else if snapshot}
			<div class="flex-1 min-w-0 relative">
				{#if buildError}
					<div class="absolute inset-x-0 top-0 z-10 px-5 py-2 bg-red-950/40 border-b border-red-900/50 text-xs font-mono text-red-300">
						{buildError}
					</div>
				{/if}

				{#if snapshot.nodes.length === 0}
					<div class="absolute inset-0 flex flex-col items-center justify-center gap-2 text-bourbon-500">
						<span class="font-display text-xs uppercase tracking-widest">Empty graph</span>
						<span class="text-sm">No nodes were extracted. Phase 1 only handles Go files; multi-language support lands in Phase 6.</span>
					</div>
				{:else if facet === 'flow'}
					<FlowFacet {snapshot} bind:selectedId bind:depth={flowDepth} />
				{:else}
					<NetworkFacet {snapshot} bind:selectedId />

					<!-- Community legend (top communities by size) -->
					<div class="absolute bottom-3 left-3 max-w-xs px-3 py-2.5 rounded-md
						bg-bourbon-900/70 border border-bourbon-800 backdrop-blur-sm
						pointer-events-none">
						<div class="font-display text-[9px] font-bold uppercase tracking-widest text-bourbon-500 mb-1.5">
							communities
						</div>
						<div class="flex flex-col gap-1">
							{#each topCommunities as c (c.id)}
								<div class="flex items-center gap-2 text-[10px] font-mono text-bourbon-400">
									<span
										class="w-2 h-2 rounded-full shrink-0"
										style:background-color={communityColor(c.id)}
									></span>
									<span class="truncate">{c.label}</span>
									<span class="text-bourbon-600">{c.size}</span>
								</div>
							{/each}
						</div>
					</div>

					<!-- Stats info button (collapsible) -->
					<div class="absolute bottom-3 right-3">
						{#if statsExpanded}
							<div class="px-3 py-2 rounded-md bg-bourbon-900/70 border border-bourbon-800 backdrop-blur-sm
								text-[10px] font-mono text-bourbon-400 leading-relaxed">
								<div class="flex items-center justify-between gap-3 mb-1">
									<span class="font-display text-[9px] font-bold uppercase tracking-widest text-bourbon-500">stats</span>
									<button
										onclick={() => (statsExpanded = false)}
										class="text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
									>
										<X size={10} />
									</button>
								</div>
								<div>{snapshot.stats.node_count} nodes</div>
								<div>{snapshot.stats.edge_count} edges</div>
								<div>{snapshot.stats.community_count} communities</div>
							</div>
						{:else}
							<button
								onclick={() => (statsExpanded = true)}
								class="flex items-center justify-center w-7 h-7 rounded-md
									bg-bourbon-900/70 border border-bourbon-800 backdrop-blur-sm
									text-bourbon-500 hover:text-bourbon-200 transition-colors cursor-pointer"
								title="Graph stats"
							>
								<Info size={14} />
							</button>
						{/if}
					</div>
				{/if}
			</div>

			{#if snapshot.nodes.length > 0}
				<GraphSidebar {snapshot} bind:selectedId />
			{/if}
		{/if}
	</main>
</div>
