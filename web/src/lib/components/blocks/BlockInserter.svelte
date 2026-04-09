<script lang="ts">
	import { Plus, Type, FileCode, Image, PenLine } from 'lucide-svelte';

	let {
		oninsert,
		last = false
	}: {
		oninsert: (type: 'text' | 'coderef' | 'image' | 'sketch') => void;
		last?: boolean;
	} = $props();

	let open = $state(false);

	function select(type: 'text' | 'coderef' | 'image' | 'sketch') {
		open = false;
		oninsert(type);
	}
</script>

<div class="relative flex items-center gap-2 {last ? 'py-1.5' : 'py-0.5'} group/inserter">
	<div class="flex-1 h-px bg-bourbon-800/50 group-hover/inserter:bg-bourbon-700/50 transition-colors"></div>
	<div class="relative">
		<button
			onclick={() => { open = !open; }}
			class="flex items-center gap-1 rounded-full font-mono
				text-bourbon-700 hover:text-bourbon-400 border border-bourbon-800/50 hover:border-bourbon-700
				transition-colors cursor-pointer
				{last ? 'px-2.5 py-1 text-[10px]' : 'px-2 py-0.5 text-[9px]'}"
		>
			<Plus size={10} />
			{last ? 'add block' : 'insert block'}
		</button>

		{#if open}
			<!-- svelte-ignore a11y_click_events_have_key_events -->
			<div
				class="fixed inset-0 z-40"
				onclick={() => { open = false; }}
				role="presentation"
			></div>
			<div class="absolute left-1/2 -translate-x-1/2 top-full mt-1 z-50 bg-bourbon-900 border border-bourbon-700 rounded-lg shadow-xl py-1 min-w-[140px]">
			<button
				onclick={() => select('text')}
				class="flex items-center gap-2 w-full px-3 py-1.5 text-[10px] font-mono text-bourbon-300 hover:bg-bourbon-800 transition-colors cursor-pointer"
			>
				<Type size={12} />
				Text
			</button>
			<button
				onclick={() => select('coderef')}
				class="flex items-center gap-2 w-full px-3 py-1.5 text-[10px] font-mono text-bourbon-300 hover:bg-bourbon-800 transition-colors cursor-pointer"
			>
				<FileCode size={12} />
				Code reference
			</button>
			<button
				onclick={() => select('image')}
				class="flex items-center gap-2 w-full px-3 py-1.5 text-[10px] font-mono text-bourbon-300 hover:bg-bourbon-800 transition-colors cursor-pointer"
			>
				<Image size={12} />
				Image
			</button>
			<button
				onclick={() => select('sketch')}
				class="flex items-center gap-2 w-full px-3 py-1.5 text-[10px] font-mono text-bourbon-300 hover:bg-bourbon-800 transition-colors cursor-pointer"
			>
				<PenLine size={12} />
				Sketch
			</button>
		</div>
	{/if}
	</div>
	<div class="flex-1 h-px bg-bourbon-800/50 group-hover/inserter:bg-bourbon-700/50 transition-colors"></div>
</div>
