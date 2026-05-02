<script lang="ts">
	import { X, CircleAlert, Sparkles } from 'lucide-svelte';

	type Props = {
		open: boolean;
		submitting?: boolean;
		error?: string | null;
		onSubmit: (prompt: string) => void | Promise<void>;
		onClose: () => void;
	};

	let { open, submitting = false, error = null, onSubmit, onClose }: Props = $props();

	let prompt = $state('');
	let textarea: HTMLTextAreaElement | undefined = $state(undefined);

	$effect(() => {
		if (open && textarea) textarea.focus();
		if (!open) prompt = '';
	});

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			e.preventDefault();
			onClose();
		} else if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
			e.preventDefault();
			submit();
		}
	}

	async function submit() {
		const trimmed = prompt.trim();
		if (!trimmed || submitting) return;
		await onSubmit(trimmed);
	}
</script>

{#if open}
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<div
		class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
		onpointerdown={(e) => { if (e.target === e.currentTarget) onClose(); }}
		onkeydown={handleKeydown}
		role="dialog"
		tabindex="-1"
	>
		<div class="bg-bourbon-900 border border-bourbon-800 rounded-2xl w-[90vw] max-w-2xl flex flex-col overflow-hidden">
			<div class="flex items-center justify-between px-6 py-4 border-b border-bourbon-800 shrink-0">
				<div class="flex items-center gap-3">
					<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">Generate trace</h2>
				</div>
				<button
					onclick={onClose}
					class="text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
				>
					<X size={18} />
				</button>
			</div>

			<div class="px-6 py-3 border-b border-bourbon-800/50 shrink-0">
				<p class="text-xs text-bourbon-500 leading-relaxed">
					Describe the specific flow you want modeled. The LLM will trace it against the latest graph snapshot.
				</p>
			</div>

			<div class="bg-bourbon-950 px-6 py-4">
				<textarea
					bind:this={textarea}
					bind:value={prompt}
					placeholder={'Trace what happens when a user hits POST /api/agent/tasks/spawn — from request validation through tmux pane creation.'}
					class="w-full min-h-[160px] bg-transparent border-none
						text-sm font-mono text-bourbon-200 placeholder:text-bourbon-700
						focus:outline-none resize-none leading-relaxed"
				></textarea>
			</div>

			{#if error}
				<div class="px-6 py-2 border-t border-bourbon-800/50 shrink-0 flex items-center gap-2 text-xs text-red-400">
					<CircleAlert size={12} />
					<span class="font-mono">{error}</span>
				</div>
			{/if}

			<div class="flex items-center justify-between gap-4 px-6 py-3 border-t border-bourbon-800 shrink-0">
				<span class="text-[9px] text-bourbon-700">⌘+Enter to submit</span>
				<button
					onclick={submit}
					disabled={submitting || !prompt.trim()}
					class="flex items-center gap-1.5 text-[10px] font-mono text-cmd-400 hover:text-cmd-300 transition-colors cursor-pointer disabled:opacity-50"
				>
					<Sparkles size={14} />
					{submitting ? 'Generating...' : 'Generate trace'}
				</button>
			</div>
		</div>
	</div>
{/if}
