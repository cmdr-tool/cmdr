<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { scaleLinear } from 'd3-scale';
	import { getActivity, type ActivityBucket, type ActivityResponse } from '$lib/api';
	import { events } from '$lib/events';

	let data = $state<ActivityResponse | null>(null);
	let containerWidth = $state(600);
	let hoverIdx: number | null = $state(null);
	let activeView: 'tools' | 'claude' | 'pi' = $state('tools');
	let container: HTMLDivElement;

	const STRIP_HEIGHT = 16;
	const BARS_PER_DAY = 288; // 5m buckets

	const toolColors: Record<string, string> = {
		nvim: 'var(--color-cmd-500)',
		agent: 'var(--color-run-500)',
		other: 'var(--color-bourbon-600)',
		away: 'hatch'
	};

	let unsub: (() => void) | null = null;
	let ro: ResizeObserver | null = null;

	onMount(() => {
		// Seed from REST
		getActivity('5m').then((d) => { data = d; }).catch(() => {});

		// Subscribe to SSE deltas
		unsub = events.on('analytics:activity', (update: ActivityResponse) => {
			data = update;
		});

		ro = new ResizeObserver((entries) => {
			containerWidth = entries[0].contentRect.width;
		});
		ro.observe(container);
	});

	onDestroy(() => {
		if (unsub) unsub();
		if (ro) ro.disconnect();
	});

	let barWidth = $derived(Math.max(1, containerWidth / BARS_PER_DAY));
	let xScale = $derived(scaleLinear().domain([0, BARS_PER_DAY]).range([0, containerWidth]));

	// --- Tools view ---

	function dominantTool(b: ActivityBucket): string | null {
		const active = b.nvim + b.agent + b.other;
		// If mostly away, show hatch pattern
		if (b.away > active && b.away > b.inactive) return 'away';
		if (active === 0) return null;
		if (b.nvim >= b.agent && b.nvim >= b.other) return 'nvim';
		if (b.agent >= b.nvim && b.agent >= b.other) return 'agent';
		return 'other';
	}

	// Fill gaps: once "away", stay "away" until an active tool proves return.
	// Buckets with no data or "inactive" between away periods → treated as away.
	function fillAwayGaps(buckets: ActivityBucket[]): Map<number, string> {
		const map = new Map(buckets.map((b) => [b.bucket, b]));
		const resolved = new Map<number, string>();

		if (!buckets.length) return resolved;

		const minBucket = Math.min(...buckets.map(b => b.bucket));
		const maxBucket = Math.max(...buckets.map(b => b.bucket));

		let inAway = false;
		for (let i = minBucket; i <= maxBucket; i++) {
			const b = map.get(i);
			const tool = b ? dominantTool(b) : null;

			if (tool === 'away') {
				inAway = true;
				resolved.set(i, 'away');
			} else if (tool && tool !== 'away') {
				// Active tool — user returned
				inAway = false;
				resolved.set(i, tool);
			} else {
				// No data or inactive — if we were away, stay away
				if (inAway) {
					resolved.set(i, 'away');
				}
				// Otherwise leave as gap (no entry in resolved)
			}
		}
		return resolved;
	}

	function toToolSegments(buckets: ActivityBucket[]): { x: number; w: number; color: string }[] {
		if (!buckets.length) return [];
		const resolved = fillAwayGaps(buckets);
		const segments: { x: number; w: number; color: string }[] = [];
		let i = 0;
		while (i < BARS_PER_DAY) {
			const tool = resolved.get(i) ?? null;
			if (!tool) { i++; continue; }
			const start = i;
			while (i < BARS_PER_DAY) {
				if ((resolved.get(i) ?? null) !== tool) break;
				i++;
			}
			segments.push({
				x: xScale(start),
				w: Math.max(1, xScale(i) - xScale(start)),
				color: toolColors[tool]
			});
		}
		return segments;
	}

	// --- Claude view ---
	// Stacked strip: green=working, amber=waiting, muted=idle
	// Height proportional to count / max total

	// --- Agent state views (claude + pi) ---
	// Generic helpers that extract total/working/waiting/idle/unknown from a bucket
	// for a given agent, enabling reuse across claude and pi tabs.

	type AgentFields = {
		total: (b: ActivityBucket) => number;
		working: (b: ActivityBucket) => number;
		waiting: (b: ActivityBucket) => number;
		idle: (b: ActivityBucket) => number;
		unknown: (b: ActivityBucket) => number;
	};

	const claudeFields: AgentFields = {
		total: b => b.claudeTotal, working: b => b.claudeWorking,
		waiting: b => b.claudeWaiting, idle: b => b.claudeIdle, unknown: b => b.claudeUnknown
	};
	const piFields: AgentFields = {
		total: b => b.piTotal, working: b => b.piWorking,
		waiting: b => b.piWaiting, idle: b => b.piIdle, unknown: b => b.piUnknown
	};

	function maxTotal(fields: AgentFields): number {
		if (!data) return 1;
		return Math.max(1,
			...data.today.buckets.map(fields.total),
			...data.yesterday.buckets.map(fields.total)
		);
	}

	// Fill gaps: between data points, interpolate from nearest neighbor's state.
	function fillAgentGaps(buckets: ActivityBucket[], fields: AgentFields): ActivityBucket[] {
		if (!buckets.length) return [];
		const agentBuckets = buckets.filter(b => fields.total(b) > 0);
		if (!agentBuckets.length) return buckets;

		const map = new Map(buckets.map((b) => [b.bucket, b]));
		const filled: ActivityBucket[] = [];
		const minBucket = Math.min(...buckets.map(b => b.bucket));
		const maxBucket = Math.max(...buckets.map(b => b.bucket));

		const agentMap = new Map(agentBuckets.map(b => [b.bucket, b]));
		let lastKnown: ActivityBucket | null = null;
		let nextKnown: ActivityBucket | null = null;

		const nextKnownAt = new Map<number, ActivityBucket>();
		let scan: ActivityBucket | null = null;
		for (let i = maxBucket; i >= minBucket; i--) {
			const cb = agentMap.get(i);
			if (cb) scan = cb;
			if (scan) nextKnownAt.set(i, scan);
		}

		lastKnown = null;
		for (let i = minBucket; i <= maxBucket; i++) {
			const b = map.get(i);
			if (b) {
				filled.push(b);
				if (fields.total(b) > 0) lastKnown = b;
			} else {
				nextKnown = nextKnownAt.get(i) ?? null;
				if (lastKnown && nextKnown) {
					const distPrev = i - lastKnown.bucket;
					const distNext = nextKnown.bucket - i;
					const source = distPrev <= distNext ? lastKnown : nextKnown;
					filled.push({ ...emptyBucket(i), ...copyAgentFields(source) });
				}
			}
		}
		return filled;
	}

	function emptyBucket(bucket: number): ActivityBucket {
		return {
			bucket, samples: 0, nvim: 0, agent: 0, other: 0, inactive: 0, away: 0,
			claudeTotal: 0, claudeWorking: 0, claudeWaiting: 0, claudeIdle: 0, claudeUnknown: 0,
			piTotal: 0, piWorking: 0, piWaiting: 0, piIdle: 0, piUnknown: 0
		};
	}

	function copyAgentFields(source: ActivityBucket): Partial<ActivityBucket> {
		return {
			claudeTotal: source.claudeTotal, claudeWorking: source.claudeWorking,
			claudeWaiting: source.claudeWaiting, claudeIdle: source.claudeIdle, claudeUnknown: source.claudeUnknown,
			piTotal: source.piTotal, piWorking: source.piWorking,
			piWaiting: source.piWaiting, piIdle: source.piIdle, piUnknown: source.piUnknown
		};
	}

	type StackedSeg = { x: number; w: number; layers: { color: string; heightPct: number }[] };

	function toAgentSegments(buckets: ActivityBucket[], fields: AgentFields, maxT: number): StackedSeg[] {
		if (!buckets.length) return [];
		const filled = fillAgentGaps(buckets, fields);
		const result: StackedSeg[] = [];
		for (const b of filled) {
			const t = fields.total(b);
			if (t === 0) continue;
			const scale = t / maxT;
			const layers: { color: string; heightPct: number }[] = [];
			const w = fields.working(b), wa = fields.waiting(b), id = fields.idle(b), un = fields.unknown(b);
			if (w > 0) layers.push({ color: 'var(--color-green-500)', heightPct: (w / t) * scale });
			if (wa > 0) layers.push({ color: 'var(--color-run-500)', heightPct: (wa / t) * scale });
			if (id > 0) layers.push({ color: 'var(--color-bourbon-700)', heightPct: (id / t) * scale });
			if (un > 0) layers.push({ color: 'var(--color-cmd-500)', heightPct: (un / t) * scale });
			result.push({ x: xScale(b.bucket), w: Math.max(1, barWidth), layers });
		}
		return result;
	}

	let maxClaudeT = $derived(maxTotal(claudeFields));
	let maxPiT = $derived(maxTotal(piFields));

	let todayToolSegs = $derived(data ? toToolSegments(data.today.buckets) : []);
	let yesterdayToolSegs = $derived(data ? toToolSegments(data.yesterday.buckets) : []);
	let todayClaudeSegs = $derived(data ? toAgentSegments(data.today.buckets, claudeFields, maxClaudeT) : []);
	let yesterdayClaudeSegs = $derived(data ? toAgentSegments(data.yesterday.buckets, claudeFields, maxClaudeT) : []);
	let todayPiSegs = $derived(data ? toAgentSegments(data.today.buckets, piFields, maxPiT) : []);
	let yesterdayPiSegs = $derived(data ? toAgentSegments(data.yesterday.buckets, piFields, maxPiT) : []);

	// Show pi tab only if there's any pi data
	let hasPiData = $derived(
		data ? data.today.buckets.some(b => b.piTotal > 0) || data.yesterday.buckets.some(b => b.piTotal > 0) : false
	);

	let nowX = $derived(data && data.today.currentBucket != null ? xScale(data.today.currentBucket) : null);
	let todayMap: Map<number, ActivityBucket> = $derived(new Map(data ? data.today.buckets.map((b): [number, ActivityBucket] => [b.bucket, b]) : []));

	function handleMouseMove(e: MouseEvent) {
		const rect = (e.currentTarget as Element).getBoundingClientRect();
		hoverIdx = Math.max(0, Math.min(BARS_PER_DAY - 1, Math.floor(xScale.invert(e.clientX - rect.left))));
	}

	function handleMouseLeave() { hoverIdx = null; }

	function bucketToTime(bucket: number): string {
		const totalMins = bucket * 5;
		const h = Math.floor(totalMins / 60);
		const m = totalMins % 60;
		const period = h >= 12 ? 'pm' : 'am';
		const h12 = h === 0 ? 12 : h > 12 ? h - 12 : h;
		return `${h12}:${String(m).padStart(2, '0')}${period}`;
	}

	let hoverBucket: ActivityBucket | null = $derived.by(() => hoverIdx !== null ? todayMap.get(hoverIdx) ?? null : null);

	let todayStats = $derived.by(() => {
		if (!data) return { nvim: 0, agent: 0, other: 0 };
		let nvim = 0, agent = 0, other = 0;
		for (const b of data.today.buckets) { nvim += b.nvim; agent += b.agent; other += b.other; }
		return { nvim: Math.round(nvim * 5 / 60), agent: Math.round(agent * 5 / 60), other: Math.round(other * 5 / 60) };
	});

	function minsToStr(mins: number): string {
		if (mins < 60) return `${mins}m`;
		const h = Math.floor(mins / 60);
		const m = mins % 60;
		return m ? `${h}h${m}m` : `${h}h`;
	}
