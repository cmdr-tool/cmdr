<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { X, ChevronDown, Info, ArrowLeft, Layers, Network } from 'lucide-svelte';
	import {
		getGraph,
		listSnapshots,
		getTraces,
		type GraphSnapshot,
		type GraphSnapshotMeta,
		type GraphPhase,
		type TraceResult
	} from '$lib/api';
	import { events } from '$lib/events';
	import NetworkFacet from '$lib/components/graphs/NetworkFacet.svelte';
	import TracesFacet from '$lib/components/graphs/TracesFacet.svelte';
	import GraphSidebar from '$lib/components/graphs/GraphSidebar.svelte';
	import TracesSidebar from '$lib/components/graphs/TracesSidebar.svelte';
	import { communityColor, superCommunityColor } from '$lib/components/graphs/colors';

	type NetworkMode = 'flat' | 'super' | 'focus';

	type Facet = 'network' | 'traces';

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
	let networkMode: NetworkMode = $state('flat');
	let focusedSuperId: number | null = $state(null);
	let networkRebuilding = $state(false);

	// Traces state — lifted to the page so the right-hand TracesSidebar
	// and the canvas TracesFacet can share the same selection.
	let traces = $state<TraceResult | null>(null);
	let tracesLoading = $state(false);
	let tracesError: string | null = $state(null);
	let selectedTraceIdx = $state(0);

	let selectedTrace = $derived(traces?.traces[selectedTraceIdx] ?? null);

	$effect(() => {
		if (!slug || !sha) return;
		tracesLoading = true;
		tracesError = null;
		selectedTraceIdx = 0;
		getTraces(slug, sha)
			.then((t) => {
				traces = t;
			})
			.catch((e) => {
				tracesError = e instanceof Error ? e.message : 'failed to load traces';
			})
			.finally(() => {
				tracesLoading = false;
			});
	});

	let repoName = $derived.by(() => {
		const s = snapshot;
		if (!s) return slug;
		return s.snapshot.repo_path.split('/').pop() || slug;
	});

	// Legend entries depend on the current zoom mode:
	//   - flat / super → list super-communities (high-level neighborhoods)
	//   - focus        → list tier-2 children of the focused super-community
	type LegendEntry = { id: number; label: string; size: number; tier: 'super' | 'community' };
	let legendEntries = $derived.by<LegendEntry[]>(() => {
		if (!snapshot) return [];
		const list: LegendEntry[] = [];
		if (networkMode === 'focus' && focusedSuperId !== null) {
			const sc = snapshot.super_communities?.[String(focusedSuperId)];
			const childIds = sc?.child_ids ?? [];
			for (const cid of childIds) {
				const c = snapshot.communities[cid];
				if (c) list.push({ id: Number(cid), label: c.label, size: c.node_ids.length, tier: 'community' });
			}
		} else {
			const supers = snapshot.super_communities ?? {};
			for (const [id, c] of Object.entries(supers)) {
				list.push({ id: Number(id), label: c.label, size: c.node_ids.length, tier: 'super' });
			}
		}
		list.sort((a, b) => b.size - a.size);
		return list;
	});

	let focusedSuperLabel = $derived.by(() => {
		if (focusedSuperId === null || !snapshot) return null;
		return snapshot.super_communities?.[String(focusedSuperId)]?.label ?? `super ${focusedSuperId}`;
	});

	function exitFocus() {
		networkMode = 'super';
		focusedSuperId = null;
		selectedId = null;
	}

	const phaseLabels: Record<GraphPhase, string> = {
		started: 'starting',
		extracting: 'extracting',
		building: 'building',
		clustering: 'clustering',
		writing: 'writing',
		tracing: 'tracing',
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
				// Loading stays true until the facet signals it has
				// drawn the new snapshot — see handleFacetReady. For
				// graphs with no nodes there's no facet to wait on,
				// so clear immediately.
				if (snap.nodes.length === 0) {
					loading = false;
				}
			})
			.catch((e) => {
				error = e instanceof Error ? e.message : 'failed to load';
				loading = false;
			});
	});

	function handleFacetReady() {
		loading = false;
	}

	let pickerEl: HTMLDivElement | null = $state(null);
	let pickerToggleEl: HTMLButtonElement | null = $state(null);

	// Dismiss the snapshot picker on any pointerdown that's outside both
	// the dropdown panel and its toggle button. Per the project's pointer-
	// events convention.
	$effect(() => {
		if (!pickerOpen) return;
		function handleOutside(e: PointerEvent) {
			const target = e.target as Node | null;
			if (!target) return;
			if (pickerEl && pickerEl.contains(target)) return;
			if (pickerToggleEl && pickerToggleEl.contains(target)) return;
			pickerOpen = false;
		}
		window.addEventListener('pointerdown', handleOutside);
		return () => window.removeEventListener('pointerdown', handleOutside);
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
				<span class="font-display text-xs font-bold uppercase tracking-widest text-run-500 mr-3">atlas</span>
				{#if snapshot}
					<span class="text-bourbon-200 truncate" title={snapshot.snapshot.repo_path}>{repoName}</span>

					<!-- Snapshot picker -->
					<button
						bind:this={pickerToggleEl}
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
					{#each ['network', 'traces'] as f (f)}
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
			{/if}

			{#if buildPhase}
				<span class="font-display text-[10px] uppercase tracking-widest text-run-500">
					{phaseLabels[buildPhase]}
				</span>
			{/if}
		</div>
	</header>

	<!-- Snapshot picker dropdown (overlay) -->
	{#if pickerOpen}
		<div bind:this={pickerEl} class="absolute top-20 left-32 z-20 w-80 max-h-96 overflow-y-auto bg-bourbon-900 border border-bourbon-700 rounded-lg shadow-xl">
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
		{#if error}
			<div class="flex-1 flex flex-col items-center justify-center gap-2 text-bourbon-500">
				<span class="font-display text-xs uppercase tracking-widest text-red-400">Error</span>
				<span class="text-sm font-mono">{error}</span>
			</div>
		{:else if snapshot}
			<div class="flex-1 min-w-0 relative bg-[#08070a]">
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
				{:else}
					<!-- Both facets stay mounted across tab switches so we don't
					     pay re-init costs (force simulation rebuild, layout
					     re-computation) every time the user toggles. The
					     inactive one is hidden + click-disabled but keeps state. -->
					<div class="absolute inset-0" class:invisible={facet !== 'network'} class:pointer-events-none={facet !== 'network'}>
						<NetworkFacet
							{snapshot}
							bind:selectedId
							bind:mode={networkMode}
							bind:focusedSuperId
							bind:rebuilding={networkRebuilding}
							onReady={handleFacetReady}
						/>
					</div>
					<div class="absolute inset-0" class:invisible={facet !== 'traces'} class:pointer-events-none={facet !== 'traces'}>
						<TracesFacet
							trace={selectedTrace}
							loading={tracesLoading}
							repoPath={snapshot.snapshot.repo_path}
							emptyMessage={traces ? 'Select a trace from the sidebar.' : 'No traces — run a Build with traces from the graphs index.'}
							onNavigate={(id) => {
								selectedId = id;
								facet = 'network';
							}}
							onReady={handleFacetReady}
						/>
					</div>

					{#if facet === 'network'}
						<!-- Zoom-mode controls: top-left of canvas. Flat = full
						     graph with hierarchical color; Super = bird's-eye
						     of just super-communities; Focus = drilled into
						     one super-community. -->
						<div class="absolute top-3 left-3 flex items-center gap-1.5 px-1 py-1 rounded-md
							bg-bourbon-900/70 border border-bourbon-800 backdrop-blur-sm">
							{#if networkMode === 'focus'}
								<button
									onclick={exitFocus}
									class="flex items-center gap-1.5 px-2 py-1 rounded text-[10px] font-mono
										text-bourbon-400 hover:text-bourbon-100 hover:bg-bourbon-800/60 transition-colors cursor-pointer"
									title="Back to super view"
								>
									<ArrowLeft size={11} />
									<span>back</span>
								</button>
								<span class="text-bourbon-700">·</span>
								<span class="flex items-center gap-1.5 px-2 py-1 text-[10px] font-mono">
									<span class="w-2 h-2 rounded-full shrink-0" style:background-color={superCommunityColor(focusedSuperId ?? 0)}></span>
									<span class="text-bourbon-200 truncate max-w-[160px]">{focusedSuperLabel}</span>
								</span>
							{:else}
								<button
									onclick={() => { networkMode = 'flat'; focusedSuperId = null; }}
									class="flex items-center gap-1.5 px-2 py-1 rounded text-[10px] font-mono transition-colors cursor-pointer
										{networkMode === 'flat'
											? 'bg-bourbon-800/80 text-bourbon-100'
											: 'text-bourbon-500 hover:text-bourbon-200 hover:bg-bourbon-800/40'}"
									title="Show every node, colored by neighborhood"
								>
									<Network size={11} />
									<span>flat</span>
								</button>
								<button
									onclick={() => { networkMode = 'super'; focusedSuperId = null; }}
									class="flex items-center gap-1.5 px-2 py-1 rounded text-[10px] font-mono transition-colors cursor-pointer
										{networkMode === 'super'
											? 'bg-bourbon-800/80 text-bourbon-100'
											: 'text-bourbon-500 hover:text-bourbon-200 hover:bg-bourbon-800/40'}"
									title="Bird's-eye: one node per neighborhood"
								>
									<Layers size={11} />
									<span>super</span>
								</button>
							{/if}
						</div>

						<!-- Legend (sorted by size). Shows super-communities by
						     default; tier-2 children when in focus mode. -->
						<div class="absolute bottom-3 left-3 max-w-xs max-h-72 overflow-y-auto px-3 py-2.5 rounded-md
							bg-bourbon-900/70 border border-bourbon-800 backdrop-blur-sm">
							<div class="font-display text-[9px] font-bold uppercase tracking-widest text-bourbon-500 mb-1.5">
								{networkMode === 'focus' ? 'sub-clusters' : 'neighborhoods'}
								<span class="text-bourbon-700">{legendEntries.length}</span>
							</div>
							<div class="flex flex-col gap-1">
								{#each legendEntries as c (c.tier + c.id)}
									<div class="flex items-center gap-2 text-[10px] font-mono text-bourbon-400">
										<span
											class="w-2 h-2 rounded-full shrink-0"
											style:background-color={c.tier === 'super'
												? superCommunityColor(c.id)
												: communityColor(c.id, focusedSuperId ?? undefined)}
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
				{/if}

				<!-- Loading overlay — covers the canvas while it's preparing
				     the new snapshot's layout, or while a zoom-mode switch
				     is rebuilding the force simulation. Drops away when
				     the facet signals onReady (which clears rebuilding). -->
				{#if (loading || networkRebuilding) && snapshot.nodes.length > 0}
					<div class="absolute inset-0 z-30 flex items-center justify-center gap-3 bg-bourbon-950/80 backdrop-blur-sm text-bourbon-400">
						<div class="w-4 h-4 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
						<span class="font-display text-xs uppercase tracking-widest">
							{loading ? 'Loading graph' : 'Switching view'}
						</span>
					</div>
				{/if}
			</div>

			{#if snapshot.nodes.length > 0 && facet === 'network'}
				<GraphSidebar {snapshot} bind:selectedId />
			{:else if facet === 'traces'}
				<TracesSidebar
					{traces}
					bind:selectedTraceIdx
					loading={tracesLoading}
					error={tracesError}
				/>
			{/if}
		{:else if loading}
			<!-- Initial mount: snapshot not yet fetched. -->
			<div class="flex-1 flex items-center justify-center gap-3 text-bourbon-600">
				<div class="w-4 h-4 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
				<span class="font-display text-xs uppercase tracking-widest">Loading graph</span>
			</div>
		{/if}
	</main>
</div>
