<script lang="ts">
	import { Search, X, ArrowRight, ArrowLeft, ChevronRight, ChevronLeft } from 'lucide-svelte';
	import type { GraphSnapshot, GraphNode } from '$lib/api';
	import { communityColor } from './colors';

	let {
		snapshot,
		selectedId = $bindable(null)
	}: {
		snapshot: GraphSnapshot;
		selectedId?: string | null;
	} = $props();

	let query = $state('');

	let nodesById = $derived.by(() => {
		const m = new Map<string, GraphNode>();
		for (const n of snapshot.nodes) m.set(n.id, n);
		return m;
	});

	let selectedNode = $derived(selectedId ? nodesById.get(selectedId) ?? null : null);

	// Build adjacency lookups for the selected node — separate incoming
	// vs outgoing so we can label the relationship direction in the UI.
	type Neighbor = { node: GraphNode; relation: string; direction: 'out' | 'in' };
	let neighbors = $derived.by(() => {
		if (!selectedId) return [] as Neighbor[];
		const out: Neighbor[] = [];
		const seen = new Set<string>();
		for (const e of snapshot.edges) {
			if (e.source === selectedId) {
				const n = nodesById.get(e.target);
				if (n && !seen.has(`${e.target}|${e.relation}|out`)) {
					seen.add(`${e.target}|${e.relation}|out`);
					out.push({ node: n, relation: e.relation, direction: 'out' });
				}
			} else if (e.target === selectedId) {
				const n = nodesById.get(e.source);
				if (n && !seen.has(`${e.source}|${e.relation}|in`)) {
					seen.add(`${e.source}|${e.relation}|in`);
					out.push({ node: n, relation: e.relation, direction: 'in' });
				}
			}
		}
		// Sort by degree DESC (most connected neighbors first)
		out.sort((a, b) => b.node.degree - a.node.degree);
		return out;
	});

	let neighborQuery = $state('');
	let filteredNeighbors = $derived.by(() => {
		const q = neighborQuery.trim().toLowerCase();
		if (!q) return neighbors;
		return neighbors.filter(
			(n) =>
				n.node.label.toLowerCase().includes(q) ||
				n.node.id.toLowerCase().includes(q) ||
				n.relation.toLowerCase().includes(q)
		);
	});

	// List-mode state — search + group by kind.
	let filteredNodes = $derived.by(() => {
		const q = query.trim().toLowerCase();
		const all = snapshot.nodes;
		if (!q) return all;
		return all.filter(
			(n) => n.label.toLowerCase().includes(q) || n.id.toLowerCase().includes(q)
		);
	});

	let groupedByKind = $derived.by(() => {
		const groups = new Map<string, GraphNode[]>();
		for (const n of filteredNodes) {
			const arr = groups.get(n.kind) ?? [];
			arr.push(n);
			groups.set(n.kind, arr);
		}
		// Sort each group's items by degree DESC, then label asc.
		for (const arr of groups.values()) {
			arr.sort((a, b) => b.degree - a.degree || a.label.localeCompare(b.label));
		}
		// Stable kind ordering — most populous first.
		return [...groups.entries()].sort((a, b) => b[1].length - a[1].length);
	});

	const relationVerb: Record<string, { out: string; in: string }> = {
		calls: { out: 'calls', in: 'called by' },
		imports: { out: 'imports', in: 'imported by' },
		contains: { out: 'contains', in: 'contained in' },
		uses_type: { out: 'uses', in: 'used by' },
		extends: { out: 'extends', in: 'extended by' },
		implements: { out: 'implements', in: 'implemented by' },
		foreign_key: { out: 'fk →', in: 'fk ←' }
	};
	function relationLabel(relation: string, direction: 'out' | 'in'): string {
		return relationVerb[relation]?.[direction] ?? `${direction === 'out' ? '→' : '←'} ${relation}`;
	}
</script>

