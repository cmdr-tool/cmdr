<script lang="ts">
	import { onMount, onDestroy, untrack } from 'svelte';
	import {
		forceSimulation,
		forceManyBody,
		forceLink,
		forceCenter,
		forceCollide,
		type Simulation,
		type SimulationNodeDatum,
		type SimulationLinkDatum
	} from 'd3-force';
	import { zoom, zoomIdentity, type ZoomBehavior } from 'd3-zoom';
	import { select } from 'd3-selection';
	import type { GraphSnapshot } from '$lib/api';

	let { snapshot }: { snapshot: GraphSnapshot } = $props();

	type SimNode = SimulationNodeDatum & {
		id: string;
		label: string;
		kind: string;
		community: number;
		degree: number;
		sourceFile: string;
	};
	type SimLink = SimulationLinkDatum<SimNode>;

	// Distinct community colors with similar saturation/lightness for the
	// dark theme. Cycles by modulo for graphs with more communities than
	// palette entries.
	const palette = [
		'#7F77DD', // cmd-purple
		'#FAC775', // run-amber
		'#85D7B5', // mint
		'#F38BA8', // pink
		'#94A3F4', // periwinkle
		'#E5C07B', // gold
		'#FF8A65', // peach
		'#80DEEA', // cyan
		'#B39DDB', // lavender
		'#A5D6A7', // sage
		'#FFAB91', // coral
		'#9FA8DA', // dusty blue
		'#CE93D8', // orchid
		'#BCAAA4', // tan
		'#90CAF9' // sky
	];
	function communityColor(c: number): string {
		return palette[((c % palette.length) + palette.length) % palette.length];
	}

	function nodeRadius(degree: number): number {
		return 4 + Math.sqrt(degree) * 1.4;
	}

	let svgEl: SVGSVGElement | null = $state(null);
	let viewport: HTMLDivElement | null = $state(null);

	let nodes: SimNode[] = $state([]);
	let links: SimLink[] = $state([]);
	let simulation: Simulation<SimNode, SimLink> | null = null;
	let zoomBehavior: ZoomBehavior<SVGSVGElement, unknown> | null = null;

	// Re-render driver: bumped on each simulation tick so Svelte re-paints.
	let tick = $state(0);
	let transform = $state({ x: 0, y: 0, k: 1 });
	let hoveredId: string | null = $state(null);
	let selectedId: string | null = $state(null);

	// Drag state — native pointer events, no d3-drag dependency.
	let dragging: { node: SimNode; pointerId: number } | null = null;

	// Convert snapshot data to simulation shape and start the simulation.
	// Re-runs whenever the snapshot prop changes (e.g. after picking a different sha).
	$effect(() => {
		const snap = snapshot;
		untrack(() => {
			rebuild(snap);
		});

		return () => {
			simulation?.stop();
		};
	});

	function rebuild(snap: GraphSnapshot) {
		simulation?.stop();

		const nodeIds = new Set(snap.nodes.map((n) => n.id));
		nodes = snap.nodes.map((n) => ({
			id: n.id,
			label: n.label,
			kind: n.kind,
			community: n.community,
			degree: n.degree,
			sourceFile: n.source_file
		}));
		links = snap.edges
			.filter((e) => nodeIds.has(e.source) && nodeIds.has(e.target))
			.map((e) => ({
				source: e.source,
				target: e.target
			}));

		// Build the simulation but don't auto-animate; we'll settle it
		// synchronously below so the user sees a stable layout immediately
		// rather than 60fps DOM updates for hundreds of nodes.
		simulation = forceSimulation<SimNode, SimLink>(nodes)
			.force(
				'link',
				forceLink<SimNode, SimLink>(links)
					.id((d) => d.id)
					.distance(45)
					.strength(0.5)
			)
			.force('charge', forceManyBody<SimNode>().strength(-120).distanceMax(400))
			.force('center', forceCenter(0, 0).strength(0.04))
			.force(
				'collide',
				forceCollide<SimNode>().radius((d) => nodeRadius(d.degree) + 2)
			)
			.alphaDecay(0.04)
			.stop()
			.on('tick', () => {
				tick++;
			});

		// Headless settle: ~150 ticks puts alpha well below the auto-stop
		// threshold, so the simulation is fully settled when we render.
		simulation.tick(150);
		tick++;

		// Fit settled layout to the viewport on next frame (after svgEl has
		// dimensions).
		requestAnimationFrame(fitToViewport);
	}

	function fitToViewport() {
		if (!svgEl || !zoomBehavior || nodes.length === 0) return;
		let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
		for (const n of nodes) {
			const x = n.x ?? 0;
			const y = n.y ?? 0;
			if (x < minX) minX = x;
			if (x > maxX) maxX = x;
			if (y < minY) minY = y;
			if (y > maxY) maxY = y;
		}
		const w = svgEl.clientWidth;
		const h = svgEl.clientHeight;
		if (w === 0 || h === 0 || maxX === minX || maxY === minY) return;
		const padding = 60;
		const scale = Math.min(
			(w - padding * 2) / (maxX - minX),
			(h - padding * 2) / (maxY - minY),
			1.5 // don't zoom in past 1.5×
		);
		const cx = (minX + maxX) / 2;
		const cy = (minY + maxY) / 2;
		// The inner <g> already translates by (w/2 + transform.x, h/2 + transform.y)
		// and scales by transform.k, so we need transform.x = -cx * k and
		// transform.y = -cy * k to put the bbox center at the viewport center.
		select(svgEl).call(
			zoomBehavior.transform,
			zoomIdentity.translate(-cx * scale, -cy * scale).scale(scale)
		);
	}

	onMount(() => {
		if (!svgEl) return;
		zoomBehavior = zoom<SVGSVGElement, unknown>()
			.scaleExtent([0.1, 8])
			.on('zoom', (event) => {
				transform = { x: event.transform.x, y: event.transform.y, k: event.transform.k };
			});
		select(svgEl).call(zoomBehavior);
	});

	onDestroy(() => {
		simulation?.stop();
	});

	function onPointerDown(e: PointerEvent, node: SimNode) {
		e.stopPropagation();
		const target = e.currentTarget as Element;
		target.setPointerCapture(e.pointerId);
		dragging = { node, pointerId: e.pointerId };
		// Pin the node during drag
		node.fx = node.x;
		node.fy = node.y;
		simulation?.alphaTarget(0.3).restart();
	}

	function onPointerMove(e: PointerEvent) {
		if (!dragging || !svgEl) return;
		// Convert client coords → SVG-local coords (after zoom transform)
		const rect = svgEl.getBoundingClientRect();
		const localX = (e.clientX - rect.left - rect.width / 2 - transform.x) / transform.k;
		const localY = (e.clientY - rect.top - rect.height / 2 - transform.y) / transform.k;
		dragging.node.fx = localX;
		dragging.node.fy = localY;
	}

	function onPointerUp(e: PointerEvent) {
		if (!dragging || dragging.pointerId !== e.pointerId) return;
		dragging.node.fx = null;
		dragging.node.fy = null;
		simulation?.alphaTarget(0);
		dragging = null;
	}

	let selectedNode = $derived(
		selectedId ? nodes.find((n) => n.id === selectedId) ?? null : null
	);
	let selectedNeighbors = $derived.by(() => {
		if (!selectedNode) return new Set<string>();
		const out = new Set<string>();
		for (const link of links) {
			const sId = typeof link.source === 'object' ? (link.source as SimNode).id : link.source;
			const tId = typeof link.target === 'object' ? (link.target as SimNode).id : link.target;
			if (sId === selectedNode.id) out.add(tId as string);
			if (tId === selectedNode.id) out.add(sId as string);
		}
		return out;
	});
