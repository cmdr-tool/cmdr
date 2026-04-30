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

	let canvas: HTMLCanvasElement | null = $state(null);
	let viewport: HTMLDivElement | null = $state(null);
	let ctx: CanvasRenderingContext2D | null = null;
	let dpr = 1;

	let nodes: SimNode[] = $state([]);
	let links: SimLink[] = $state([]);
	let simulation: Simulation<SimNode, SimLink> | null = null;
	let zoomBehavior: ZoomBehavior<HTMLCanvasElement, unknown> | null = null;

	let transform = $state({ x: 0, y: 0, k: 1 });
	let hoveredId: string | null = $state(null);
	let selectedId: string | null = $state(null);
	let cursor: { x: number; y: number } | null = $state(null);

	// Drag state — native pointer events.
	let dragging: { node: SimNode; pointerId: number } | null = $state(null);

	// rAF redraw batching: multiple state changes within one frame coalesce
	// into a single draw call.
	let drawScheduled = false;
	function scheduleDraw() {
		if (drawScheduled) return;
		drawScheduled = true;
		requestAnimationFrame(() => {
			drawScheduled = false;
			draw();
		});
	}

	$effect(() => {
		const snap = snapshot;
		untrack(() => rebuild(snap));
		return () => {
			simulation?.stop();
		};
	});

	// Redraw on any visual state change.
	$effect(() => {
		// Read these so the effect re-runs when they change.
		transform; hoveredId; selectedId;
		scheduleDraw();
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
			.map((e) => ({ source: e.source, target: e.target }));

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
			.on('tick', scheduleDraw);

		// Settle layout synchronously so the user sees a stable graph
		// instantly, no animation phase.
		simulation.tick(150);
		requestAnimationFrame(() => {
			fitToViewport();
			scheduleDraw();
		});
	}

	function fitToViewport() {
		if (!canvas || !zoomBehavior || nodes.length === 0) return;
		let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
		for (const n of nodes) {
			const x = n.x ?? 0;
			const y = n.y ?? 0;
			if (x < minX) minX = x;
			if (x > maxX) maxX = x;
			if (y < minY) minY = y;
			if (y > maxY) maxY = y;
		}
		const w = canvas.clientWidth;
		const h = canvas.clientHeight;
		if (w === 0 || h === 0 || maxX === minX || maxY === minY) return;
		const padding = 60;
		const scale = Math.min(
			(w - padding * 2) / (maxX - minX),
			(h - padding * 2) / (maxY - minY),
			1.5
		);
		const cx = (minX + maxX) / 2;
		const cy = (minY + maxY) / 2;
		select(canvas).call(
			zoomBehavior.transform,
			zoomIdentity.translate(-cx * scale, -cy * scale).scale(scale)
		);
	}

	function resizeCanvas() {
		if (!canvas) return;
		dpr = window.devicePixelRatio || 1;
		const w = canvas.clientWidth;
		const h = canvas.clientHeight;
		canvas.width = Math.round(w * dpr);
		canvas.height = Math.round(h * dpr);
		scheduleDraw();
	}

	function draw() {
		if (!ctx || !canvas) return;
		const w = canvas.clientWidth;
		const h = canvas.clientHeight;
		ctx.save();
		ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
		ctx.clearRect(0, 0, w, h);

		// Camera transform: center origin then apply zoom/pan
		ctx.translate(w / 2 + transform.x, h / 2 + transform.y);
		ctx.scale(transform.k, transform.k);

		const neighbors = selectedNeighbors;
		const hasSelection = selectedId !== null;

		// Edges first (background layer)
		ctx.lineWidth = 0.7 / transform.k;
		for (const link of links) {
			const src = link.source as SimNode;
			const tgt = link.target as SimNode;
			const dim = hasSelection && src.id !== selectedId && tgt.id !== selectedId;
			ctx.strokeStyle = dim ? 'rgba(74,61,46,0.3)' : 'rgba(74,61,46,0.7)';
			ctx.beginPath();
			ctx.moveTo(src.x ?? 0, src.y ?? 0);
			ctx.lineTo(tgt.x ?? 0, tgt.y ?? 0);
			ctx.stroke();
		}

		// Nodes
		for (const node of nodes) {
			const isSel = node.id === selectedId;
			const isHover = node.id === hoveredId;
			const isNeighbor = neighbors.has(node.id);
			const dim = hasSelection && !isSel && !isNeighbor;
			const r = isSel ? nodeRadius(node.degree) + 2 : nodeRadius(node.degree);
			const x = node.x ?? 0;
			const y = node.y ?? 0;

			ctx.globalAlpha = dim ? 0.2 : 1;
			ctx.fillStyle = communityColor(node.community);
			ctx.beginPath();
			ctx.arc(x, y, r, 0, Math.PI * 2);
			ctx.fill();

			if (isSel || isHover) {
				ctx.strokeStyle = isSel ? '#f0ebe4' : '#c4b5a2';
				ctx.lineWidth = (isSel ? 1.5 : 1) / transform.k;
				ctx.stroke();
			}
			ctx.globalAlpha = 1;
		}

		ctx.restore();
	}

	// Convert client coords to graph-local coords (after zoom transform).
	function clientToLocal(clientX: number, clientY: number) {
		if (!canvas) return { x: 0, y: 0 };
		const rect = canvas.getBoundingClientRect();
		return {
			x: (clientX - rect.left - rect.width / 2 - transform.x) / transform.k,
			y: (clientY - rect.top - rect.height / 2 - transform.y) / transform.k
		};
	}

	function findNodeAt(clientX: number, clientY: number): SimNode | null {
		const { x, y } = clientToLocal(clientX, clientY);
		// Iterate in reverse so the topmost-rendered node wins on overlap.
		for (let i = nodes.length - 1; i >= 0; i--) {
			const n = nodes[i];
			const dx = (n.x ?? 0) - x;
			const dy = (n.y ?? 0) - y;
			const r = nodeRadius(n.degree);
			if (dx * dx + dy * dy <= r * r) return n;
		}
		return null;
	}

	function onPointerMove(e: PointerEvent) {
		if (dragging && canvas) {
			const { x, y } = clientToLocal(e.clientX, e.clientY);
			dragging.node.fx = x;
			dragging.node.fy = y;
			cursor = null;
			return;
		}
		const hit = findNodeAt(e.clientX, e.clientY);
		const newId = hit?.id ?? null;
		if (newId !== hoveredId) {
			hoveredId = newId;
		}
		cursor = hit ? { x: e.clientX, y: e.clientY } : null;
	}

	function onPointerLeave() {
		hoveredId = null;
		cursor = null;
	}

	function onPointerDown(e: PointerEvent) {
		const hit = findNodeAt(e.clientX, e.clientY);
		if (!hit || !canvas) return;
		// Stop d3-zoom from interpreting this as a pan when starting on a node.
		e.stopPropagation();
		canvas.setPointerCapture(e.pointerId);
		dragging = { node: hit, pointerId: e.pointerId };
		hit.fx = hit.x;
		hit.fy = hit.y;
		simulation?.alphaTarget(0.3).restart();
	}

	function onPointerUp(e: PointerEvent) {
		if (!dragging || dragging.pointerId !== e.pointerId) return;
		dragging.node.fx = null;
		dragging.node.fy = null;
		simulation?.alphaTarget(0);
		dragging = null;
	}

	function onCanvasClick(e: MouseEvent) {
		const hit = findNodeAt(e.clientX, e.clientY);
		selectedId = hit ? (hit.id === selectedId ? null : hit.id) : null;
	}

	onMount(() => {
		if (!canvas) return;
		ctx = canvas.getContext('2d');

		zoomBehavior = zoom<HTMLCanvasElement, unknown>()
			.scaleExtent([0.1, 8])
			.filter((event) => {
				// Let pointerdown on a node start a drag instead of a pan.
				if (event.type === 'mousedown' || event.type === 'pointerdown') {
					return findNodeAt(event.clientX, event.clientY) === null;
				}
				return !event.ctrlKey && !event.button;
			})
			.on('zoom', (event) => {
				transform = { x: event.transform.x, y: event.transform.y, k: event.transform.k };
			});
		select(canvas).call(zoomBehavior);

		resizeCanvas();
		const ro = new ResizeObserver(resizeCanvas);
		ro.observe(canvas);

		return () => {
			ro.disconnect();
		};
	});

	onDestroy(() => {
		simulation?.stop();
	});

	let selectedNode = $derived(
		selectedId ? nodes.find((n) => n.id === selectedId) ?? null : null
	);
	let selectedNeighbors = $derived.by(() => {
		if (!selectedId) return new Set<string>();
		const out = new Set<string>();
		for (const link of links) {
			const sId = typeof link.source === 'object' ? (link.source as SimNode).id : link.source;
			const tId = typeof link.target === 'object' ? (link.target as SimNode).id : link.target;
			if (sId === selectedId) out.add(tId as string);
			if (tId === selectedId) out.add(sId as string);
		}
		return out;
	});

	let hoveredNode = $derived(
		hoveredId ? nodes.find((n) => n.id === hoveredId) ?? null : null
	);
</script>

<div bind:this={viewport} class="relative w-full h-full overflow-hidden">
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<canvas
		bind:this={canvas}
		class="w-full h-full block cursor-grab"
		class:cursor-pointer={hoveredId !== null}
		class:cursor-grabbing={dragging !== null}
		onpointermove={onPointerMove}
		onpointerdown={onPointerDown}
		onpointerup={onPointerUp}
		onpointercancel={onPointerUp}
		onpointerleave={onPointerLeave}
		onclick={onCanvasClick}
	></canvas>

	<!-- Hover tooltip -->
	{#if hoveredNode && cursor && !dragging}
		<div
			class="absolute pointer-events-none px-2 py-1 rounded
				bg-bourbon-900/95 border border-bourbon-700 backdrop-blur-sm
				text-[10px] font-mono text-bourbon-200 shadow-lg
				whitespace-nowrap"
			style:left="{cursor.x + 12}px"
			style:top="{cursor.y + 12}px"
		>
			{hoveredNode.label}
			<span class="text-bourbon-500"> · {hoveredNode.kind}</span>
		</div>
	{/if}

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
