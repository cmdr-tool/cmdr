<script lang="ts">
	import { getStroke } from 'perfect-freehand';
	import { Pen, Eraser, Undo2, Check } from 'lucide-svelte';
	import type { StrokeData } from '$lib/blocks';

	let {
		width,
		height,
		strokes: initialStrokes,
		hideDone = false,
		onchange,
		ondone
	}: {
		width: number;
		height: number;
		strokes: StrokeData[];
		hideDone?: boolean;
		onchange: (strokes: StrokeData[]) => void;
		ondone: () => void;
	} = $props();

	const COLORS = ['#ff4444', '#ffaa00', '#44ff44', '#4488ff', '#ff44ff', '#ffffff', '#000000'];

	let svgEl: SVGSVGElement | undefined = $state(undefined);
	let strokes = $state<StrokeData[]>([]);

	$effect(() => {
		strokes = [...initialStrokes];
	});
	let currentPoints = $state<number[][]>([]);
	let drawing = $state(false);
	let tool = $state<'pen' | 'eraser'>('pen');
	let color = $state('#ff4444');
	let strokeSize = $state(5);
	let showColors = $state(false);

	function getPointerPos(e: PointerEvent): number[] {
		if (!svgEl) return [0, 0, 0.5];
		const rect = svgEl.getBoundingClientRect();
		return [
			e.clientX - rect.left,
			e.clientY - rect.top,
			e.pressure || 0.5
		];
	}

	function handlePointerDown(e: PointerEvent) {
		if (tool === 'eraser') {
			eraseAt(e);
			return;
		}
		drawing = true;
		currentPoints = [getPointerPos(e)];
		svgEl?.setPointerCapture(e.pointerId);
	}

	function handlePointerMove(e: PointerEvent) {
		if (tool === 'eraser' && e.buttons > 0) {
			eraseAt(e);
			return;
		}
		if (!drawing) return;
		currentPoints = [...currentPoints, getPointerPos(e)];
	}

	function handlePointerUp() {
		if (!drawing) return;
		drawing = false;
		if (currentPoints.length > 1) {
			strokes = [...strokes, { points: currentPoints, color, size: strokeSize }];
			onchange(strokes);
		}
		currentPoints = [];
	}

	function eraseAt(e: PointerEvent) {
		const pos = getPointerPos(e);
		const threshold = 12;
		const before = strokes.length;
		strokes = strokes.filter(stroke => {
			return !stroke.points.some(p =>
				Math.abs(p[0] - pos[0]) < threshold && Math.abs(p[1] - pos[1]) < threshold
			);
		});
		if (strokes.length !== before) {
			onchange(strokes);
		}
	}

	function clearAll() {
		strokes = [];
		onchange(strokes);
	}

	function getSvgPath(points: number[][], size: number): string {
		const outline = getStroke(points, {
			size,
			thinning: 0.5,
			smoothing: 0.5,
			streamline: 0.5,
		});
		if (outline.length === 0) return '';
		return outline.reduce(
			(acc, [x, y], i, arr) => {
				if (i === 0) return `M ${x.toFixed(1)},${y.toFixed(1)}`;
				const [cx, cy] = arr[i - 1];
				const mx = ((cx + x) / 2).toFixed(1);
				const my = ((cy + y) / 2).toFixed(1);
				return `${acc} Q ${cx.toFixed(1)},${cy.toFixed(1)} ${mx},${my}`;
			},
			''
		) + ' Z';
	}
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<svg
	bind:this={svgEl}
	{width}
	{height}
	class="absolute inset-0 {tool === 'eraser' ? 'cursor-cell' : 'cursor-crosshair'} touch-none"
	style="width: {width}px; height: {height}px;"
	onpointerdown={handlePointerDown}
	onpointermove={handlePointerMove}
	onpointerup={handlePointerUp}
	onpointerleave={handlePointerUp}
>
	{#each strokes as stroke}
		<path
			d={getSvgPath(stroke.points, stroke.size)}
			fill={stroke.color}
			opacity="0.85"
		/>
	{/each}

	{#if currentPoints.length > 1}
		<path
			d={getSvgPath(currentPoints, strokeSize)}
			fill={color}
			opacity="0.85"
		/>
	{/if}
</svg>

<!-- Toolbar -->
<div class="flex items-center gap-1.5 px-3 h-10 bg-bourbon-900 border-t border-bourbon-800">
	<button
		onclick={() => { tool = 'pen'; }}
		class="p-1.5 rounded transition-colors cursor-pointer
			{tool === 'pen' ? 'bg-cmd-500/20 text-cmd-400' : 'text-bourbon-600 hover:text-bourbon-400'}"
		title="Pen"
	>
		<Pen size={14} />
	</button>
	<button
		onclick={() => { tool = 'eraser'; }}
		class="p-1.5 rounded transition-colors cursor-pointer
			{tool === 'eraser' ? 'bg-cmd-500/20 text-cmd-400' : 'text-bourbon-600 hover:text-bourbon-400'}"
		title="Eraser"
	>
		<Eraser size={14} />
	</button>

	<!-- Color picker -->
	<div class="relative {tool !== 'pen' ? 'opacity-30 pointer-events-none' : ''}">
			<button
				onclick={() => { showColors = !showColors; }}
				class="p-1.5 rounded transition-colors cursor-pointer text-bourbon-600 hover:text-bourbon-400"
				title="Color"
			>
				<div class="w-3.5 h-3.5 rounded-full border border-bourbon-600" style="background-color: {color}"></div>
			</button>
			{#if showColors}
				<button type="button" class="fixed inset-0 z-40 cursor-default" onclick={() => { showColors = false; }} aria-label="Close color picker"></button>
				<div class="absolute bottom-full mb-1 left-0 z-50 flex gap-1 p-1.5 bg-bourbon-900 border border-bourbon-700 rounded-lg shadow-xl">
					{#each COLORS as c}
						<button
							onclick={() => { color = c; showColors = false; }}
							class="w-5 h-5 rounded-full border-2 cursor-pointer transition-transform hover:scale-110
								{c === color ? 'border-white' : 'border-bourbon-700'}"
							style="background-color: {c}"
							aria-label="Color {c}"
						></button>
					{/each}
				</div>
			{/if}
		</div>

	<!-- Stroke size -->
	<input
		type="range"
		min="1"
		max="8"
		bind:value={strokeSize}
		class="w-16 h-1 accent-cmd-500 {tool !== 'pen' ? 'opacity-30 pointer-events-none' : ''}"
	/>

	<button
		onclick={clearAll}
		class="flex items-center gap-1 px-1.5 py-1 rounded text-[9px] font-mono text-bourbon-600 hover:text-red-400 transition-colors cursor-pointer"
		title="Clear all annotations"
	>
		<Undo2 size={14} />
		clear
	</button>

	<div class="flex-1"></div>

	{#if !hideDone}
		<button
			onclick={ondone}
			class="flex items-center gap-1 px-2 py-1 rounded text-[10px] font-mono text-cmd-400 hover:text-cmd-300 transition-colors cursor-pointer"
		>
			<Check size={12} />
			done
		</button>
	{/if}
</div>
