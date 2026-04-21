<script lang="ts">
	import { CircleQuestionMark, Send } from 'lucide-svelte';
	import { askAgent } from '$lib/api';

	let open = $state(false);
	let question = $state('');
	let submitting = $state(false);
	let textareaEl: HTMLTextAreaElement | undefined = $state(undefined);

	async function handleSubmit() {
		const q = question.trim();
		if (!q || submitting) return;
		submitting = true;
		try {
			await askAgent(q);
			question = '';
			open = false;
		} catch { /* silent */ }
		submitting = false;
	}

	function handleOpen() {
		open = true;
		requestAnimationFrame(() => textareaEl?.focus());
	}
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
{#if open}
	<!-- Backdrop -->
	<div
		class="fixed inset-0 z-40"
		onmousedown={() => { if (!submitting) open = false; }}
	></div>

	<!-- Expanded input -->
	<div class="fixed bottom-6 right-6 z-50 w-96">
		<div class="bg-bourbon-900 border border-bourbon-800 rounded-2xl shadow-2xl shadow-black/40 overflow-hidden">
			<div class="flex items-center gap-2 px-4 py-2.5 border-b border-bourbon-800/50">
				<CircleQuestionMark size={14} class="text-run-500 shrink-0" />
				<span class="font-display text-[10px] font-bold uppercase tracking-widest text-run-500">Ask Claude</span>
			</div>
			<div class="p-3">
				<textarea
					bind:this={textareaEl}
					bind:value={question}
					placeholder="Ask your knowledge base..."
					class="w-full bg-bourbon-950 border border-bourbon-800 rounded-lg px-3 py-2.5 text-xs font-mono text-bourbon-200 placeholder:text-bourbon-700 focus:outline-none focus:border-cmd-500/50 transition-colors resize-none leading-relaxed"
					rows="3"
					onkeydown={(e) => {
						if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
							e.preventDefault();
							handleSubmit();
						}
						if (e.key === 'Escape') { e.preventDefault(); open = false; }
					}}
					disabled={submitting}
				></textarea>
			</div>
			<div class="flex items-center justify-between px-4 py-2 border-t border-bourbon-800/50">
				<span class="text-[9px] text-bourbon-700">⌘+Enter to ask</span>
				{#if submitting}
					<div class="flex items-center gap-2">
						<div class="w-3 h-3 border-2 border-bourbon-700 border-t-cmd-500 rounded-full animate-spin"></div>
						<span class="text-[10px] font-mono text-bourbon-500">asking</span>
					</div>
				{:else}
					<button
						onclick={handleSubmit}
						disabled={!question.trim()}
						class="flex items-center gap-1.5 text-[10px] font-mono text-cmd-400 hover:text-cmd-300 transition-colors cursor-pointer disabled:opacity-30 disabled:cursor-not-allowed"
					>
						<Send size={10} />
						Ask
					</button>
				{/if}
			</div>
		</div>
	</div>
{:else}
	<!-- Collapsed bubble -->
	<button
		onclick={handleOpen}
		class="fixed bottom-6 right-6 z-40 flex items-center gap-2 bg-bourbon-900 border border-bourbon-800 rounded-full pl-3.5 pr-4 py-2.5 shadow-lg shadow-black/30 hover:border-bourbon-700 hover:shadow-xl transition-all cursor-pointer group"
	>
		<CircleQuestionMark size={16} class="text-run-500 group-hover:text-run-400 transition-colors" />
		<span class="font-display text-[10px] font-bold uppercase tracking-widest text-bourbon-500 group-hover:text-bourbon-400 transition-colors">Ask</span>
	</button>
{/if}
