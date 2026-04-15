<script lang="ts">
	import type { Component } from 'svelte';
	import { onMount, onDestroy } from 'svelte';
	import { X, CircleQuestionMark, Terminal, Trash2 } from 'lucide-svelte';
	import { renderMarkdown } from '$lib/markdown';
	import { getClaudeTaskResult, continueAsk } from '$lib/api';
	import { dismiss as dismissTask } from '$lib/taskStore';
	import { events } from '$lib/events';

	let {
		taskId,
		onclose,
		title = 'Ask Claude',
		titleClass = 'text-run-500',
		icon: Icon = CircleQuestionMark as Component<{ size: number; class: string }>,
		emptyHint = 'thinking',
		oncontinue,
	}: {
		taskId: number;
		onclose: () => void;
		title?: string;
		titleClass?: string;
		icon?: Component<{ size: number; class: string }>;
		emptyHint?: string;
		oncontinue?: (() => Promise<void>) | null;
	} = $props();

	let status = $state<'running' | 'completed' | 'failed'>('running');
	let streamedText = $state('');
	let toolStatus = $state('');
	let errorMsg = $state('');
	let unsub: (() => void) | null = null;

	const proseClasses = `prose prose-invert prose-sm max-w-none
		prose-headings:text-bourbon-200 prose-headings:font-display prose-headings:tracking-wider
		prose-p:text-bourbon-300
		prose-strong:text-bourbon-200
		prose-code:text-run-400 prose-code:bg-bourbon-800/50 prose-code:px-1 prose-code:py-0.5 prose-code:rounded
		prose-pre:bg-bourbon-900 prose-pre:border prose-pre:border-bourbon-800
		[&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:rounded-none
		prose-a:text-cmd-400 prose-a:no-underline hover:prose-a:text-cmd-300
		prose-li:text-bourbon-300
		prose-blockquote:border-l-run-500 prose-blockquote:text-bourbon-400`;

	// Extract ★ Insight blocks and render them as styled callouts
	const insightRe = /`[★✦][\s]*Insight[\s]*─+`\n([\s\S]*?)\n`─+`/g;

	function renderMd(md: string): string {
		const processed = md.replace(insightRe, (_match, content: string) => {
			const inner = renderMarkdown(content.trim());
			return `<div class="insight-callout">`
				+ `<div class="insight-header">★ Insight</div>`
				+ `<div class="insight-body">${inner}</div>`
				+ `</div>`;
		});
		return renderMarkdown(processed);
	}

	// Friendly tool status messages
	function toolStatusLabel(tool: string, detail: string): string {
		switch (tool) {
			case 'Glob': return detail ? `searching for ${detail}` : 'searching files';
			case 'Grep': return detail ? `searching for "${detail}"` : 'searching files';
			case 'Read': return detail ? `reading ${detail}` : 'reading file';
			default: return tool.toLowerCase();
		}
	}

	async function handleContinue() {
		try {
			if (oncontinue) {
				await oncontinue();
			} else {
				await continueAsk(taskId);
			}
			onclose();
		} catch { /* silent */ }
	}

	onMount(async () => {
		// Check if task is already completed (e.g., clicking a finished task)
		try {
			const data = await getClaudeTaskResult(taskId);
			if (data.status === 'completed' && data.result) {
				streamedText = data.result;
				status = 'completed';
				return;
			}
			if (data.status === 'failed') {
				status = 'failed';
				errorMsg = data.errorMsg || 'Unknown error';
				return;
			}
		} catch { /* task might be brand new, proceed to stream */ }

		// Subscribe to streaming events
		unsub = events.on('claude:ask:stream', (evt) => {
			if (evt.id !== taskId) return;

			switch (evt.type) {
				case 'text':
					streamedText = evt.text ?? '';
					toolStatus = '';
					break;
				case 'tool':
					toolStatus = toolStatusLabel(evt.tool ?? '', evt.detail ?? '');
					break;
				case 'done':
					status = 'completed';
					// Re-fetch to get the stored result (may differ from streamed text)
					getClaudeTaskResult(taskId).then((data) => {
						if (data.result) streamedText = data.result;
					}).catch(() => {});
					break;
				case 'error':
					status = 'failed';
					errorMsg = evt.error ?? 'Unknown error';
					break;
			}
		});
	});

	onDestroy(() => {
		unsub?.();
	});

	let renderedHtml = $derived(streamedText ? renderMd(streamedText) : '');
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
	onmousedown={(e) => { if (e.target === e.currentTarget) onclose(); }}
	onkeydown={(e) => { if (e.key === 'Escape') onclose(); }}
	role="dialog"
	tabindex="-1"
>
	<div class="bg-bourbon-900 border border-bourbon-800 rounded-2xl w-[90vw] max-w-3xl max-h-[85vh] flex flex-col overflow-hidden">
		<!-- Header -->
		<div class="flex items-center justify-between px-6 py-4 border-b border-bourbon-800 shrink-0">
			<div class="flex items-center gap-3">
				<Icon size={14} class={titleClass} />
				<h2 class="font-display text-xs font-bold uppercase tracking-widest {titleClass}">{title}</h2>
				{#if status === 'running'}
					<div class="flex items-center gap-2">
						<div class="w-2.5 h-2.5 border-2 border-bourbon-700 border-t-cmd-500 rounded-full animate-spin"></div>
						{#if toolStatus}
							<span class="text-[10px] font-mono text-bourbon-500 animate-pulse">{toolStatus}</span>
						{:else if !streamedText}
							<span class="text-[10px] font-mono text-bourbon-500 animate-pulse">{emptyHint}</span>
						{/if}
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

		<!-- Body -->
		<div class="overflow-auto flex-1 px-6 py-4 bg-bourbon-950">
			{#if status === 'failed'}
				<div class="text-red-400 text-xs font-mono">{errorMsg}</div>
			{:else if streamedText}
				<div class={proseClasses}>
					{@html renderedHtml}
				</div>
			{:else}
				<!-- Empty state while waiting for first text -->
				<div class="flex flex-col items-center justify-center py-12 gap-3">
					<div class="w-6 h-6 border-2 border-bourbon-700 border-t-cmd-500 rounded-full animate-spin"></div>
					{#if toolStatus}
						<span class="text-xs font-mono text-bourbon-500">{toolStatus}</span>
					{:else}
						<span class="text-xs font-mono text-bourbon-500">{emptyHint}</span>
					{/if}
				</div>
			{/if}
		</div>

		<!-- Footer -->
		{#if status === 'completed' || status === 'failed'}
			<div class="flex items-center justify-between px-6 py-3 border-t border-bourbon-800 shrink-0">
				<button
					onclick={async () => {
						await dismissTask(taskId);
						onclose();
					}}
					class="flex items-center gap-1.5 text-[10px] font-mono text-bourbon-600 hover:text-red-400 transition-colors cursor-pointer"
				>
					<Trash2 size={12} />
					Dismiss
				</button>
				{#if status === 'completed'}
					<button
						onclick={handleContinue}
						class="flex items-center gap-1.5 text-[10px] font-mono text-cmd-400 hover:text-cmd-300 transition-colors cursor-pointer"
					>
						<Terminal size={12} />
						Continue in interactive session
					</button>
				{/if}
			</div>
		{/if}
	</div>
</div>
