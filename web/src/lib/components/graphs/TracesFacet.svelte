<script lang="ts">
	import { onMount } from 'svelte';
	import * as dagre from '@dagrejs/dagre';
	import { zoom, zoomIdentity, type ZoomBehavior } from 'd3-zoom';
	import { select } from 'd3-selection';
	import { X, ArrowRight, ExternalLink, History, ChevronRight } from 'lucide-svelte';
	import { openInEditor, type Trace, type TraceStep, type ChangeSummary, type TraceEvent } from '$lib/api';

	type ActivityEvent = {
		id: number;
		kind: 'tool' | 'text' | 'error' | 'phase';
		text: string;
	};

	type Props = {
		trace: Trace | null;
		loading?: boolean;
		generating?: boolean;
		emptyMessage?: string;
		repoPath?: string;
		// Optional previous-version data for the current/previous toggle.
		previousTrace?: Trace | null;
		changeSummary?: ChangeSummary | null;
		view?: 'current' | 'previous';
		// SSE activity stream (events for in-flight runs).
		activity?: ActivityEvent[];
		onViewChange?: (view: 'current' | 'previous') => void;
		onNavigate?: (nodeId: string) => void;
		onReady?: () => void;
	};

	let {
		trace,
		loading = false,
		generating = false,
		emptyMessage = 'Select a trace from the sidebar.',
		repoPath,
		previousTrace = null,
		changeSummary = null,
		view = 'current',
		activity = [],
		onViewChange,
		onNavigate,
		onReady
	}: Props = $props();

	// Show only the trailing window of activity events so the panel
	// doesn't keep growing through a long generation run. New events
	// push older ones off the bottom — a tail rather than a log.
	const ACTIVITY_TAIL = 5;
	let recentActivity = $derived(activity.slice(-ACTIVITY_TAIL));

	// Whichever version is being shown drives the layout. Callouts only
	// render in 'previous' view because the spec calls for "show what
	// changed by overlaying it on the previous DAG."
	let activeTrace = $derived(view === 'previous' ? previousTrace : trace);
	let calloutsByStepID = $derived.by(() => {
		const map = new Map<string, { kind: string; description: string }[]>();
		if (view !== 'previous' || !changeSummary) return map;
		for (const ch of changeSummary.changes) {
			const key = ch.previous_step_id;
			if (!key) continue;
			const list = map.get(key) ?? [];
			list.push({ kind: ch.kind, description: ch.description });
			map.set(key, list);
		}
		return map;
	});

	// Anchorless callouts (added in current with no previous step) show in
	// a side panel rather than on the canvas.
	let anchorlessCallouts = $derived.by(() => {
		if (view !== 'previous' || !changeSummary) return [];
		return changeSummary.changes.filter((c) => !c.previous_step_id);
	});

	let canvas: HTMLCanvasElement | null = $state(null);
	let ctx: CanvasRenderingContext2D | null = null;
	let dpr = 1;
	let transform = $state({ x: 0, y: 0, k: 1 });
	let canvasW = $state(0);
	let canvasH = $state(0);
	let zoomBehavior: ZoomBehavior<HTMLCanvasElement, unknown> | null = null;
	let hoveredStepId: string | null = $state(null);
	let pinnedStepId: string | null = $state(null);
	let hoveredEdgeIdx: number | null = $state(null);
	let cursor: { x: number; y: number } | null = $state(null);

	// Rebuilt every draw frame; not reactive, used only for edge-label hit testing.
	let edgeLabelBoxes: Array<{ idx: number; x: number; y: number; w: number; h: number }> = [];

	// Elapsed seconds since the current generation run started. Counts from
	// when the panel opens, not from a backend-tracked run start — close+reopen
	// resets the counter, which is fine for a UX hint.
	let elapsed = $state(0);
	$effect(() => {
		if (!generating) {
			elapsed = 0;
			return;
		}
		const start = Date.now();
		elapsed = 0;
		let timeoutId: ReturnType<typeof setTimeout>;
		const tick = () => {
			elapsed = Math.floor((Date.now() - start) / 1000);
			timeoutId = setTimeout(tick, 1000);
		};
		timeoutId = setTimeout(tick, 1000);
		return () => clearTimeout(timeoutId);
	});

	function formatElapsed(s: number): string {
		const m = Math.floor(s / 60);
		const r = s % 60;
		return `${m}:${String(r).padStart(2, '0')}`;
	}

	type LayoutNode = {
		id: string;
		x: number;
		y: number;
		w: number;
		h: number;
		step: TraceStep;
	};
	type LayoutEdge = {
		points: Array<{ x: number; y: number }>;
		condition?: string;
		inferred: boolean;
	};

	let layout = $derived.by<{ nodes: LayoutNode[]; edges: LayoutEdge[] } | null>(() => {
		if (!activeTrace) return null;
		const g = new dagre.graphlib.Graph()
			.setGraph({
				rankdir: 'TB',
				nodesep: 60,
				ranksep: 110,
				edgesep: 30,
				marginx: 30,
				marginy: 30
			})
			.setDefaultEdgeLabel(() => ({}));

		const stepById = new Map<string, TraceStep>();
		for (const step of activeTrace.steps) {
			stepById.set(step.id, step);
			const labelLen = step.label.length;
			const w = Math.max(220, Math.min(360, labelLen * 7 + 60));
			const reqCount = step.requires?.length ?? 0;
			const hasDesc = !!(step.description && step.description.trim());
			// Description reserves space for up to 3 wrapped lines (10px font, 13px line-height = 39px + bottom gap).
			const h = 56 + (hasDesc ? 42 : 0) + (reqCount > 0 ? 14 : 0);
			g.setNode(step.id, { width: w, height: h, step });
		}
		for (const step of activeTrace.steps) {
			for (const next of step.next ?? []) {
				const target = stepById.get(next.to);
				if (!target) continue;
				const inferred = step.provenance === 'inferred' || target.provenance === 'inferred';
				g.setEdge(step.id, next.to, { condition: next.condition ?? '', inferred });
			}
		}
		dagre.layout(g);

		const nodes: LayoutNode[] = g.nodes().map((id) => {
			const n = g.node(id) as any;
			return { id, x: n.x, y: n.y, w: n.width, h: n.height, step: n.step };
		});
		const edges: LayoutEdge[] = g.edges().map((e) => {
			const ed = g.edge(e) as any;
			return { points: ed.points, condition: ed.condition, inferred: ed.inferred };
		});
		return { nodes, edges };
	});

	$effect(() => {
		// Fire ready as soon as we know whether traces are present or not.
		// The canvas itself doesn't have a "first paint" milestone here.
		activeTrace;
		loading;
		onReady?.();
	});

	// Drop the pinned panel whenever the underlying trace changes — the
	// step IDs are local per-trace, so a stale pin would point at a step
	// in a different flow.
	$effect(() => {
		activeTrace;
		pinnedStepId = null;
	});

	onMount(() => {
		if (!canvas) return;
		ctx = canvas.getContext('2d');

		zoomBehavior = zoom<HTMLCanvasElement, unknown>()
			.scaleExtent([0.2, 4])
			.on('zoom', (event) => {
				transform = { x: event.transform.x, y: event.transform.y, k: event.transform.k };
			});
		select(canvas).call(zoomBehavior);

		const ro = new ResizeObserver(resizeCanvas);
		ro.observe(canvas);
		resizeCanvas();

		return () => ro.disconnect();
	});

	function resizeCanvas() {
		if (!canvas) return;
		const w = canvas.clientWidth;
		const h = canvas.clientHeight;
		if (w === 0 || h === 0) return;
		dpr = window.devicePixelRatio || 1;
		canvas.width = Math.round(w * dpr);
		canvas.height = Math.round(h * dpr);
		canvasW = w;
		canvasH = h;
		scheduleDraw();
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
		layout;
		hoveredStepId;
		transform;
		scheduleDraw();
	});

	$effect(() => {
		// Auto-fit when the laid-out trace changes.
		layout;
		requestAnimationFrame(fitToViewport);
	});

	function fitToViewport() {
		if (!layout || !canvas || !zoomBehavior) return;
		const w = canvas.clientWidth;
		const h = canvas.clientHeight;
		if (w === 0 || h === 0) {
			requestAnimationFrame(fitToViewport);
			return;
		}
		let minX = Infinity,
			maxX = -Infinity,
			minY = Infinity,
			maxY = -Infinity;
		for (const n of layout.nodes) {
			if (n.x - n.w / 2 < minX) minX = n.x - n.w / 2;
			if (n.x + n.w / 2 > maxX) maxX = n.x + n.w / 2;
			if (n.y - n.h / 2 < minY) minY = n.y - n.h / 2;
			if (n.y + n.h / 2 > maxY) maxY = n.y + n.h / 2;
		}
		if (maxX === minX || maxY === minY) return;
		const padding = 40;
		const scale = Math.min(
			(w - padding * 2) / (maxX - minX),
			(h - padding * 2) / (maxY - minY),
			1.2
		);
		const cx = (minX + maxX) / 2;
		const cy = (minY + maxY) / 2;
		select(canvas).call(
			zoomBehavior.transform,
			zoomIdentity.translate(w / 2 - cx * scale, h / 2 - cy * scale).scale(scale)
		);
	}

	function draw() {
		if (!ctx || !canvas) return;
		const w = canvas.clientWidth;
		const h = canvas.clientHeight;
		ctx.save();
		ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
		ctx.clearRect(0, 0, w, h);

		if (!layout) {
			ctx.restore();
			return;
		}

		// Pass 1: boxes + edges in world coordinates.
		ctx.save();
		ctx.translate(transform.x, transform.y);
		ctx.scale(transform.k, transform.k);
		for (const edge of layout.edges) drawEdge(ctx, edge);
		for (const node of layout.nodes) drawNodeBox(ctx, node);
		ctx.restore();

		// Pass 2: text in screen coordinates so it stays crisp at any zoom.
		edgeLabelBoxes.length = 0;
		for (const node of layout.nodes) drawNodeText(ctx, node);
		for (let i = 0; i < layout.edges.length; i++) drawEdgeLabel(ctx, layout.edges[i], i);

		ctx.restore();
	}

	function drawEdge(c: CanvasRenderingContext2D, edge: LayoutEdge) {
		if (edge.points.length < 2) return;
		c.strokeStyle = edge.inferred ? 'rgba(160, 130, 100, 0.6)' : 'rgba(180, 160, 130, 0.85)';
		c.lineWidth = 1.5 / transform.k;
		if (edge.inferred) {
			c.setLineDash([6 / transform.k, 4 / transform.k]);
		} else {
			c.setLineDash([]);
		}
		c.beginPath();
		c.moveTo(edge.points[0].x, edge.points[0].y);
		for (let i = 1; i < edge.points.length; i++) {
			c.lineTo(edge.points[i].x, edge.points[i].y);
		}
		c.stroke();
		c.setLineDash([]);

		const last = edge.points[edge.points.length - 1];
		const prev = edge.points[edge.points.length - 2];
		const angle = Math.atan2(last.y - prev.y, last.x - prev.x);
		const len = 8 / transform.k;
		c.fillStyle = edge.inferred ? 'rgba(160, 130, 100, 0.7)' : 'rgba(180, 160, 130, 0.9)';
		c.beginPath();
		c.moveTo(last.x, last.y);
		c.lineTo(last.x - len * Math.cos(angle - Math.PI / 6), last.y - len * Math.sin(angle - Math.PI / 6));
		c.lineTo(last.x - len * Math.cos(angle + Math.PI / 6), last.y - len * Math.sin(angle + Math.PI / 6));
		c.closePath();
		c.fill();
	}

	function drawNodeBox(c: CanvasRenderingContext2D, node: LayoutNode) {
		const x = node.x - node.w / 2;
		const y = node.y - node.h / 2;
		const isHovered = hoveredStepId === node.id;
		const isInferred = node.step.provenance === 'inferred';

		c.fillStyle = '#1a1612';
		c.strokeStyle = isHovered
			? '#a896ff'
			: isInferred
				? 'rgba(160, 130, 100, 0.5)'
				: 'rgba(180, 160, 130, 0.55)';
		c.lineWidth = (isHovered ? 2 : 1) / transform.k;
		if (isInferred) {
			c.setLineDash([4 / transform.k, 3 / transform.k]);
		} else {
			c.setLineDash([]);
		}
		roundRect(c, x, y, node.w, node.h, 6 / transform.k);
		c.fill();
		c.stroke();
		c.setLineDash([]);
	}

	function drawNodeText(c: CanvasRenderingContext2D, node: LayoutNode) {
		const sx = node.x * transform.k + transform.x;
		const sy = node.y * transform.k + transform.y;
		const sw = node.w * transform.k;
		const sh = node.h * transform.k;
		if (sw < 60) return;

		const tx = sx - sw / 2 + 10;
		const ty = sy - sh / 2 + 8;

		const hasNodeId = !!node.step.node_id;
		const reqCount = node.step.requires?.length ?? 0;
		const hasDesc = !!(node.step.description && node.step.description.trim());

		const labelFontPx = 12;
		const descFontPx = 10;
		const reqFontPx = 9;

		c.fillStyle = hasNodeId ? '#e8dcc8' : '#9b8d7a';
		c.font = `bold ${labelFontPx}px ui-monospace, monospace`;
		c.textAlign = 'left';
		c.textBaseline = 'top';
		c.fillText(fitText(c, node.step.label, sw - 20), tx, ty);

		let yOff = labelFontPx + 6;

		// Box height scales with zoom but text stays in screen pixels — at
		// low zoom levels the 3-line description budget would overflow into
		// the requires line. Compute how many description lines actually fit.
		const descLineHeight = descFontPx + 3;
		const reqReserve = reqCount > 0 ? reqFontPx + 4 : 0;
		const bottomPad = 6;
		const availForDesc = sh - 8 - (labelFontPx + 6) - reqReserve - bottomPad;
		const maxDescLines = Math.min(3, Math.max(0, Math.floor(availForDesc / descLineHeight)));

		if (hasDesc && maxDescLines > 0) {
			c.fillStyle = 'rgba(155, 141, 122, 0.85)';
			c.font = `${descFontPx}px ui-sans-serif, system-ui`;
			const descLines = wrapText(c, node.step.description!, sw - 20, maxDescLines);
			for (const line of descLines) {
				c.fillText(line, tx, ty + yOff);
				yOff += descLineHeight;
			}
			yOff += 2;
		}

		if (reqCount > 0 && sh > 50) {
			c.fillStyle = 'rgba(255, 180, 90, 0.7)';
			c.font = `${reqFontPx}px ui-monospace, monospace`;
			c.fillText(`+ ${reqCount} require${reqCount === 1 ? '' : 's'}`, tx, ty + yOff);
		}
	}

	function drawEdgeLabel(c: CanvasRenderingContext2D, edge: LayoutEdge, edgeIdx: number) {
		if (!edge.condition || edge.points.length < 2) return;
		const mid = edge.points[Math.floor(edge.points.length / 2)];
		const sx = mid.x * transform.k + transform.x;
		const sy = mid.y * transform.k + transform.y;

		const fontPx = 10;
		c.font = `${fontPx}px ui-monospace, monospace`;
		c.textAlign = 'center';
		c.textBaseline = 'middle';

		// Single line, aggressively truncated — full text shown on hover.
		const maxLineWidth = 140;
		const lines = wrapText(c, edge.condition, maxLineWidth, 1);
		if (lines.length === 0) return;

		const padX = 6;
		const padY = 3;
		const textW = c.measureText(lines[0]).width;
		const textH = fontPx;

		const boxX = sx - textW / 2 - padX;
		const boxY = sy - textH / 2 - padY;
		const boxW = textW + padX * 2;
		const boxH = textH + padY * 2;

		const isHovered = hoveredEdgeIdx === edgeIdx;
		c.fillStyle = isHovered ? 'rgba(40, 30, 20, 0.95)' : 'rgba(20, 16, 12, 0.92)';
		c.strokeStyle = isHovered ? 'rgba(180, 160, 130, 0.6)' : 'rgba(180, 160, 130, 0.25)';
		c.lineWidth = 1;
		roundRect(c, boxX, boxY, boxW, boxH, 4);
		c.fill();
		c.stroke();

		c.fillStyle = isHovered ? 'rgba(232, 220, 200, 1)' : 'rgba(200, 180, 150, 0.95)';
		c.fillText(lines[0], sx, sy);

		edgeLabelBoxes.push({ idx: edgeIdx, x: boxX, y: boxY, w: boxW, h: boxH });
	}

	function wrapText(
		c: CanvasRenderingContext2D,
		text: string,
		maxWidth: number,
		maxLines: number
	): string[] {
		const words = text.split(/\s+/).filter(Boolean);
		if (words.length === 0) return [];

		const lines: string[] = [];
		let current = '';

		for (let i = 0; i < words.length; i++) {
			const word = words[i];
			const candidate = current ? `${current} ${word}` : word;
			if (c.measureText(candidate).width <= maxWidth) {
				current = candidate;
				continue;
			}
			// Word doesn't fit — flush current line and continue with this word.
			if (current) lines.push(current);
			if (lines.length >= maxLines) {
				// Out of vertical budget — squash remaining words into last line + ellipsis.
				const remainder = words.slice(i).join(' ');
				lines[maxLines - 1] = fitText(c, `${lines[maxLines - 1]} ${remainder}`, maxWidth);
				return lines;
			}
			current = word;
		}
		if (current) lines.push(current);
		return lines;
	}

	function fitText(c: CanvasRenderingContext2D, s: string, maxWidth: number): string {
		if (maxWidth <= 0) return '';
		if (c.measureText(s).width <= maxWidth) return s;
		const ellipsis = '…';
		let lo = 0;
		let hi = s.length;
		while (lo < hi) {
			const mid = (lo + hi + 1) >> 1;
			if (c.measureText(s.slice(0, mid) + ellipsis).width <= maxWidth) lo = mid;
			else hi = mid - 1;
		}
		return lo > 0 ? s.slice(0, lo) + ellipsis : '';
	}

	function roundRect(
		c: CanvasRenderingContext2D,
		x: number,
		y: number,
		w: number,
		h: number,
		r: number
	) {
		const rr = Math.min(r, w / 2, h / 2);
		c.beginPath();
		c.moveTo(x + rr, y);
		c.arcTo(x + w, y, x + w, y + h, rr);
		c.arcTo(x + w, y + h, x, y + h, rr);
		c.arcTo(x, y + h, x, y, rr);
		c.arcTo(x, y, x + w, y, rr);
		c.closePath();
	}

	function hitTest(clientX: number, clientY: number): LayoutNode | null {
		if (!canvas || !layout) return null;
		const rect = canvas.getBoundingClientRect();
		const cx = clientX - rect.left;
		const cy = clientY - rect.top;
		const wx = (cx - transform.x) / transform.k;
		const wy = (cy - transform.y) / transform.k;
		for (const node of layout.nodes) {
			if (
				wx >= node.x - node.w / 2 &&
				wx <= node.x + node.w / 2 &&
				wy >= node.y - node.h / 2 &&
				wy <= node.y + node.h / 2
			) {
				return node;
			}
		}
		return null;
	}

	function handlePointerMove(e: PointerEvent) {
		const nodeHit = hitTest(e.clientX, e.clientY);
		if (nodeHit) {
			hoveredStepId = nodeHit.id;
			hoveredEdgeIdx = null;
			cursor = { x: e.clientX, y: e.clientY };
			return;
		}
		hoveredStepId = null;

		// Edge labels are drawn in screen space — hit-test against raw cursor coords.
		if (!canvas) {
			hoveredEdgeIdx = null;
			cursor = null;
			return;
		}
		const rect = canvas.getBoundingClientRect();
		const cx = e.clientX - rect.left;
		const cy = e.clientY - rect.top;
		const edgeHit = edgeLabelBoxes.find(
			(b) => cx >= b.x && cx <= b.x + b.w && cy >= b.y && cy <= b.y + b.h
		);
		hoveredEdgeIdx = edgeHit?.idx ?? null;
		cursor = edgeHit ? { x: e.clientX, y: e.clientY } : null;
	}

	function handlePointerLeave() {
		hoveredStepId = null;
		hoveredEdgeIdx = null;
		cursor = null;
	}

	function handleClick(e: MouseEvent) {
		const hit = hitTest(e.clientX, e.clientY);
		if (!hit) {
			pinnedStepId = null;
			return;
		}
		// Toggle: clicking the already-pinned step closes the panel.
		pinnedStepId = pinnedStepId === hit.id ? null : hit.id;
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape' && pinnedStepId) {
			pinnedStepId = null;
		}
	}

	// Position derivation for the pinned panel — anchors to the clicked
	// step's current screen position. Side-switches based on which half
	// of the canvas the box is in so the panel stays visible.
	let pinned = $derived.by(() => {
		if (!pinnedStepId || !layout || canvasW === 0) return null;
		const node = layout.nodes.find((n) => n.id === pinnedStepId);
		if (!node) return null;
		const sx = node.x * transform.k + transform.x;
		const sy = node.y * transform.k + transform.y;
		const sw = node.w * transform.k;
		const sh = node.h * transform.k;
		const placeRight = sx < canvasW / 2;
		return { node, sx, sy, sw, sh, placeRight };
	});
