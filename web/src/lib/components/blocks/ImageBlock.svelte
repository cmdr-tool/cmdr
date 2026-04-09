<script lang="ts">
	import { Image, Upload } from 'lucide-svelte';
	import { uploadImage } from '$lib/api';
	import type { ImageBlock } from '$lib/blocks';

	let {
		block,
		onchange
	}: {
		block: ImageBlock;
		onchange: (updates: Partial<ImageBlock>) => void;
	} = $props();

	let dragging = $state(false);
	let uploading = $state(false);

	let src = $derived(
		!block.path ? '' :
		block.path.startsWith('/api/') ? block.path :
		block.path.startsWith('http') ? block.path :
		`/api/images/${block.path.split('/').pop()}`
	);

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
	<!-- Image preview -->
	<div class="rounded-lg border border-bourbon-700 overflow-hidden bg-bourbon-800/50">
		<img
			{src}
			alt={block.caption || 'image'}
			class="w-full max-h-[400px] object-contain"
		/>
		{#if block.caption}
			<div class="px-3 py-1.5 text-[10px] text-bourbon-500 font-mono border-t border-bourbon-800/50">
				{block.caption}
			</div>
		{/if}
	</div>
{/if}
