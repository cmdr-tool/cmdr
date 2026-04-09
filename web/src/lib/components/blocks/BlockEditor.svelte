<script lang="ts">
	import { X } from 'lucide-svelte';
	import { uploadImage } from '$lib/api';
	import {
		type Block,
		createTextBlock,
		createCodeRefBlock,
		createImageBlock,
		ensureTrailingTextBlock
	} from '$lib/blocks';

	import TextBlockComponent from './TextBlock.svelte';
	import CodeRefBlock from './CodeRefBlock.svelte';
	import ImageBlock from './ImageBlock.svelte';
	import BlockInserter from './BlockInserter.svelte';
	import FileAutocomplete from './FileAutocomplete.svelte';

	let {
		blocks = $bindable(),
		repoPath,
		onchange,
		onsubmit,
		ontrigger
	}: {
		blocks: Block[];
		repoPath: string;
		onchange: () => void;
		onsubmit: () => void;
		ontrigger?: (type: string, query: string, rect: DOMRect, blockIndex: number) => void;
	} = $props();

	import { onMount } from 'svelte';

	let dragIdx: number | null = $state(null);
	let dropIdx: number | null = $state(null);
	let dropSide: 'above' | 'below' = $state('above'); // which block visually shows the indicator
	let container: HTMLDivElement | undefined = $state(undefined);

	// Autocomplete state
	let acQuery = $state('');
	let acPosition = $state({ x: 0, y: 0 });
	let acBlockIdx = $state(-1);
	let showAutocomplete = $state(false);

	function handleTrigger(type: string, query: string, rect: DOMRect, blockIndex: number) {
		if (type === 'file') {
			acQuery = query;
			acPosition = { x: rect.left, y: rect.bottom + 4 };
			acBlockIdx = blockIndex;
			showAutocomplete = true;
		} else if (type === 'dismiss') {
			showAutocomplete = false;
		}
	}

	function handleAutocompleteSelect(file: string) {
		showAutocomplete = false;
		const block = blocks[acBlockIdx];
		if (!block) return;

		// If selecting from a code ref block, just update the ref
		if (block.type === 'coderef') {
			updateBlock(acBlockIdx, { ref: file });
			return;
		}

		if (block.type !== 'text') return;

		// Find the @query in the text and remove it
		const atPattern = '@' + acQuery;
		const content = block.content;
		const atIdx = content.lastIndexOf(atPattern);
		if (atIdx < 0) return;

		// Split text: before @, after @query
		const before = content.slice(0, atIdx).trimEnd();
		const after = content.slice(atIdx + atPattern.length).trimStart();

		// Update the text block with content before @
		updateBlock(acBlockIdx, { content: before });

		// Insert code ref block after the text block
		const codeRef = createCodeRefBlock(file);
		blocks.splice(acBlockIdx + 1, 0, codeRef);

		// If there's text after, add another text block
		if (after) {
			const afterBlock = createTextBlock(after);
			blocks.splice(acBlockIdx + 2, 0, afterBlock);
		}

		blocks = ensureTrailingTextBlock([...blocks]);
		onchange();
	}

	function handleAutocompleteCancel() {
		showAutocomplete = false;
	}

	onMount(() => {
		requestAnimationFrame(() => focusLast());
	});

	export function focusLast() {
		if (!container) return;
		// Find all textareas, focus the last one
		const areas = container.querySelectorAll('textarea');
		if (areas.length > 0) {
			areas[areas.length - 1].focus();
		}
	}

	function updateBlock(index: number, updates: Partial<Block>) {
		blocks[index] = { ...blocks[index], ...updates } as Block;
		blocks = ensureTrailingTextBlock([...blocks]);
		onchange();
	}

	function removeBlock(index: number) {
		blocks.splice(index, 1);
		if (blocks.length === 0) {
			blocks = [createTextBlock()];
		}
		// Merge adjacent text blocks
		mergeAdjacentTextBlocks();
		blocks = ensureTrailingTextBlock([...blocks]);
		onchange();
	}

	function mergeAdjacentTextBlocks() {
		for (let i = blocks.length - 1; i > 0; i--) {
			if (blocks[i].type === 'text' && blocks[i - 1].type === 'text') {
				const prev = blocks[i - 1] as import('$lib/blocks').TextBlock;
				const curr = blocks[i] as import('$lib/blocks').TextBlock;
				prev.content = [prev.content, curr.content].filter(s => s.trim()).join('\n\n');
				blocks.splice(i, 1);
			}
		}
	}

	function insertBlock(index: number, type: 'text' | 'coderef' | 'image') {
		// Don't insert text next to text — just focus the adjacent one
		if (type === 'text') {
			const next = index < blocks.length ? blocks[index] : null;
			const prev = index > 0 ? blocks[index - 1] : null;
			if (next?.type === 'text' || prev?.type === 'text') {
				// Focus the adjacent text block — prefer next, fall back to prev
				const targetIdx = next?.type === 'text' ? index : index - 1;
				requestAnimationFrame(() => {
					if (!container) return;
					const areas = container.querySelectorAll('textarea');
					let areaIdx = 0;
					for (let b = 0; b < blocks.length; b++) {
						if (blocks[b].type === 'text') {
							if (b === targetIdx) { areas[areaIdx]?.focus(); break; }
							areaIdx++;
						}
					}
				});
				return;
			}
		}

		let block: Block;
		if (type === 'image') {
			block = createImageBlock('');
		} else if (type === 'coderef') {
			block = createCodeRefBlock();
		} else {
			block = createTextBlock();
		}
		blocks.splice(index, 0, block);
		blocks = ensureTrailingTextBlock([...blocks]);
		onchange();

		// Focus the new block's input after render
		requestAnimationFrame(() => {
			if (!container) return;
			const inputs = container.querySelectorAll('textarea, input[type="text"]');
			// Find the input that belongs to the new block by index
			let inputIdx = 0;
			for (let b = 0; b < blocks.length && b <= index; b++) {
				if (blocks[b].type === 'text' || blocks[b].type === 'coderef') {
					if (b === index) { (inputs[inputIdx] as HTMLElement)?.focus(); break; }
					inputIdx++;
				}
			}
		});
	}

	// --- Image paste handling ---
	async function handlePaste(e: ClipboardEvent, blockIndex: number) {
		const items = e.clipboardData?.items;
		if (!items) return;

		for (const item of items) {
			if (item.type.startsWith('image/')) {
				e.preventDefault();
				const blob = item.getAsFile();
				if (!blob) return;
				const { url } = await uploadImage(blob);
				const block = createImageBlock(url);
				blocks.splice(blockIndex + 1, 0, block);
				blocks = ensureTrailingTextBlock([...blocks]);
				onchange();
				return;
			}
		}
	}

	// --- Drag and drop ---
	function handleDragStart(e: DragEvent, index: number) {
		dragIdx = index;
		if (e.dataTransfer) {
			e.dataTransfer.effectAllowed = 'move';
			e.dataTransfer.setData('text/plain', String(index));
		}
	}

	// dropIdx represents the insertion point: block will be placed BEFORE dropIdx.
	// So dropIdx=0 means top, dropIdx=blocks.length means bottom.

	function wouldMove(from: number, insertBefore: number): boolean {
		// No-op if inserting right before or right after the dragged block
		return insertBefore !== from && insertBefore !== from + 1;
	}

	function handleDragOver(e: DragEvent, index: number) {
		e.preventDefault();
		if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
		if (dragIdx === null) return;

		// Determine top/bottom half of the block element
		const rect = (e.currentTarget as HTMLElement).getBoundingClientRect();
		const midY = rect.top + rect.height / 2;
		const isTop = e.clientY < midY;
		const insertBefore = isTop ? index : index + 1;

		if (wouldMove(dragIdx, insertBefore)) {
			dropIdx = insertBefore;
			dropSide = isTop ? 'above' : 'below';
		} else {
			dropIdx = null;
		}
	}

	function handleDrop(e: DragEvent) {
		e.preventDefault();
		if (dragIdx === null || dropIdx === null || !wouldMove(dragIdx, dropIdx)) {
			dragIdx = null;
			dropIdx = null;
			return;
		}

		const [moved] = blocks.splice(dragIdx, 1);
		const insertAt = dropIdx > dragIdx ? dropIdx - 1 : dropIdx;
		blocks.splice(insertAt, 0, moved);
		blocks = ensureTrailingTextBlock([...blocks]);
		dragIdx = null;
		dropIdx = null;
		onchange();
	}

	function handleDragEnd() {
		dragIdx = null;
		dropIdx = null;
	}