</script>

<div bind:this={container} class="w-full">
	{#if data}
		<!-- Header with tab switcher -->
		<div class="flex items-center justify-between mb-2">
			<div class="flex items-center gap-3">
				<button
					onclick={() => activeView = 'tools'}
					class="text-[10px] font-mono transition-colors cursor-pointer {activeView === 'tools' ? 'text-run-400' : 'text-bourbon-700 hover:text-bourbon-500'}"
				>tools</button>
				<button
					onclick={() => activeView = 'claude'}
					class="text-[10px] font-mono transition-colors cursor-pointer {activeView === 'claude' ? 'text-green-400' : 'text-bourbon-700 hover:text-bourbon-500'}"
				>claude</button>
				{#if hasPiData}
					<button
						onclick={() => activeView = 'pi'}
						class="text-[10px] font-mono transition-colors cursor-pointer {activeView === 'pi' ? 'text-green-400' : 'text-bourbon-700 hover:text-bourbon-500'}"
					>pi</button>
				{/if}
			</div>
			<div class="text-[10px] font-mono text-bourbon-600 h-4">
				{#if hoverIdx !== null}
					{bucketToTime(hoverIdx)}
					{#if hoverBucket}
						{#if activeView === 'tools'}
							{@const tool = dominantTool(hoverBucket)}
							— <span class="{tool === 'nvim' ? 'text-cmd-400' : tool === 'agent' ? 'text-run-400' : 'text-bourbon-500'}">{tool ?? 'inactive'}</span>
						{:else if activeView === 'claude'}
							— <span class="text-green-400">{hoverBucket.claudeWorking}</span> working
							· <span class="text-run-400">{hoverBucket.claudeWaiting}</span> waiting
							· {hoverBucket.claudeIdle} idle
							{#if hoverBucket.claudeUnknown > 0}· <span class="text-cmd-400">{hoverBucket.claudeUnknown}</span> ?{/if}
							<span class="text-bourbon-700">/ {hoverBucket.claudeTotal}</span>
						{:else}
							— <span class="text-green-400">{hoverBucket.piWorking}</span> working
							· <span class="text-run-400">{hoverBucket.piWaiting}</span> waiting
							· {hoverBucket.piIdle} idle
							{#if hoverBucket.piUnknown > 0}· <span class="text-cmd-400">{hoverBucket.piUnknown}</span> ?{/if}
							<span class="text-bourbon-700">/ {hoverBucket.piTotal}</span>
						{/if}
					{/if}
				{:else if activeView === 'tools'}
					<span class="text-cmd-400">{minsToStr(todayStats.nvim)}</span> nvim
					· <span class="text-run-400">{minsToStr(todayStats.agent)}</span> agent
					{#if todayStats.other > 0}· {minsToStr(todayStats.other)} other{/if}
				{/if}
			</div>
		</div>

		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="relative cursor-crosshair"
			onmousemove={handleMouseMove}
			onmouseleave={handleMouseLeave}
		>
			{#if activeView === 'tools'}
				<!-- Yesterday strip -->
				<div class="relative h-1 rounded-sm overflow-hidden bg-bourbon-800/20 mb-1">
					{#each yesterdayToolSegs as seg}
						<div class="absolute top-0 bottom-0 opacity-30 {seg.color === 'hatch' ? 'bg-hatch' : ''}" style="left:{seg.x}px; width:{seg.w}px; {seg.color !== 'hatch' ? `background:${seg.color}` : ''}"></div>
					{/each}
				</div>
				<!-- Today strip -->
				<div class="relative rounded-sm overflow-hidden bg-bourbon-800/20" style="height:{STRIP_HEIGHT}px">
					{#each todayToolSegs as seg}
						<div class="absolute top-0 bottom-0 {seg.color === 'hatch' ? 'bg-hatch' : ''}" style="left:{seg.x}px; width:{seg.w}px; {seg.color !== 'hatch' ? `background:${seg.color}` : ''}"></div>
					{/each}
					{#if hoverIdx !== null}<div class="absolute top-0 bottom-0 w-px bg-bourbon-100/60" style="left:{xScale(hoverIdx)}px"></div>{/if}
				</div>
				<!-- Now line + caret -->
				{#if nowX !== null}
					<div class="absolute top-[8px] h-[16px] border-r border-dashed border-bourbon-100 translate-x-px animate-pulse pointer-events-none" style="left:{nowX}px"></div>
					<div class="absolute top-[25px] -translate-x-[2px] animate-pulse pointer-events-none" style="left:{nowX}px">
						<div class="border-x-[3px] border-x-transparent border-b-[4px] border-b-bourbon-300 drop-shadow-[0_0_2px_var(--color-bourbon-300)]"></div>
					</div>
				{/if}
				<div class="flex items-center gap-3 mt-1.5 text-[9px] font-mono text-bourbon-700">
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-cmd-500 inline-block"></span>nvim</span>
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-run-500 inline-block"></span>agent</span>
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-bourbon-600 inline-block"></span>other</span>
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-sm bg-hatch inline-block"></span>away</span>
				</div>
			{:else}
				{@const todaySegs = activeView === 'claude' ? todayClaudeSegs : todayPiSegs}
				{@const yesterdaySegs = activeView === 'claude' ? yesterdayClaudeSegs : yesterdayPiSegs}
				<!-- Agent state: yesterday strip -->
				<div class="relative h-1 rounded-sm overflow-hidden bg-bourbon-800/20 mb-1">
					{#each yesterdaySegs as seg}
						{#each seg.layers as layer}
							<div class="absolute opacity-25" style="left:{seg.x}px; width:{seg.w}px; bottom:0; height:100%; background:{layer.color}"></div>
						{/each}
					{/each}
				</div>
				<!-- Agent state: today strip (stacked bars) -->
				<div class="relative rounded-sm overflow-hidden bg-bourbon-800/20" style="height:{STRIP_HEIGHT}px">
					{#each todaySegs as seg}
						{#each seg.layers as layer, li}
							{@const bottomPct = seg.layers.slice(0, li).reduce((s, l) => s + l.heightPct, 0)}
							<div class="absolute" style="left:{seg.x}px; width:{seg.w}px; bottom:{bottomPct * STRIP_HEIGHT}px; height:{Math.max(1, layer.heightPct * STRIP_HEIGHT)}px; background:{layer.color}"></div>
						{/each}
					{/each}
					{#if hoverIdx !== null}<div class="absolute top-0 bottom-0 w-px bg-bourbon-100/60" style="left:{xScale(hoverIdx)}px"></div>{/if}
				</div>
				<!-- Now line + caret -->
				{#if nowX !== null}
					<div class="absolute top-[8px] h-[16px] border-r border-dashed border-bourbon-100 translate-x-px animate-pulse pointer-events-none" style="left:{nowX}px"></div>
					<div class="absolute top-[25px] -translate-x-[2px] animate-pulse pointer-events-none" style="left:{nowX}px">
						<div class="border-x-[3px] border-x-transparent border-b-[4px] border-b-bourbon-300 drop-shadow-[0_0_2px_var(--color-bourbon-300)]"></div>
					</div>
				{/if}
				<div class="flex items-center gap-3 mt-1.5 text-[9px] font-mono text-bourbon-700">
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-green-500 inline-block"></span>working</span>
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-run-500 inline-block"></span>waiting</span>
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-bourbon-700 inline-block"></span>idle</span>
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-cmd-500 inline-block"></span>?</span>
				</div>
			{/if}
		</div>
	{/if}
</div>