</script>

<svelte:window onkeydown={handleKeydown} />

<div class="absolute inset-0">
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<canvas
		bind:this={canvas}
		class="absolute inset-0 w-full h-full"
		class:cursor-pointer={hoveredStepId || hoveredEdgeIdx !== null}
		onpointermove={handlePointerMove}
		onpointerleave={handlePointerLeave}
		onclick={handleClick}
	></canvas>

	{#if !loading && !generating && !activeTrace}
		<div class="absolute inset-0 flex items-center justify-center pointer-events-none">
			<span class="text-bourbon-600 text-sm">{emptyMessage}</span>
		</div>
	{/if}

	<!-- Current/Previous toggle — only when a previous version exists. -->
	{#if previousTrace}
		<div class="absolute top-3 right-3 z-20 flex items-center gap-1 p-0.5 rounded-md
			bg-bourbon-900/70 border border-bourbon-800 backdrop-blur-sm">
			<button
				onclick={() => onViewChange?.('current')}
				class="px-2.5 py-1 rounded font-display text-[10px] font-bold uppercase tracking-widest transition-colors cursor-pointer
					{view === 'current'
						? 'bg-bourbon-700/60 text-bourbon-200'
						: 'text-bourbon-500 hover:text-bourbon-300'}"
			>
				current
			</button>
			<button
				onclick={() => onViewChange?.('previous')}
				class="px-2.5 py-1 rounded font-display text-[10px] font-bold uppercase tracking-widest transition-colors cursor-pointer
					{view === 'previous'
						? 'bg-bourbon-700/60 text-bourbon-200'
						: 'text-bourbon-500 hover:text-bourbon-300'}"
			>
				<History size={10} class="inline mr-1" />
				previous
			</button>
		</div>
	{/if}

	<!-- Change-summary banner when viewing previous. -->
	{#if view === 'previous' && changeSummary && changeSummary.summary}
		<div class="absolute top-3 left-3 z-20 max-w-md px-3 py-2 rounded-md
			bg-cmd-900/60 border border-cmd-700/50 backdrop-blur-sm">
			<div class="font-display text-[9px] font-bold uppercase tracking-widest text-cmd-400 mb-1">
				what changed
			</div>
			<div class="text-[11px] text-bourbon-200 leading-relaxed">
				{changeSummary.summary}
			</div>
		</div>
	{/if}

	<!-- Anchorless callouts (added in current with no previous step) — text-only side panel. -->
	{#if view === 'previous' && anchorlessCallouts.length > 0}
		<div class="absolute bottom-3 right-3 z-20 w-72 max-h-64 overflow-y-auto
			rounded-md bg-bourbon-900/85 border border-bourbon-800 backdrop-blur-sm">
			<div class="px-3 py-2 border-b border-bourbon-800/60 font-display text-[9px] font-bold uppercase tracking-widest text-cmd-400">
				added in current ({anchorlessCallouts.length})
			</div>
			<div class="flex flex-col">
				{#each anchorlessCallouts as ch}
					<div class="px-3 py-2 border-b border-bourbon-800/40 last:border-b-0">
						<div class="text-[9px] font-display font-bold uppercase tracking-widest text-cmd-500 mb-0.5">
							{ch.kind}
						</div>
						<div class="text-[10px] text-bourbon-300 leading-relaxed">{ch.description}</div>
					</div>
				{/each}
			</div>
		</div>
	{/if}

	<!-- In-flight activity panel: streams agent tool/text events as they arrive. -->
	{#if generating}
		<div class="absolute inset-0 z-30 flex items-center justify-center bg-bourbon-950/85 backdrop-blur-sm">
			<div class="w-[28rem] max-h-[70vh] flex flex-col bg-bourbon-900/95 border border-bourbon-800 rounded-lg overflow-hidden">
				<div class="px-4 py-3 border-b border-bourbon-800 flex items-center justify-between gap-2">
					<div class="flex items-center gap-2">
						<div class="w-3.5 h-3.5 border-2 border-bourbon-700 border-t-cmd-500 rounded-full animate-spin"></div>
						<span class="font-display text-xs uppercase tracking-widest text-bourbon-300">Generating trace</span>
					</div>
					<span class="font-mono text-xs text-bourbon-500 tabular-nums">{formatElapsed(elapsed)}</span>
				</div>
				<div class="px-4 py-3 flex flex-col gap-1.5 font-mono text-[10px]">
					{#if activity.length === 0}
						<span class="text-bourbon-600">waiting for agent…</span>
					{:else}
						{#each recentActivity as evt (evt.id)}
							{#if evt.kind === 'tool'}
								<div class="text-bourbon-300 truncate">· {evt.text}</div>
							{:else if evt.kind === 'text'}
								<div class="text-bourbon-500 leading-relaxed line-clamp-2">{evt.text}</div>
							{:else if evt.kind === 'error'}
								<div class="text-red-400 truncate">! {evt.text}</div>
							{:else if evt.kind === 'phase'}
								<div class="text-run-500 uppercase tracking-widest">— {evt.text} —</div>
							{/if}
						{/each}
					{/if}
				</div>
				<div class="px-4 py-2 border-t border-bourbon-800 text-[9px] text-bourbon-700">
					Typically takes 1–3 minutes. You can close this page; the run continues in the background.
				</div>
			</div>
		</div>
	{/if}

	<!-- Hover tooltip — quick peek. Suppressed while a step is pinned to
	     avoid double-display. -->
	{#if hoveredStepId && cursor && layout && !pinnedStepId}
		{@const hovered = layout.nodes.find((n) => n.id === hoveredStepId)}
		{#if hovered}
			<div
				class="fixed z-40 max-w-md px-3 py-2 rounded-md bg-bourbon-900/95 border border-bourbon-700 backdrop-blur-sm pointer-events-none shadow-xl"
				style:left="{cursor.x + 14}px"
				style:top="{cursor.y + 14}px"
			>
				<div class="font-mono text-xs text-bourbon-200 mb-1">{hovered.step.label}</div>
				{#if hovered.step.description}
					<div class="text-[10px] text-bourbon-500 leading-relaxed line-clamp-3">
						{hovered.step.description}
					</div>
				{/if}
				<div class="text-[9px] text-bourbon-700 mt-1 italic">click to expand</div>
			</div>
		{/if}
	{/if}

	<!-- Edge condition tooltip — full text on hover, since the on-canvas
	     label is aggressively truncated to one line. -->
	{#if hoveredEdgeIdx !== null && cursor && layout}
		{@const hoveredEdge = layout.edges[hoveredEdgeIdx]}
		{#if hoveredEdge?.condition}
			<div
				class="fixed z-40 max-w-sm px-3 py-2 rounded-md bg-bourbon-900/95 border border-bourbon-700 backdrop-blur-sm pointer-events-none shadow-xl"
				style:left="{cursor.x + 14}px"
				style:top="{cursor.y + 14}px"
			>
				<div class="text-[10px] font-mono text-bourbon-300 leading-relaxed">
					{hoveredEdge.condition}
				</div>
			</div>
		{/if}
	{/if}

	<!-- Pinned detail panel — anchored to the clicked step. Shows full
	     description, provenance, source location, and requires. Click
	     a step again (or the X) to dismiss. -->
	{#if pinned}
		<div
			class="absolute z-40 w-[400px] max-h-[80%] overflow-y-auto rounded-lg bg-bourbon-900/95 border border-bourbon-700 backdrop-blur-sm shadow-2xl"
			style:left={pinned.placeRight
				? `${Math.min(canvasW - 16 - 400, pinned.sx + pinned.sw / 2 + 16)}px`
				: `${Math.max(16, pinned.sx - pinned.sw / 2 - 400 - 16)}px`}
			style:top="{Math.max(16, Math.min(canvasH - 200, pinned.sy - pinned.sh / 2))}px"
		>
			<div class="flex items-start justify-between gap-2 px-4 py-3 border-b border-bourbon-800/60">
				<div class="font-mono text-xs text-bourbon-200 leading-snug min-w-0 break-words">
					{pinned.node.step.label}
				</div>
				<button
					onclick={() => (pinnedStepId = null)}
					class="text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer shrink-0"
				>
					<X size={14} />
				</button>
			</div>

			{#if pinned.node.step.description}
				<div class="px-4 py-3 text-[11px] text-bourbon-400 leading-relaxed border-b border-bourbon-800/40">
					{pinned.node.step.description}
				</div>
			{/if}

			<div class="px-4 py-2.5 flex items-center gap-2 text-[9px] font-mono border-b border-bourbon-800/40">
				<span
					class="px-1.5 py-0.5 rounded {pinned.node.step.provenance === 'extracted'
						? 'bg-bourbon-800/60 text-bourbon-400'
						: 'bg-bourbon-800/40 text-bourbon-500'}"
				>
					{pinned.node.step.provenance}
				</span>
				{#if pinned.node.step.source_file}
					<span
						class="text-bourbon-600 truncate"
						style="direction: rtl; text-align: left;"
					>{pinned.node.step.source_file}{#if pinned.node.step.source_line}:{pinned.node.step.source_line}{/if}</span>
					{#if repoPath}
						<button
							onclick={() => openInEditor(repoPath!, pinned!.node.step.source_file!, pinned!.node.step.source_line ?? 1)}
							class="shrink-0 flex items-center gap-1 text-bourbon-600 hover:text-cmd-400 transition-colors cursor-pointer uppercase tracking-wider"
							title="Open in editor"
						>
							<ExternalLink size={10} />
							open
						</button>
					{/if}
				{/if}
			</div>

			{#if view === 'previous' && calloutsByStepID.get(pinned.node.id)}
				{@const calls = calloutsByStepID.get(pinned.node.id) ?? []}
				<div class="px-4 py-3 border-b border-bourbon-800/40 flex flex-col gap-1.5">
					<div class="text-[9px] font-display font-bold uppercase tracking-widest text-cmd-400">
						what changed
					</div>
					{#each calls as c}
						<div>
							<div class="text-[9px] font-display font-bold uppercase tracking-widest text-cmd-500 mb-0.5">
								{c.kind}
							</div>
							<div class="text-[10px] text-bourbon-300 leading-relaxed">{c.description}</div>
						</div>
					{/each}
				</div>
			{/if}

			{#if pinned.node.step.requires && pinned.node.step.requires.length > 0}
				<div class="px-4 py-3 border-b border-bourbon-800/40 bg-black/20">
					<div class="text-[9px] font-display font-bold uppercase tracking-widest text-run-500 mb-2">
						requires
					</div>
					<div class="flex flex-col gap-1.5">
						{#each pinned.node.step.requires as req}
							<div class="flex flex-col gap-0.5">
								<div class="flex items-center gap-2 text-[10px] font-mono">
									<span class="text-bourbon-600 w-14 shrink-0">{req.kind}</span>
									<span class="text-bourbon-300 break-words">{req.label}</span>
									{#if req.provenance === 'inferred'}
										<span class="text-bourbon-700 text-[8px]">inferred</span>
									{/if}
								</div>
								{#if req.description}
									<div class="text-[10px] text-bourbon-600 ml-16 leading-relaxed">
										{req.description}
									</div>
								{/if}
							</div>
						{/each}
					</div>
				</div>
			{/if}

			{#if pinned.node.step.node_id && onNavigate}
				{@const nodeId = pinned.node.step.node_id}
				<button
					onclick={() => onNavigate?.(nodeId)}
					class="w-full flex items-center justify-center gap-1.5 px-4 py-2.5
						text-[10px] font-display font-bold uppercase tracking-widest
						text-cmd-400 hover:text-cmd-300 hover:bg-bourbon-800/40
						transition-colors cursor-pointer"
				>
					View in network
					<ArrowRight size={11} />
				</button>
			{/if}
		</div>
	{/if}
</div>