</script>

<div
	bind:this={container}
	class="flex flex-col gap-0.5"
>
	{#each blocks as block, i (block.id)}
		{@const isDragging = dragIdx === i}
		{@const showAbove = dropIdx === i && dropSide === 'above' && dragIdx !== null}
		{@const showBelow = dropIdx === i + 1 && dropSide === 'below' && dragIdx !== null}

		<!-- Drop indicator above this block -->
		{#if showAbove}
			<div class="h-0.5 bg-cmd-500 rounded-full mx-8"></div>
		{/if}

		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<div
			class="flex gap-1 group/block {isDragging ? 'opacity-30' : ''}"
			ondragover={(e) => handleDragOver(e, i)}
			ondrop={handleDrop}
		>
			<!-- Gutter -->
			<div class="w-8 -ml-8 shrink-0 flex flex-col items-center pt-1 gap-1 opacity-0 group-hover/block:opacity-100 transition-opacity">
				{#if blocks.length > 1 || block.type !== 'text'}
					<button
						draggable="true"
						ondragstart={(e) => handleDragStart(e, i)}
						ondragend={handleDragEnd}
						aria-label="Drag to reorder"
						class="w-3 h-5 rounded cursor-grab active:cursor-grabbing bg-grip hover:bg-grip-hover"
					></button>
					<button
						onclick={() => removeBlock(i)}
						class="text-bourbon-700 hover:text-red-400 cursor-pointer py-1"
					>
						<X size={14} />
					</button>
				{/if}
			</div>

			<!-- Block content -->
			<div class="flex-1 min-w-0">
				{#if block.type === 'text'}
					<TextBlockComponent
						{block}
						onchange={(content) => updateBlock(i, { content })}
						onpaste={(e) => handlePaste(e, i)}
						ontrigger={(type, query, rect) => handleTrigger(type, query, rect, i)}
					/>
				{:else if block.type === 'coderef'}
					<CodeRefBlock
						{block}
						{repoPath}
						onchange={(ref) => updateBlock(i, { ref })}
						ontrigger={(type, query, rect) => handleTrigger(type, query, rect, i)}
					/>
				{:else if block.type === 'image'}
					<ImageBlock {block} onchange={(updates) => updateBlock(i, updates)} />
				{/if}
			</div>
		</div>

		<!-- Drop indicator below this block -->
		{#if showBelow}
			<div class="h-0.5 bg-cmd-500 rounded-full mx-8"></div>
		{/if}

		<!-- Inserter between blocks -->
		<BlockInserter oninsert={(type) => insertBlock(i + 1, type)} last={i === blocks.length - 1} />
	{/each}
</div>

{#if showAutocomplete}
	<FileAutocomplete
		query={acQuery}
		{repoPath}
		position={acPosition}
		onselect={handleAutocompleteSelect}
		oncancel={handleAutocompleteCancel}
	/>
{/if}
