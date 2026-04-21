<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { X, CircleQuestionMark, Terminal, Trash2, List } from 'lucide-svelte';
	import { renderMarkdown } from '$lib/markdown';
	import { getAgentTaskResult, continueAsk } from '$lib/api';
	import { dismiss as dismissTask } from '$lib/taskStore';
	import { events } from '$lib/events';

	let {
		taskId,
		onclose,
		title = 'Ask Claude',
		titleClass = 'text-run-500',
		icon: Icon = CircleQuestionMark,
		emptyHint = 'thinking',
		outputFormat = 'markdown',
		oncontinue,
	}: {
		taskId: number;
		onclose: () => void;
		title?: string;
		titleClass?: string;
		icon?: typeof CircleQuestionMark;
		emptyHint?: string;
		outputFormat?: string;
		oncontinue?: (() => Promise<void>) | null;
	} = $props();

	let status = $state<'running' | 'completed' | 'failed'>('running');
	let streamedText = $state('');
	let toolStatus = $state('');
	let errorMsg = $state('');
	let showToc = $state(false);
	let bodyEl: HTMLDivElement | undefined = $state();
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

	// --- Heading / section extraction ---

	interface TocEntry {
		id: string;
		level: number;
		text: string;
	}

	const headingRe = /^(#{1,4})\s+(.+)$/gm;

	function slugify(text: string): string {
		return text.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/(^-|-$)/g, '');
	}

	// All headings (for ID injection into rendered HTML)
	let allHeadings = $derived.by(() => {
		if (!streamedText) return [];
		const entries: TocEntry[] = [];
		for (const m of streamedText.matchAll(headingRe)) {
			const text = m[2].replace(/[`*_]/g, '');
			entries.push({ id: slugify(text), level: m[1].length, text });
		}
		return entries;
	});

	// TOC entries (skip h1 — it's usually just the document title)
	let tocEntries = $derived(allHeadings.filter(e => e.level > 1));

	// Inject IDs into rendered headings and add inline copy buttons
	let renderedHtml = $derived.by(() => {
		if (!streamedText) return '';
		const html = renderMd(streamedText);
		let idx = 0;
		return html.replace(/<(h[1-4])>([\s\S]*?)<\/h[1-4]>/g, (_match, tag, content) => {
			const entry = allHeadings[idx++];
			const id = entry?.id ?? '';
			return `<${tag} id="${id}" class="group/heading">${content} <button class="copy-section-btn invisible group-hover/heading:visible inline-flex items-center align-middle ml-1 text-bourbon-500 hover:text-run-400 cursor-pointer" data-section-id="${id}" title="Copy section">${copyIconSvg}</button></${tag}>`;
		});
	});

	function getSectionMarkdown(sectionId: string): string {
		const lines = streamedText.split('\n');
		let capturing = false;
		let captureLevel = 0;
		const result: string[] = [];

		for (const line of lines) {
			const m = line.match(/^(#{1,4})\s+(.+)$/);
			if (m) {
				const level = m[1].length;
				const id = slugify(m[2].replace(/[`*_]/g, ''));
				if (id === sectionId) {
					capturing = true;
					captureLevel = level;
					result.push(line);
					continue;
				}
				if (capturing && level <= captureLevel) {
					break; // next heading of same or higher level
				}
			}
			if (capturing) result.push(line);
		}
		return result.join('\n').trim();
	}

	import { createElement, Copy as CopyIcon, Check as CheckIcon } from 'lucide';

	const copyIconSvg = createElement(CopyIcon, { width: 12, height: 12 }).outerHTML;
	const checkIconSvg = createElement(CheckIcon, { width: 12, height: 12 }).outerHTML;

	async function copySection(sectionId: string) {
		const md = getSectionMarkdown(sectionId);
		if (!md) return;
		await navigator.clipboard.writeText(md);

		// Swap icon to checkmark briefly
		const btn = bodyEl?.querySelector(`.copy-section-btn[data-section-id="${CSS.escape(sectionId)}"]`);
		if (btn) {
			btn.innerHTML = checkIconSvg;
			btn.classList.remove('text-bourbon-500');
			btn.classList.add('text-green-400', 'visible');
			setTimeout(() => {
				btn.innerHTML = copyIconSvg;
				btn.classList.remove('text-green-400', 'visible');
				btn.classList.add('text-bourbon-500');
			}, 1500);
		}
	}

	function scrollToSection(id: string) {
		showToc = false;
		const el = bodyEl?.querySelector(`#${CSS.escape(id)}`);
		el?.scrollIntoView({ behavior: 'smooth', block: 'start' });
	}

	// Handle copy button clicks via event delegation (buttons are in innerHTML)
	function handleBodyClick(e: MouseEvent) {
		const btn = (e.target as HTMLElement).closest('.copy-section-btn') as HTMLElement | null;
		if (btn) {
			e.preventDefault();
			e.stopPropagation();
			const id = btn.dataset.sectionId;
			if (id) copySection(id);
		}
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
			const data = await getAgentTaskResult(taskId);
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
		unsub = events.on('agent:stream', (evt) => {
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
					getAgentTaskResult(taskId).then((data) => {
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
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
	onmousedown={(e) => { if (e.target === e.currentTarget) { showToc = false; onclose(); } }}
	onkeydown={(e) => { if (e.key === 'Escape') { showToc = false; onclose(); } }}
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
				{#if tocEntries.length > 1}
					<div class="relative flex items-center">
						<button
							onclick={() => { showToc = !showToc; }}
							class="flex items-center text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
							title="Table of contents"
						>
							<List size={12} />
						</button>
						{#if showToc}
							<!-- svelte-ignore a11y_no_static_element_interactions -->
							<div class="fixed inset-0 z-10" onclick={() => { showToc = false; }} onkeydown={() => {}}></div>
							<div class="absolute top-6 left-0 z-20 bg-bourbon-900 border border-bourbon-800 rounded-lg shadow-xl py-1.5 min-w-[240px] max-h-[300px] overflow-auto">
								{#each tocEntries as entry}
									<button
										onclick={() => scrollToSection(entry.id)}
										class="block w-full text-left px-3 py-1.5 text-[11px] font-mono text-bourbon-400 hover:text-bourbon-200 hover:bg-bourbon-800/50 cursor-pointer truncate"
										style="padding-left: {8 + (entry.level - 1) * 12}px"
									>
										{entry.text}
									</button>
								{/each}
							</div>
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
		<!-- svelte-ignore a11y_click_events_have_key_events -->
		<div
			bind:this={bodyEl}
			onclick={handleBodyClick}
			class="overflow-auto flex-1 px-6 py-4 bg-bourbon-950"
		>
			{#if status === 'failed'}
				<div class="text-red-400 text-xs font-mono">{errorMsg}</div>
			{:else if streamedText}
				{#if outputFormat === 'html'}
					<iframe
						srcdoc={streamedText}
						class="w-full h-full border-0 rounded-lg bg-white"
						sandbox="allow-same-origin"
						title="Agent output"
					></iframe>
				{:else if outputFormat === 'text'}
					<pre class="text-sm whitespace-pre-wrap wrap-break-word text-bourbon-300 font-mono">{streamedText}</pre>
				{:else}
					<div class={proseClasses}>
						{@html renderedHtml}
					</div>
				{/if}
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
