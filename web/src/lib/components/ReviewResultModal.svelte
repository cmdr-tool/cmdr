<script lang="ts">
	import { X, Wrench, ExternalLink, Pencil } from 'lucide-svelte';
	import { marked } from 'marked';
	import { startRefactor, updateClaudeTaskResult } from '$lib/api';

	let {
		result,
		taskId,
		prUrl,
		onclose,
		onupdate
	}: {
		result: string;
		taskId: number;
		prUrl?: string;
		onclose: () => void;
		onupdate?: (result: string) => void;
	} = $props();

	let editing = $state(false);
	let draft = $state('');
	let saving = $state(false);
	let refactoring = $state(false);

	let displayResult = $derived(editing ? draft : result);
	let html = $derived(marked(displayResult, { breaks: true }));

	async function handleSave() {
		saving = true;
		try {
			await updateClaudeTaskResult(taskId, draft);
			onupdate?.(draft);
			editing = false;
		} catch { /* silent */ }
		saving = false;
	}

	function handleCancel() {
		draft = result;
		editing = false;
	}

	async function handleRefactor() {
		refactoring = true;
		try {
			await startRefactor(taskId);
			onclose();
		} catch (e) {
			refactoring = false;
		}
	}
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
	onclick={onclose}
	onkeydown={(e) => { if (e.key === 'Escape') onclose(); }}
	role="dialog"
	tabindex="-1"
>
	<div
		class="bg-bourbon-900 border border-bourbon-800 rounded-2xl w-[90vw] max-w-3xl max-h-[85vh] flex flex-col overflow-hidden"
		onclick={(e) => e.stopPropagation()}
	>
		<div class="flex items-center justify-between px-6 py-4 border-b border-bourbon-800 shrink-0">
			<div class="flex items-center gap-3">
				<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">Review Result</h2>
				{#if !editing}
					<button
						onclick={() => { draft = result; editing = true; }}
						class="flex items-center gap-1 text-[10px] font-mono text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
					>
						<Pencil size={10} />
						edit
					</button>
				{:else}
					<div class="flex items-center gap-2">
						<button
							onclick={handleCancel}
							class="text-[10px] font-mono text-bourbon-600 hover:text-bourbon-400 transition-colors cursor-pointer"
						>cancel</button>
						<button
							onclick={handleSave}
							disabled={saving}
							class="text-[10px] font-mono text-run-400 hover:text-run-300 transition-colors cursor-pointer disabled:opacity-50"
						>{saving ? 'saving...' : 'save'}</button>
					</div>
				{/if}
			</div>
			<button
				onclick={onclose}
				class="text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
			>
				<X size={18} />
			</button>
		</div>
		{#if editing}
			<div class="overflow-auto bg-bourbon-950">
				<textarea
					bind:value={draft}
					class="w-full h-[60vh] bg-transparent text-xs font-mono text-bourbon-300 px-6 py-4 resize-none focus:outline-none select-text leading-relaxed"
				></textarea>
			</div>
		{:else}
			<div class="overflow-auto flex-1 px-6 py-4 bg-bourbon-950">
				<div class="prose prose-invert prose-sm max-w-none
					prose-headings:text-bourbon-200 prose-headings:font-display prose-headings:tracking-wider
					prose-p:text-bourbon-300
					prose-strong:text-bourbon-200
					prose-code:text-cmd-400 prose-code:bg-bourbon-950 prose-code:px-1 prose-code:py-0.5 prose-code:rounded
					prose-pre:bg-bourbon-950 prose-pre:border prose-pre:border-bourbon-800
					prose-a:text-cmd-400 prose-a:no-underline hover:prose-a:text-cmd-300
					prose-li:text-bourbon-300
					prose-blockquote:border-l-run-500 prose-blockquote:text-bourbon-400">
					{@html html}
				</div>
			</div>
		{/if}
		<div class="flex items-center justify-between px-6 py-3 border-t border-bourbon-800 shrink-0">
			<div>
				{#if prUrl}
					<a
						href={prUrl}
						target="_blank"
						rel="noopener"
						class="flex items-center gap-1.5 text-[10px] font-mono text-cmd-400 hover:text-cmd-300 transition-colors"
					>
						<ExternalLink size={12} />
						PR #{prUrl.split('/').pop()}
					</a>
				{/if}
			</div>
			<button
				onclick={handleRefactor}
				disabled={refactoring}
				class="flex items-center gap-1.5 text-[10px] font-mono text-cmd-400 hover:text-cmd-300 transition-colors cursor-pointer disabled:opacity-50"
			>
				<Wrench size={12} />
				{refactoring ? 'Starting...' : 'Start Refactor'}
			</button>
		</div>
	</div>
</div>