</script>

<div bind:this={viewport} class="relative w-full h-full overflow-hidden">
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
	<svg
		bind:this={svgEl}
		class="w-full h-full block"
		role="application"
		aria-label="Knowledge graph network visualization"
		onpointermove={onPointerMove}
		onpointerup={onPointerUp}
		onpointercancel={onPointerUp}
		onclick={() => (selectedId = null)}
	>
		<g
			transform="translate({svgEl ? svgEl.clientWidth / 2 + transform.x : 0},{svgEl
				? svgEl.clientHeight / 2 + transform.y
				: 0}) scale({transform.k})"
		>
			<!-- Edges -->
			<g class="edges" stroke-width={0.7 / transform.k}>
				{#each links as link, i (i)}
					{@const src = link.source as SimNode}
					{@const tgt = link.target as SimNode}
					{@const _ = tick /* trigger re-render on tick */}
					{@const dim =
						selectedId !== null && src.id !== selectedId && tgt.id !== selectedId}
					<line
						x1={src.x ?? 0}
						y1={src.y ?? 0}
						x2={tgt.x ?? 0}
						y2={tgt.y ?? 0}
						stroke={dim ? '#332a1f' : '#4a3d2e'}
						opacity={dim ? 0.3 : 0.7}
					/>
				{/each}
			</g>

			<!-- Nodes -->
			<g class="nodes">
				{#each nodes as node (node.id)}
					{@const _ = tick}
					{@const r = nodeRadius(node.degree)}
					{@const isSel = node.id === selectedId}
					{@const isHover = node.id === hoveredId}
					{@const isNeighbor = selectedNeighbors.has(node.id)}
					{@const dim = selectedId !== null && !isSel && !isNeighbor}
					<!-- svelte-ignore a11y_click_events_have_key_events -->
					<!-- svelte-ignore a11y_no_static_element_interactions -->
					<circle
						cx={node.x ?? 0}
						cy={node.y ?? 0}
						r={isSel ? r + 2 : r}
						fill={communityColor(node.community)}
						opacity={dim ? 0.2 : 1}
						stroke={isSel ? '#f0ebe4' : isHover ? '#c4b5a2' : 'transparent'}
						stroke-width={isSel ? 1.5 / transform.k : 1 / transform.k}
						class="cursor-pointer"
						onpointerdown={(e) => onPointerDown(e, node)}
						onpointerenter={() => (hoveredId = node.id)}
						onpointerleave={() => (hoveredId = null)}
						onclick={(e) => {
							e.stopPropagation();
							selectedId = node.id === selectedId ? null : node.id;
						}}
					>
						<title>{node.label} · {node.kind} · degree {node.degree}</title>
					</circle>
				{/each}
			</g>
		</g>
	</svg>

	<!-- Legend / stats -->
	<div class="absolute top-3 right-3 px-3 py-2 rounded-md
		bg-bourbon-900/60 border border-bourbon-800 backdrop-blur-sm
		text-[10px] font-mono text-bourbon-500 leading-relaxed pointer-events-none">
		<div>{snapshot.stats.node_count} nodes</div>
		<div>{snapshot.stats.edge_count} edges</div>
		<div>{snapshot.stats.community_count} communities</div>
	</div>

	<!-- Detail panel -->
	{#if selectedNode}
		<div class="absolute top-3 left-3 max-w-md p-4 rounded-lg
			bg-bourbon-900/90 border border-bourbon-700 backdrop-blur-sm
			shadow-xl">
			<div class="flex items-center gap-2 mb-2">
				<span
					class="w-2.5 h-2.5 rounded-full shrink-0"
					style:background-color={communityColor(selectedNode.community)}
				></span>
				<span class="font-display text-xs font-bold uppercase tracking-widest text-bourbon-200">
					{selectedNode.kind}
				</span>
				<span class="text-[10px] font-mono text-bourbon-600">
					community {selectedNode.community}
				</span>
			</div>
			<div class="text-bourbon-100 font-mono text-sm break-all mb-2">{selectedNode.label}</div>
			{#if selectedNode.sourceFile}
				<div class="text-[10px] font-mono text-bourbon-500 break-all">{selectedNode.sourceFile}</div>
			{/if}
			<div class="flex items-center gap-3 mt-3 pt-3 border-t border-bourbon-800 text-[10px] font-mono text-bourbon-500">
				<span>degree <span class="text-bourbon-300">{selectedNode.degree}</span></span>
				<span>neighbors <span class="text-bourbon-300">{selectedNeighbors.size}</span></span>
			</div>
		</div>
	{/if}
</div>
