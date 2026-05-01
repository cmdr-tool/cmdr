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
	import type { GraphSnapshot, GraphNode } from '$lib/api';
	import { communityColor, superCommunityColor } from './colors';

	// Zoom modes:
	//   'flat'   = the classic full-graph view (every node, hierarchical color)
	//   'super'  = bird's-eye: one super-node per super-community, aggregate edges
	//   'focus'  = zoomed into a single super-community: members + phantom
	//              boundary nodes for each connected external super-community
	type Mode = 'flat' | 'super' | 'focus';

	let {
		snapshot,
		selectedId = $bindable(null),
		mode = $bindable<Mode>('flat'),
		focusedSuperId = $bindable<number | null>(null),
		rebuilding = $bindable(false),
		onReady
	}: {
		snapshot: GraphSnapshot;
		selectedId?: string | null;
		mode?: Mode;
		focusedSuperId?: number | null;
		rebuilding?: boolean;
		onReady?: () => void;
	} = $props();

	type SimNode = SimulationNodeDatum & {
		id: string;
		label: string;
		kind: string;
		community: number;
		superCommunity: number;
		degree: number;
		sourceFile: string;
		// For phantom boundary nodes in focus mode:
		isPhantom?: boolean;
		phantomSuperId?: number;
		phantomCount?: number;
	};
	type SimLink = SimulationLinkDatum<SimNode> & { weight?: number };

	function nodeRadius(degree: number): number {
		return 4 + Math.sqrt(degree) * 1.4;
	}
	function superNodeRadius(memberCount: number): number {
		return 14 + Math.sqrt(memberCount) * 2.2;
	}
	function phantomRadius(weight: number): number {
		return 10 + Math.sqrt(weight) * 1.5;
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
	let cursor: { x: number; y: number } | null = $state(null);

	let dragging: { node: SimNode; pointerId: number; moved: boolean } | null = $state(null);
	// Set true during a drag's pointermove; checked in onCanvasClick so a
	// drag-then-release doesn't also count as a click (which would zoom
	// into the node we just dragged).
	let suppressNextClick = false;

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
		const m = mode;
		const fid = focusedSuperId;
		untrack(() => rebuild(snap, m, fid));
		return () => {
			simulation?.stop();
		};
	});

	$effect(() => {
		transform; hoveredId; selectedId;
		scheduleDraw();
	});

	let readyFired = false;

	function rebuild(snap: GraphSnapshot, m: Mode, fid: number | null) {
		simulation?.stop();
		readyFired = false;
		rebuilding = true;

		if (m === 'super') {
			buildSuperView(snap);
		} else if (m === 'focus' && fid !== null) {
			buildFocusView(snap, fid);
		} else {
			buildFlatView(snap);
		}

		requestAnimationFrame(() => {
			simulation = forceSimulation<SimNode, SimLink>(nodes)
				.force(
					'link',
					forceLink<SimNode, SimLink>(links)
						.id((d) => d.id)
						.distance((d) => (m === 'super' ? 100 : 45))
						.strength(0.5)
				)
				.force('charge', forceManyBody<SimNode>().strength(m === 'super' ? -400 : -120).distanceMax(400))
				.force('center', forceCenter(0, 0).strength(0.04))
				.force(
					'collide',
					forceCollide<SimNode>().radius((d) => {
						if (d.isPhantom) return phantomRadius(d.phantomCount ?? 1) + 2;
						if (m === 'super') return superNodeRadius((d as any).memberCount ?? 1) + 4;
						return nodeRadius(d.degree) + 2;
					})
				)
				.alphaDecay(0.04)
				.stop()
				.on('tick', scheduleDraw);

			simulation.tick(150);
			requestAnimationFrame(() => {
				fitToViewport();
				scheduleDraw();
			});
		});
	}

	function buildFlatView(snap: GraphSnapshot) {
		const nodeIds = new Set(snap.nodes.map((n) => n.id));
		nodes = snap.nodes.map((n) => ({
			id: n.id,
			label: n.label,
			kind: n.kind,
			community: n.community,
			superCommunity: n.super_community,
			degree: n.degree,
			sourceFile: n.source_file
		}));
		links = snap.edges
			.filter((e) => nodeIds.has(e.source) && nodeIds.has(e.target))
			.map((e) => ({ source: e.source, target: e.target }));
	}

	function buildSuperView(snap: GraphSnapshot) {
		// One super-node per super-community; edges are aggregate weights.
		const superMap = new Map<number, { memberCount: number; label: string; superId: number }>();
		for (const n of snap.nodes) {
			const sc = n.super_community;
			const cur = superMap.get(sc);
			if (cur) cur.memberCount++;
			else
				superMap.set(sc, {
					memberCount: 1,
					label: snap.super_communities?.[String(sc)]?.label ?? `super ${sc}`,
					superId: sc
				});
		}

		const nodeToSuper = new Map<string, number>();
		for (const n of snap.nodes) nodeToSuper.set(n.id, n.super_community);

		// Edge aggregation: for each edge, find the super-community pair.
		const edgeWeights = new Map<string, { source: number; target: number; weight: number }>();
		for (const e of snap.edges) {
			const s = nodeToSuper.get(e.source);
			const t = nodeToSuper.get(e.target);
			if (s === undefined || t === undefined || s === t) continue;
			const a = Math.min(s, t);
			const b = Math.max(s, t);
			const key = `${a}|${b}`;
			const cur = edgeWeights.get(key);
			if (cur) cur.weight++;
			else edgeWeights.set(key, { source: a, target: b, weight: 1 });
		}

		nodes = [...superMap.values()].map((s) => ({
			id: `super:${s.superId}`,
			label: s.label,
			kind: 'super',
			community: s.superId,
			superCommunity: s.superId,
			degree: s.memberCount,
			sourceFile: '',
			memberCount: s.memberCount as any
		}));
		links = [...edgeWeights.values()].map((e) => ({
			source: `super:${e.source}`,
			target: `super:${e.target}`,
			weight: e.weight
		}));
	}

	function buildFocusView(snap: GraphSnapshot, focusedSuperId: number) {
		// Internal nodes: members of the focused super-community.
		const internalNodes = snap.nodes.filter((n) => n.super_community === focusedSuperId);
		const internalIds = new Set(internalNodes.map((n) => n.id));
		const nodeToSuper = new Map<string, number>();
		for (const n of snap.nodes) nodeToSuper.set(n.id, n.super_community);

		// Aggregate cross-edges by external super-community to build phantom nodes.
		const phantomCounts = new Map<number, number>();
		// Track per-edge for rendering: each cross edge becomes (internalId → phantomId).
		const externalLinks: SimLink[] = [];
		const internalLinks: SimLink[] = [];

		for (const e of snap.edges) {
			const sIn = internalIds.has(e.source);
			const tIn = internalIds.has(e.target);
			if (sIn && tIn) {
				internalLinks.push({ source: e.source, target: e.target });
				continue;
			}
			if (sIn) {
				const otherSuper = nodeToSuper.get(e.target);
				if (otherSuper === undefined || otherSuper === focusedSuperId) continue;
				phantomCounts.set(otherSuper, (phantomCounts.get(otherSuper) ?? 0) + 1);
				externalLinks.push({ source: e.source, target: `phantom:${otherSuper}` });
			} else if (tIn) {
				const otherSuper = nodeToSuper.get(e.source);
				if (otherSuper === undefined || otherSuper === focusedSuperId) continue;
				phantomCounts.set(otherSuper, (phantomCounts.get(otherSuper) ?? 0) + 1);
				externalLinks.push({ source: e.target, target: `phantom:${otherSuper}` });
			}
		}

		const internalSimNodes: SimNode[] = internalNodes.map((n) => ({
			id: n.id,
			label: n.label,
			kind: n.kind,
			community: n.community,
			superCommunity: n.super_community,
			degree: n.degree,
			sourceFile: n.source_file
		}));
		const phantomSimNodes: SimNode[] = [...phantomCounts.entries()].map(([sc, count]) => ({
			id: `phantom:${sc}`,
			label: snap.super_communities?.[String(sc)]?.label ?? `super ${sc}`,
			kind: 'phantom',
			community: sc,
			superCommunity: sc,
			degree: count,
			sourceFile: '',
			isPhantom: true,
			phantomSuperId: sc,
			phantomCount: count
		}));

		nodes = [...internalSimNodes, ...phantomSimNodes];
		links = [...internalLinks, ...externalLinks];
	}

	function fitToViewport() {
		if (!canvas || nodes.length === 0) return;
		const w = canvas.clientWidth;
		const h = canvas.clientHeight;
		if (!zoomBehavior || w === 0 || h === 0) {
			requestAnimationFrame(fitToViewport);
			return;
		}
		let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
		for (const n of nodes) {
			const x = n.x ?? 0;
			const y = n.y ?? 0;
			if (x < minX) minX = x;
			if (x > maxX) maxX = x;
			if (y < minY) minY = y;
			if (y > maxY) maxY = y;
		}
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

		if (!readyFired) {
			readyFired = true;
			rebuilding = false;
			onReady?.();
		}
	}

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

	function draw() {
		if (!ctx || !canvas) return;
		const w = canvas.clientWidth;
		const h = canvas.clientHeight;
		ctx.save();
		ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
		ctx.clearRect(0, 0, w, h);

		ctx.translate(w / 2 + transform.x, h / 2 + transform.y);
		ctx.scale(transform.k, transform.k);

		const neighbors = selectedNeighbors;
		const hasSelection = selectedId !== null;

		// Edges
		for (const link of links) {
			const src = link.source as SimNode;
			const tgt = link.target as SimNode;
			const dim = hasSelection && src.id !== selectedId && tgt.id !== selectedId;
			const baseW = mode === 'super' ? 0.7 + Math.sqrt(link.weight ?? 1) * 0.4 : 0.7;
			ctx.lineWidth = baseW / transform.k;
			// Phantom edges (cross-cutting) get a dashed style.
			if (tgt.isPhantom || src.isPhantom) {
				ctx.setLineDash([4 / transform.k, 3 / transform.k]);
				ctx.strokeStyle = dim ? 'rgba(150,128,90,0.25)' : 'rgba(180,150,100,0.55)';
			} else {
				ctx.setLineDash([]);
				ctx.strokeStyle = dim ? 'rgba(74,61,46,0.3)' : 'rgba(74,61,46,0.7)';
			}
			ctx.beginPath();
			ctx.moveTo(src.x ?? 0, src.y ?? 0);
			ctx.lineTo(tgt.x ?? 0, tgt.y ?? 0);
			ctx.stroke();
		}
		ctx.setLineDash([]);

		// Nodes
		for (const node of nodes) {
			const isSel = node.id === selectedId;
			const isHover = node.id === hoveredId;
			const isNeighbor = neighbors.has(node.id);
			const dim = hasSelection && !isSel && !isNeighbor;

			let r: number;
			if (node.isPhantom) r = phantomRadius(node.phantomCount ?? 1);
			else if (mode === 'super') r = superNodeRadius((node as any).memberCount ?? 1);
			else r = isSel ? nodeRadius(node.degree) + 2 : nodeRadius(node.degree);

			const x = node.x ?? 0;
			const y = node.y ?? 0;

			ctx.globalAlpha = dim ? 0.2 : 1;

			// Phantom nodes: hollow ring in their super-community color.
			if (node.isPhantom) {
				ctx.strokeStyle = superCommunityColor(node.phantomSuperId ?? 0);
				ctx.lineWidth = 2 / transform.k;
				ctx.fillStyle = 'rgba(20,16,10,0.7)';
				ctx.beginPath();
				ctx.arc(x, y, r, 0, Math.PI * 2);
				ctx.fill();
				ctx.stroke();
			} else {
				ctx.fillStyle =
					mode === 'super'
						? superCommunityColor(node.superCommunity)
						: communityColor(node.community, node.superCommunity);
				ctx.beginPath();
				ctx.arc(x, y, r, 0, Math.PI * 2);
				ctx.fill();
			}

			if (isSel || isHover) {
				ctx.strokeStyle = isSel ? '#f0ebe4' : '#c4b5a2';
				ctx.lineWidth = (isSel ? 1.5 : 1) / transform.k;
				ctx.stroke();
			}

			// Super-view labels: light text with a dark halo so it reads
			// on any node color regardless of background luminance.
			if (mode === 'super' && r > 16) {
				const label = node.label.length > 22 ? node.label.slice(0, 20) + '…' : node.label;
				const fontPx = Math.max(10, Math.min(13, r * 0.55));
				ctx.font = `${fontPx / transform.k}px ui-monospace, monospace`;
				ctx.textAlign = 'center';
				ctx.textBaseline = 'middle';
				ctx.lineWidth = 3 / transform.k;
				ctx.lineJoin = 'round';
				ctx.strokeStyle = 'rgba(20,16,10,0.85)';
				ctx.strokeText(label, x, y);
				ctx.fillStyle = '#f5efe6';
				ctx.fillText(label, x, y);
			}

			ctx.globalAlpha = 1;
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

	function findNodeAt(clientX: number, clientY: number): SimNode | null {
		const { x, y } = clientToLocal(clientX, clientY);
		for (let i = nodes.length - 1; i >= 0; i--) {
			const n = nodes[i];
			let r: number;
			if (n.isPhantom) r = phantomRadius(n.phantomCount ?? 1);
			else if (mode === 'super') r = superNodeRadius((n as any).memberCount ?? 1);
			else r = nodeRadius(n.degree);
			const dx = (n.x ?? 0) - x;
			const dy = (n.y ?? 0) - y;
			if (dx * dx + dy * dy <= r * r) return n;
		}
		return null;
	}

	function onPointerMove(e: PointerEvent) {
		if (dragging && canvas) {
			const { x, y } = clientToLocal(e.clientX, e.clientY);
			dragging.node.fx = x;
			dragging.node.fy = y;
			dragging.moved = true;
			cursor = null;
			return;
		}
		const hit = findNodeAt(e.clientX, e.clientY);
		const newId = hit?.id ?? null;
		if (newId !== hoveredId) hoveredId = newId;
		cursor = hit ? { x: e.clientX, y: e.clientY } : null;
	}

	function onPointerLeave() {
		hoveredId = null;
		cursor = null;
	}

	function onPointerDown(e: PointerEvent) {
		const hit = findNodeAt(e.clientX, e.clientY);
		if (!hit || !canvas) return;
		e.stopPropagation();
		canvas.setPointerCapture(e.pointerId);
		dragging = { node: hit, pointerId: e.pointerId, moved: false };
		hit.fx = hit.x;
		hit.fy = hit.y;
		simulation?.alphaTarget(0.3).restart();
	}

	function onPointerUp(e: PointerEvent) {
		if (!dragging || dragging.pointerId !== e.pointerId) return;
		// If the user actually moved during this gesture, this was a drag,
		// not a tap — swallow the synthesized click that follows.
		if (dragging.moved) suppressNextClick = true;
		dragging.node.fx = null;
		dragging.node.fy = null;
		simulation?.alphaTarget(0);
		dragging = null;
	}

	function onCanvasClick(e: MouseEvent) {
		if (suppressNextClick) {
			suppressNextClick = false;
			return;
		}
		const hit = findNodeAt(e.clientX, e.clientY);
		if (!hit) {
			selectedId = null;
			return;
		}
		// Super-view: click a super-node → enter focus mode for it.
		if (mode === 'super' && hit.id.startsWith('super:')) {
			focusedSuperId = parseInt(hit.id.slice('super:'.length), 10);
			mode = 'focus';
			selectedId = null;
			return;
		}
		// Focus-view: click a phantom → re-focus on that other super.
		if (mode === 'focus' && hit.isPhantom && hit.phantomSuperId !== undefined) {
			focusedSuperId = hit.phantomSuperId;
			selectedId = null;
			return;
		}
		// Otherwise toggle selection on the actual node.
		selectedId = hit.id === selectedId ? null : hit.id;
	}

	onMount(() => {
		if (!canvas) return;
		ctx = canvas.getContext('2d');

		zoomBehavior = zoom<HTMLCanvasElement, unknown>()
			.scaleExtent([0.1, 8])
			.filter((event) => {
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

	{#if hoveredNode && cursor && !dragging}
		<div
			class="fixed z-30 pointer-events-none px-2 py-1 rounded
				bg-bourbon-900/95 border border-bourbon-700 backdrop-blur-sm
				text-[10px] font-mono text-bourbon-200 shadow-lg
				whitespace-nowrap"
			style:left="{cursor.x + 12}px"
			style:top="{cursor.y + 12}px"
		>
			{hoveredNode.label}
			<span class="text-bourbon-500">
				{#if hoveredNode.isPhantom}
					· portal · {hoveredNode.phantomCount} edges
				{:else if mode === 'super'}
					· {(hoveredNode as any).memberCount ?? 0} members
				{:else}
					· {hoveredNode.kind}
				{/if}
			</span>
		</div>
	{/if}
</div>
