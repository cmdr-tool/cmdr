<script lang="ts">
	import { Plus, Type, FileCode, Image, PenLine } from 'lucide-svelte';
	import { computePosition, flip, shift, offset } from '@floating-ui/dom';

	let {
		oninsert,
		last = false
	}: {
		oninsert: (type: 'text' | 'coderef' | 'image' | 'sketch') => void;
		last?: boolean;
	} = $props();

	let open = $state(false);
	let buttonEl: HTMLButtonElement | undefined = $state(undefined);
	let menuEl: HTMLDivElement | undefined = $state(undefined);

	function select(type: 'text' | 'coderef' | 'image' | 'sketch') {
		open = false;
		oninsert(type);
	}

	// Portal container — appended to document.body to escape overflow clipping
	let portalEl: HTMLDivElement;
	function portal(node: HTMLElement) {
		portalEl = document.createElement('div');
		document.body.appendChild(portalEl);
		portalEl.appendChild(node);
		return {
			destroy() { portalEl?.remove(); }
		};
	}

	// Position menu with Floating UI
	$effect(() => {
		if (open && buttonEl && menuEl) {
			computePosition(buttonEl, menuEl, {
				strategy: 'fixed',
				placement: 'bottom',
				middleware: [offset(4), flip(), shift({ padding: 8 })],
			}).then(({ x, y }) => {
				if (menuEl) {
					menuEl.style.left = `${x}px`;
					menuEl.style.top = `${y}px`;
					menuEl.style.visibility = 'visible';
				}
			});
		}
	});
</script>

<div class="relative flex items-center gap-2 {last ? 'py-1.5' : 'py-0.5'} group/inserter">
	<div class="flex-1 h-px bg-bourbon-800/50 group-hover/inserter:bg-bourbon-700/50 transition-colors"></div>
	<div>
		<button
			bind:this={buttonEl}
			onclick={() => { open = !open; }}
			class="flex items-center gap-1 rounded-full font-mono
				text-bourbon-700 hover:text-bourbon-400 border border-bourbon-800/50 hover:border-bourbon-700
				transition-colors cursor-pointer
				{last ? 'px-2.5 py-1 text-[10px]' : 'px-2 py-0.5 text-[9px]'}"
		>
			<Plus size={10} />
			{last ? 'add block' : 'insert block'}
		</button>
	</div>
	<div class="flex-1 h-px bg-bourbon-800/50 group-hover/inserter:bg-bourbon-700/50 transition-colors"></div>
</div>

{#if open}
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<div use:portal>
		<div
			class="fixed inset-0 z-40"
			onclick={() => { open = false; }}
			role="presentation"
		></div>
		<div
			bind:this={menuEl}
			class="fixed z-50 bg-bourbon-900 border border-bourbon-700 rounded-lg shadow-xl py-1 min-w-[140px]"
			style="visibility: hidden;"
		>
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
	</div>
{/if}
