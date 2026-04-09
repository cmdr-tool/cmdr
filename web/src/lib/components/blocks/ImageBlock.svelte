<script lang="ts">
	import { Upload } from 'lucide-svelte';
	import { getStroke } from 'perfect-freehand';
	import { uploadImage } from '$lib/api';
	import { parseStrokes, serializeStrokes, type StrokeData } from '$lib/blocks';
	import type { ImageBlock } from '$lib/blocks';
	import AnnotationOverlay from './AnnotationOverlay.svelte';

	function getSvgPathFromStroke(stroke: StrokeData): string {
		const outline = getStroke(stroke.points, {
			size: stroke.size,
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

	let {
		block,
		onchange
	}: {
		block: ImageBlock;
		onchange: (updates: Partial<ImageBlock>) => void;
	} = $props();

	let dragging = $state(false);
	let uploading = $state(false);
	let imgEl: HTMLImageElement | undefined = $state(undefined);
	let containerEl: HTMLDivElement | undefined = $state(undefined);
	let imgWidth = $state(0);
	let imgHeight = $state(0);
	let annotating = $state(false);
	let isSketch = $derived(block.path === 'sketch');

	// Auto-enter annotation mode for sketches
	$effect(() => {
		if (isSketch && !annotating) {
			annotating = true;
		}
	});

	// For sketches, use container width and a fixed aspect ratio
	$effect(() => {
		if (isSketch && containerEl) {
			imgWidth = containerEl.clientWidth;
			imgHeight = Math.round(imgWidth * 9 / 16); // 16:9 aspect ratio
		}
	});

	let src = $derived(
		!block.path ? '' :
		block.path.startsWith('/api/') ? block.path :
		block.path.startsWith('http') ? block.path :
		`/api/images/${block.path.split('/').pop()}`
	);

	let strokes = $derived(parseStrokes(block.meta));

	function handleImageLoad() {
		if (imgEl) {
			imgWidth = imgEl.clientWidth;
			imgHeight = imgEl.clientHeight;
		}
	}

	function handleStrokesChange(newStrokes: StrokeData[]) {
		onchange({ meta: serializeStrokes(newStrokes) });
	}

	async function handleFile(file: File) {
		if (!file.type.startsWith('image/')) return;
		uploading = true;
		try {
			const { url } = await uploadImage(file);
			onchange({ path: url });
		} catch { /* silent */ }
		uploading = false;
	}

	function handleDrop(e: DragEvent) {
		e.preventDefault();
		dragging = false;
		const file = e.dataTransfer?.files[0];
		if (file) handleFile(file);
	}

	function handleClick() {
		const input = document.createElement('input');
		input.type = 'file';
		input.accept = 'image/*';
		input.onchange = () => {
			const file = input.files?.[0];
			if (file) handleFile(file);
		};
		input.click();
	}
</script>

{#if !block.path}
	<!-- Upload zone -->
	<button
		type="button"
		class="flex flex-col items-center justify-center gap-2 py-6 w-full rounded-lg border border-dashed cursor-pointer transition-colors
			{dragging ? 'border-cmd-500 bg-cmd-500/10' : 'border-bourbon-700 hover:border-bourbon-500 bg-bourbon-950'}"
		onclick={handleClick}
		ondragover={(e) => { e.preventDefault(); dragging = true; }}
		ondragleave={() => { dragging = false; }}
		ondrop={handleDrop}
	>
		{#if uploading}
			<div class="w-4 h-4 border-2 border-bourbon-700 border-t-cmd-500 rounded-full animate-spin"></div>
			<span class="text-[10px] font-mono text-bourbon-600">uploading...</span>
		{:else}
			<Upload size={16} class="text-bourbon-600" />
			<span class="text-[10px] font-mono text-bourbon-600">click or drop image</span>
		{/if}
	</button>
{:else}
	<!-- Image/sketch with annotation layer -->
	<div bind:this={containerEl} class="rounded-lg border border-bourbon-700 overflow-hidden {isSketch ? 'bg-bourbon-200' : 'bg-bourbon-800/50'}">
		<div class="relative overflow-hidden rounded-t-lg">
			{#if !isSketch}
				<img
					bind:this={imgEl}
					{src}
					alt={block.caption || 'image'}
					class="w-full max-h-[400px] object-contain"
					onload={handleImageLoad}
				/>
			{:else}
				<!-- Blank sketch canvas -->
				<div style="width: {imgWidth}px; height: {imgHeight}px;"></div>
			{/if}

			{#if annotating && imgWidth > 0}
				<AnnotationOverlay
					width={imgWidth}
					height={imgHeight}
					strokes={strokes}
					hideDone={isSketch}
					onchange={handleStrokesChange}
					ondone={() => { if (!isSketch) annotating = false; }}
				/>
			{:else if strokes.length > 0 && imgWidth > 0}
				<!-- Read-only stroke preview -->
				<svg
					class="absolute inset-0 pointer-events-none"
					width={imgWidth}
					height={imgHeight}
					style="width: {imgWidth}px; height: {imgHeight}px;"
				>
					{#each strokes as stroke}
						<path
							d={getSvgPathFromStroke(stroke)}
							fill={stroke.color}
							opacity="0.85"
						/>
					{/each}
				</svg>
			{/if}
		</div>

		<!-- Bottom bar -->
		{#if !annotating}
			<div class="flex items-center justify-between px-3 py-1.5 border-t border-bourbon-800/50">
				{#if block.caption}
					<span class="text-[10px] text-bourbon-500 font-mono">{block.caption}</span>
				{:else}
					<span></span>
				{/if}
				<button
					onclick={() => { annotating = true; }}
					class="text-[10px] font-mono text-bourbon-600 hover:text-cmd-400 transition-colors cursor-pointer"
				>
					annotate
				</button>
			</div>
		{/if}
	</div>
{/if}

