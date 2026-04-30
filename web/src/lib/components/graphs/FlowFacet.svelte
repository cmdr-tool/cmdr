<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { hierarchy, tree, type HierarchyPointNode } from 'd3-hierarchy';
	import { zoom, zoomIdentity, type ZoomBehavior } from 'd3-zoom';
	import { select } from 'd3-selection';
	import type { GraphSnapshot, GraphNode, GraphEdge } from '$lib/api';
	import { communityColor } from './colors';

	let {
		snapshot,
		selectedId = $bindable(null),
		depth = $bindable(2)
	}: {
		snapshot: GraphSnapshot;
		selectedId?: string | null;
		depth?: number;
	} = $props();

	type FlowNode = {
		id: string;
		label: string;
		kind: string;
		community: number;
		degree: number;
		children: FlowNode[];
		// Set after layout
		x?: number;
		y?: number;
	};

	let canvas: HTMLCanvasElement | null = $state(null);
	let viewport: HTMLDivElement | null = $state(null);
	let ctx: CanvasRenderingContext2D | null = null;
	let dpr = 1;

	let transform = $state({ x: 0, y: 0, k: 1 });
	let hoveredId: string | null = $state(null);
	let cursor: { x: number; y: number } | null = $state(null);
	let zoomBehavior: ZoomBehavior<HTMLCanvasElement, unknown> | null = null;

	// Build a node lookup + adjacency map keyed on outgoing flow edges
	// (calls / imports / uses_type — what does X "lead to").
	let nodesById = $derived.by(() => {
		const m = new Map<string, GraphNode>();
		for (const n of snapshot.nodes) m.set(n.id, n);
		return m;
	});

	let outgoing = $derived.by(() => {
		const m = new Map<string, GraphEdge[]>();
		const flowRel = new Set(['calls', 'imports', 'uses_type']);
		for (const e of snapshot.edges) {
			if (!flowRel.has(e.relation)) continue;
			const arr = m.get(e.source) ?? [];
			arr.push(e);
			m.set(e.source, arr);
		}
		return m;
	});

	// Build a depth-limited tree from selectedId via BFS. Each node
	// appears at most once (its first parent wins) so the result is a
	// proper tree d3-hierarchy can lay out.
	let layoutRoot = $derived.by<HierarchyPointNode<FlowNode> | null>(() => {
		if (!selectedId) return null;
		const rootInfo = nodesById.get(selectedId);
		if (!rootInfo) return null;

		const seen = new Set<string>([selectedId]);
		const buildChildren = (id: string, remaining: number): FlowNode[] => {
			if (remaining <= 0) return [];
			const out: FlowNode[] = [];
			for (const e of outgoing.get(id) ?? []) {
				if (seen.has(e.target)) continue;
				seen.add(e.target);
				const tgt = nodesById.get(e.target);
				if (!tgt) continue;
				out.push({
					id: tgt.id,
					label: tgt.label,
					kind: tgt.kind,
					community: tgt.community,
					degree: tgt.degree,
					children: buildChildren(tgt.id, remaining - 1)
				});
			}
			return out;
		};

		const treeData: FlowNode = {
			id: rootInfo.id,
			label: rootInfo.label,
			kind: rootInfo.kind,
			community: rootInfo.community,
			degree: rootInfo.degree,
			children: buildChildren(rootInfo.id, depth)
		};

		// Tidy-tree layout, bottom-up (so root is at top).
		// Sizing logic: each level gets ~120px vertical, each leaf ~150px
		// horizontal. d3-hierarchy auto-fits using these bounds.
		const root = hierarchy(treeData);
		const leafCount = root.leaves().length;
		const levelCount = root.height + 1;
		const height = Math.max(400, levelCount * 120);
		const width = Math.max(600, leafCount * 150);
		const layout = tree<FlowNode>().size([width, height]);
		return layout(root);
	});

	let leafCount = $derived(layoutRoot?.leaves().length ?? 0);

	function nodeRadius(degree: number): number {
		return 5 + Math.sqrt(Math.max(0, degree)) * 1.4;
	}

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
		// Trigger redraw on layout changes / hover / transform
		layoutRoot;
		hoveredId;
		transform;
		scheduleDraw();
	});

	function resizeCanvas() {
		if (!canvas) return;
		const w = canvas.clientWidth;
		const h = canvas.clientHeight;
		if (w === 0 || h === 0) return;
		dpr = window.devicePixelRatio || 1;
		canvas.width = Math.round(w * dpr);
		canvas.height = Math.round(h * dpr);
		scheduleDraw();
	}

	function fitToViewport() {
		if (!canvas || !zoomBehavior || !layoutRoot) return;
		const w = canvas.clientWidth;
		const h = canvas.clientHeight;
		if (w === 0 || h === 0) {
			requestAnimationFrame(fitToViewport);
			return;
		}
		// Compute bbox of laid-out positions.
		let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
		layoutRoot.each((d) => {
			if (d.x < minX) minX = d.x;
			if (d.x > maxX) maxX = d.x;
			if (d.y < minY) minY = d.y;
			if (d.y > maxY) maxY = d.y;
		});
		if (maxX === minX || maxY === minY) return;
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

	// Re-fit when the layout changes (selection or depth).
	$effect(() => {
		layoutRoot;
		requestAnimationFrame(fitToViewport);
	});

	function draw() {
		if (!ctx || !canvas) return;
		const w = canvas.clientWidth;
		const h = canvas.clientHeight;
		ctx.save();
		ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
		ctx.clearRect(0, 0, w, h);

		if (!layoutRoot) {
			ctx.restore();
			return;
		}

		// Camera transform: center origin then zoom/pan
		ctx.translate(w / 2 + transform.x, h / 2 + transform.y);
		ctx.scale(transform.k, transform.k);

		// Edges first
		ctx.strokeStyle = 'rgba(74,61,46,0.7)';
		ctx.lineWidth = 0.8 / transform.k;
		layoutRoot.each((d) => {
			if (!d.parent) return;
			ctx!.beginPath();
			ctx!.moveTo(d.parent.x, d.parent.y);
			ctx!.lineTo(d.x, d.y);
			ctx!.stroke();
		});

		// Nodes
		layoutRoot.each((d) => {
			const isHover = d.data.id === hoveredId;
			const isRoot = d.depth === 0;
			const r = nodeRadius(d.data.degree);
			ctx!.fillStyle = communityColor(d.data.community);
			ctx!.beginPath();
			ctx!.arc(d.x, d.y, isRoot ? r + 2 : r, 0, Math.PI * 2);
			ctx!.fill();

			if (isRoot || isHover) {
				ctx!.strokeStyle = isRoot ? '#f0ebe4' : '#c4b5a2';
				ctx!.lineWidth = (isRoot ? 1.5 : 1) / transform.k;
				ctx!.stroke();
			}
		});

		// Labels under each node (only when zoomed in enough to be readable)
		if (transform.k > 0.4) {
			ctx.fillStyle = '#a89580';
			ctx.font = `${Math.max(10, 11 / transform.k)}px ui-monospace, SFMono-Regular, Menlo, monospace`;
			ctx.textAlign = 'center';
			ctx.textBaseline = 'top';
			layoutRoot.each((d) => {
				const label = d.data.label.length > 28 ? d.data.label.slice(0, 26) + '…' : d.data.label;
				ctx!.fillText(label, d.x, d.y + nodeRadius(d.data.degree) + 4);
			});
		}

		ctx.restore();
	}

	function clientToLocal(clientX: number, clientY: number) {
		if (!canvas) return { x: 0, y: 0 };
		const rect = canvas.getBoundingClientRect();
		return {
			x: (clientX - rect.left - rect.width / 2 - transform.x) / transform.k,
			y: (clientY - rect.top - rect.height / 2 - transform.y) / transform.k
		};
	}

	function findNodeAt(clientX: number, clientY: number): FlowNode | null {
		if (!layoutRoot) return null;
		const { x, y } = clientToLocal(clientX, clientY);
		let hit: FlowNode | null = null;
		layoutRoot.each((d) => {
			const dx = d.x - x;
			const dy = d.y - y;
			const r = nodeRadius(d.data.degree);
			if (dx * dx + dy * dy <= r * r) hit = d.data;
		});
		return hit;
	}

	function onPointerMove(e: PointerEvent) {
		const hit = findNodeAt(e.clientX, e.clientY);
		hoveredId = hit?.id ?? null;
		cursor = hit ? { x: e.clientX, y: e.clientY } : null;
	}

	function onPointerLeave() {
		hoveredId = null;
		cursor = null;
	}

	function onCanvasClick(e: MouseEvent) {
		const hit = findNodeAt(e.clientX, e.clientY);
		if (hit && hit.id !== selectedId) {
			selectedId = hit.id;
		}
	}

	let hoveredFlowNode = $derived.by((): FlowNode | null => {
		if (!hoveredId || !layoutRoot) return null;
		let found: FlowNode | null = null;
		layoutRoot.each((d) => {
			if (d.data.id === hoveredId) found = d.data;
		});
		return found;
	});

	onMount(() => {
		if (!canvas) return;
		ctx = canvas.getContext('2d');

		zoomBehavior = zoom<HTMLCanvasElement, unknown>()
			.scaleExtent([0.1, 8])
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

	onDestroy(() => {});
</script>

<div bind:this={viewport} class="relative w-full h-full overflow-hidden">
	{#if !selectedId}
		<div class="absolute inset-0 flex items-center justify-center text-bourbon-500">
			<div class="max-w-md text-center">
				<div class="font-display text-xs font-bold uppercase tracking-widest text-run-500 mb-2">flow</div>
				<p class="text-sm text-bourbon-400">
					Pick a node from the sidebar to see what it calls,
					<br />imports, or depends on — recursively.
				</p>
			</div>
		</div>
	{:else if !layoutRoot || layoutRoot.descendants().length === 1}
		<!-- svelte-ignore a11y_click_events_have_key_events -->
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<canvas
			bind:this={canvas}
			class="w-full h-full block"
			onpointermove={onPointerMove}
			onpointerleave={onPointerLeave}
			onclick={onCanvasClick}
		></canvas>
		<div class="absolute inset-0 flex items-center justify-center pointer-events-none">
			<div class="max-w-md text-center bg-bourbon-900/80 border border-bourbon-800 backdrop-blur-sm rounded-lg px-5 py-4">
				<div class="font-display text-xs font-bold uppercase tracking-widest text-bourbon-500 mb-1">leaf node</div>
				<p class="text-sm text-bourbon-400">
					{nodesById.get(selectedId)?.label ?? selectedId} has no outgoing flow.
				</p>
			</div>
		</div>
	{:else}
		<!-- svelte-ignore a11y_click_events_have_key_events -->
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<canvas
			bind:this={canvas}
			class="w-full h-full block cursor-grab"
			class:cursor-pointer={hoveredId !== null}
			onpointermove={onPointerMove}
			onpointerleave={onPointerLeave}
			onclick={onCanvasClick}
		></canvas>

		{#if hoveredFlowNode && cursor}
			<div
				class="fixed z-30 pointer-events-none px-2 py-1 rounded
					bg-bourbon-900/95 border border-bourbon-700 backdrop-blur-sm
					text-[10px] font-mono text-bourbon-200 shadow-lg
					whitespace-nowrap"
				style:left="{cursor.x + 12}px"
				style:top="{cursor.y + 12}px"
			>
				{hoveredFlowNode.label}
				<span class="text-bourbon-500"> · {hoveredFlowNode.kind}</span>
			</div>
		{/if}

		<!-- Depth indicator + leaf count, bottom-left -->
		<div class="absolute bottom-3 left-3 px-3 py-2 rounded-md
			bg-bourbon-900/70 border border-bourbon-800 backdrop-blur-sm
			text-[10px] font-mono text-bourbon-500 leading-relaxed pointer-events-none">
			depth {depth} · {leafCount} leaves
		</div>
	{/if}
</div>