<aside class="w-80 shrink-0 flex flex-col border-l border-bourbon-800 bg-bourbon-950/40 backdrop-blur-sm overflow-hidden">
	{#if selectedNode}
		<!-- Detail mode -->
		<header class="shrink-0 h-11 px-4 flex items-center gap-2 border-b border-bourbon-800">
			<button
				onclick={() => (selectedId = null)}
				class="text-bourbon-500 hover:text-bourbon-200 transition-colors cursor-pointer"
				title="Back to all nodes"
			>
				<ArrowLeft size={14} />
			</button>
			<span class="font-display text-xs font-bold uppercase tracking-widest text-run-500">
				node
			</span>
		</header>

		<div class="shrink-0 px-4 py-4 border-b border-bourbon-800">
			<div class="flex items-center gap-2 mb-2">
				<span
					class="w-2.5 h-2.5 rounded-full shrink-0"
					style:background-color={communityColor(selectedNode.community)}
				></span>
				<span class="font-display text-[10px] font-bold uppercase tracking-widest text-bourbon-300">
					{selectedNode.kind}
				</span>
				<span class="text-[10px] font-mono text-bourbon-600">
					community {selectedNode.community}
				</span>
			</div>
			<div class="text-bourbon-100 font-mono text-sm break-all mb-2">{selectedNode.label}</div>
			{#if selectedNode.source_file}
				<div class="text-[10px] font-mono text-bourbon-500 break-all">{selectedNode.source_file}</div>
			{/if}
			<div class="flex items-center gap-3 mt-3 pt-3 border-t border-bourbon-800/50 text-[10px] font-mono text-bourbon-500">
				<span>degree <span class="text-bourbon-300">{selectedNode.degree}</span></span>
				<span>neighbors <span class="text-bourbon-300">{neighbors.length}</span></span>
			</div>
		</div>

		<!-- Neighbors list -->
		<div class="shrink-0 px-4 py-3 border-b border-bourbon-800">
			<div class="flex items-center gap-2">
				<Search size={12} class="text-bourbon-600" />
				<input
					bind:value={neighborQuery}
					placeholder="filter neighbors..."
					class="flex-1 bg-transparent text-xs text-bourbon-200 placeholder:text-bourbon-600 focus:outline-none"
				/>
				{#if neighborQuery}
					<button
						onclick={() => (neighborQuery = '')}
						class="text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
					>
						<X size={12} />
					</button>
				{/if}
			</div>
		</div>

		<div class="flex-1 min-h-0 overflow-y-auto">
			{#if filteredNeighbors.length === 0}
				<p class="px-4 py-3 text-xs text-bourbon-600">
					{neighborQuery ? 'No matching neighbors' : 'No connections'}
				</p>
			{:else}
				<ul class="flex flex-col">
					{#each filteredNeighbors as { node, relation, direction } (node.id + relation + direction)}
						<li>
							<button
								onclick={() => (selectedId = node.id)}
								class="w-full text-left px-4 py-2 hover:bg-bourbon-800/40 transition-colors cursor-pointer group flex items-center gap-2 border-b border-bourbon-900"
							>
								<div class="flex flex-col gap-0.5 min-w-0 flex-1">
									<div class="flex items-center gap-2 min-w-0">
										<span
											class="w-2 h-2 rounded-full shrink-0"
											style:background-color={communityColor(node.community)}
										></span>
										<span class="text-bourbon-200 text-sm truncate">{node.label}</span>
									</div>
									<div class="flex items-center gap-1.5 text-[10px] font-mono text-bourbon-600 ml-4">
										{#if direction === 'out'}
											<ArrowRight size={10} />
										{:else}
											<ArrowLeft size={10} />
										{/if}
										<span>{relationLabel(relation, direction)}</span>
										<span class="text-bourbon-700">·</span>
										<span>{node.kind}</span>
									</div>
								</div>
								<!-- Directional decorator on the right edge — mirrors the
								     inline arrow but at a more scannable location.
								     Outgoing: chevron right (you → them).
								     Incoming: chevron left (them → you). -->
								<span class="shrink-0 {direction === 'out' ? 'text-cmd-400/60' : 'text-run-400/60'}">
									{#if direction === 'out'}
										<ChevronRight size={16} />
									{:else}
										<ChevronLeft size={16} />
									{/if}
								</span>
							</button>
						</li>
					{/each}
				</ul>
			{/if}
		</div>
	{:else}
		<!-- List mode -->
		<header class="shrink-0 h-11 px-4 flex items-center justify-between border-b border-bourbon-800">
			<span class="font-display text-xs font-bold uppercase tracking-widest text-run-500">
				nodes
			</span>
			<span class="text-[10px] font-mono text-bourbon-600">{snapshot.nodes.length}</span>
		</header>

		<div class="shrink-0 px-4 py-3 border-b border-bourbon-800">
			<div class="flex items-center gap-2">
				<Search size={12} class="text-bourbon-600" />
				<input
					bind:value={query}
					placeholder="search nodes..."
					class="flex-1 bg-transparent text-xs text-bourbon-200 placeholder:text-bourbon-600 focus:outline-none"
				/>
				{#if query}
					<button
						onclick={() => (query = '')}
						class="text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
					>
						<X size={12} />
					</button>
				{/if}
			</div>
		</div>

		<div class="flex-1 min-h-0 overflow-y-auto">
			{#if filteredNodes.length === 0}
				<p class="px-4 py-3 text-xs text-bourbon-600">No matching nodes</p>
			{:else}
				{#each groupedByKind as [kind, members] (kind)}
					<div>
						<div class="sticky top-0 z-10 px-4 py-2 bg-bourbon-950/90 backdrop-blur-sm border-b border-bourbon-800 flex items-center justify-between">
							<span class="font-display text-[10px] font-bold uppercase tracking-widest text-bourbon-500">
								{kind}
							</span>
							<span class="text-[10px] font-mono text-bourbon-700">{members.length}</span>
						</div>
						<ul class="flex flex-col">
							{#each members as node (node.id)}
								<li>
									<button
										onclick={() => (selectedId = node.id)}
										class="w-full text-left px-4 py-2 hover:bg-bourbon-800/40 transition-colors cursor-pointer flex flex-col gap-0.5 border-b border-bourbon-900"
									>
										<div class="flex items-center gap-2 min-w-0">
											<span
												class="w-2 h-2 rounded-full shrink-0"
												style:background-color={communityColor(node.community)}
											></span>
											<span class="text-bourbon-200 text-sm truncate">{node.label}</span>
										</div>
										{#if node.source_file}
											<div class="text-[10px] font-mono text-bourbon-600 ml-4 truncate">
												{node.source_file}
											</div>
										{/if}
									</button>
								</li>
							{/each}
						</ul>
					</div>
				{/each}
			{/if}
		</div>
	{/if}
</aside>
