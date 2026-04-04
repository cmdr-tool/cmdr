<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { scaleLinear } from 'd3-scale';
	import { getActivity, type ActivityBucket, type ActivityResponse } from '$lib/api';
	import { events } from '$lib/events';

	let data: ActivityResponse | null = $state(null);
	let containerWidth = $state(600);
	let hoverIdx: number | null = $state(null);
	let activeView: 'tools' | 'claude' = $state('tools');
	let container: HTMLDivElement;

	const STRIP_HEIGHT = 16;
	const BARS_PER_DAY = 288; // 5m buckets

	const toolColors: Record<string, string> = {
		nvim: 'var(--color-cmd-500)',
		claude: 'var(--color-run-500)',
		other: 'var(--color-bourbon-600)',
		away: 'hatch'
	};

	let unsub: (() => void) | null = null;

	onMount(async () => {
		// Seed from REST
		try { data = await getActivity('5m'); } catch { /* silent */ }

		// Subscribe to SSE deltas
		unsub = events.on('analytics:activity', (update) => {
			data = update;
		});

		const ro = new ResizeObserver((entries) => {
			containerWidth = entries[0].contentRect.width;
		});
		ro.observe(container);
		return () => ro.disconnect();
	});

	onDestroy(() => {
		if (unsub) unsub();
	});

	let barWidth = $derived(Math.max(1, containerWidth / BARS_PER_DAY));
	let xScale = $derived(scaleLinear().domain([0, BARS_PER_DAY]).range([0, containerWidth]));

	// --- Tools view ---

	function dominantTool(b: ActivityBucket): string | null {
		const active = b.nvim + b.claude + b.other;
		// If mostly away, show hatch pattern
		if (b.away > active && b.away > b.inactive) return 'away';
		if (active === 0) return null;
		if (b.nvim >= b.claude && b.nvim >= b.other) return 'nvim';
		if (b.claude >= b.nvim && b.claude >= b.other) return 'claude';
		return 'other';
	}

	function toToolSegments(buckets: ActivityBucket[]): { x: number; w: number; color: string }[] {
		if (!buckets.length) return [];
		const map = new Map(buckets.map((b) => [b.bucket, b]));
		const segments: { x: number; w: number; color: string }[] = [];
		let i = 0;
		while (i < BARS_PER_DAY) {
			const b = map.get(i);
			const tool = b ? dominantTool(b) : null;
			if (!tool) { i++; continue; }
			const start = i;
			while (i < BARS_PER_DAY) {
				const nb = map.get(i);
				if (!nb || dominantTool(nb) !== tool) break;
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

	let maxClaudeTotal = $derived(
		Math.max(1, ...(data?.today.buckets.map(b => b.claudeTotal) ?? [1]), ...(data?.yesterday.buckets.map(b => b.claudeTotal) ?? [1]))
	);

	function toClaudeSegments(buckets: ActivityBucket[]): { x: number; w: number; layers: { color: string; heightPct: number }[] }[] {
		if (!buckets.length) return [];
		const result: { x: number; w: number; layers: { color: string; heightPct: number }[] }[] = [];
		for (const b of buckets) {
			if (b.claudeTotal === 0) continue;
			const scale = b.claudeTotal / maxClaudeTotal;
			const layers: { color: string; heightPct: number }[] = [];
			if (b.claudeWorking > 0) layers.push({ color: 'var(--color-green-500)', heightPct: (b.claudeWorking / b.claudeTotal) * scale });
			if (b.claudeWaiting > 0) layers.push({ color: 'var(--color-run-500)', heightPct: (b.claudeWaiting / b.claudeTotal) * scale });
			if (b.claudeIdle > 0) layers.push({ color: 'var(--color-bourbon-600)', heightPct: (b.claudeIdle / b.claudeTotal) * scale });
			if (b.claudeUnknown > 0) layers.push({ color: 'var(--color-cmd-500)', heightPct: (b.claudeUnknown / b.claudeTotal) * scale });
			result.push({
				x: xScale(b.bucket),
				w: Math.max(1, barWidth),
				layers
			});
		}
		return result;
	}

	let todayToolSegs = $derived(data ? toToolSegments(data.today.buckets) : []);
	let yesterdayToolSegs = $derived(data ? toToolSegments(data.yesterday.buckets) : []);
	let todayClaudeSegs = $derived(data ? toClaudeSegments(data.today.buckets) : []);
	let yesterdayClaudeSegs = $derived(data ? toClaudeSegments(data.yesterday.buckets) : []);

	let nowX = $derived(data?.today.currentBucket != null ? xScale(data.today.currentBucket) : null);
	let todayMap = $derived(new Map(data?.today.buckets.map((b) => [b.bucket, b]) ?? []));

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

	let hoverBucket = $derived.by(() => hoverIdx !== null ? todayMap.get(hoverIdx) ?? null : null);

	let todayStats = $derived.by(() => {
		if (!data) return { nvim: 0, claude: 0, other: 0 };
		let nvim = 0, claude = 0, other = 0;
		for (const b of data.today.buckets) { nvim += b.nvim; claude += b.claude; other += b.other; }
		return { nvim: Math.round(nvim * 5 / 60), claude: Math.round(claude * 5 / 60), other: Math.round(other * 5 / 60) };
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
			</div>
			<div class="text-[10px] font-mono text-bourbon-600 h-4">
				{#if hoverIdx !== null}
					{bucketToTime(hoverIdx)}
					{#if hoverBucket}
						{#if activeView === 'tools'}
							{@const tool = dominantTool(hoverBucket)}
							— <span class="{tool === 'nvim' ? 'text-cmd-400' : tool === 'claude' ? 'text-run-400' : 'text-bourbon-500'}">{tool ?? 'inactive'}</span>
						{:else}
							— <span class="text-green-400">{hoverBucket.claudeWorking}</span> working
							· <span class="text-run-400">{hoverBucket.claudeWaiting}</span> waiting
							· {hoverBucket.claudeIdle} idle
							{#if hoverBucket.claudeUnknown > 0}· <span class="text-cmd-400">{hoverBucket.claudeUnknown}</span> ?{/if}
							<span class="text-bourbon-700">/ {hoverBucket.claudeTotal}</span>
						{/if}
					{/if}
				{:else if activeView === 'tools'}
					<span class="text-cmd-400">{minsToStr(todayStats.nvim)}</span> nvim
					· <span class="text-run-400">{minsToStr(todayStats.claude)}</span> claude
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
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-run-500 inline-block"></span>claude</span>
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-bourbon-600 inline-block"></span>other</span>
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-sm bg-hatch inline-block"></span>away</span>
				</div>
			{:else}
				<!-- Claude: yesterday strip -->
				<div class="relative h-1 rounded-sm overflow-hidden bg-bourbon-800/20 mb-1">
					{#each yesterdayClaudeSegs as seg}
						{#each seg.layers as layer}
							{@const bottomPct = seg.layers.slice(0, seg.layers.indexOf(layer)).reduce((s, l) => s + l.heightPct, 0)}
							<div class="absolute opacity-25" style="left:{seg.x}px; width:{seg.w}px; bottom:0; height:100%; background:{layer.color}"></div>
						{/each}
					{/each}
				</div>
				<!-- Claude: today strip (stacked bars) -->
				<div class="relative rounded-sm overflow-hidden bg-bourbon-800/20" style="height:{STRIP_HEIGHT}px">
					{#each todayClaudeSegs as seg}
						{@const totalH = seg.layers.reduce((s, l) => s + l.heightPct, 0)}
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
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-bourbon-600 inline-block"></span>idle</span>
					<span class="flex items-center gap-1"><span class="w-1.5 h-1.5 rounded-full bg-cmd-500 inline-block"></span>?</span>
				</div>
			{/if}
		</div>
	{/if}
</div>
