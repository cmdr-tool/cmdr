<script lang="ts">
	import { X, Wrench, ExternalLink, Pencil, Trash2, MessageSquarePlus, Undo2 } from 'lucide-svelte';
	import { marked } from 'marked';
	import { startRefactor, updateClaudeTaskResult } from '$lib/api';
	import {
		parseReviewSections,
		reconstructMarkdown,
		setSectionUserNote,
		type ParsedReview,
		type ReviewSection
	} from '$lib/reviewParser';

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
	let bodyEl: HTMLDivElement | undefined = $state(undefined);
	let editHeight: number | null = $state(null);

	// Section note editing
	let noteSectionIdx: number | null = $state(null);
	let noteDraft = $state('');

	// Staged deletions (by section index)
	let stagedDeletions = $state(new Set<number>());
	let stagedCount = $derived(stagedDeletions.size);

	let parsedReview = $derived(parseReviewSections(result));
	let hasSections = $derived(parsedReview !== null && parsedReview.sections.length > 0);

	// Prose rendering for fallback / preamble / section bodies
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

	function renderMd(md: string): string {
		return marked(md, { breaks: true }) as string;
	}

	// Full fallback HTML for non-parsed view
	let fullHtml = $derived(renderMd(editing ? draft : result));

	// --- Raw edit ---
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

	// --- Section actions ---
	async function persistResult(newResult: string) {
		try {
			await updateClaudeTaskResult(taskId, newResult);
			onupdate?.(newResult);
		} catch { /* silent */ }
	}

	function stageDelete(idx: number) {
		stagedDeletions = new Set([...stagedDeletions, idx]);
	}

	function unstageDelete(idx: number) {
		const next = new Set(stagedDeletions);
		next.delete(idx);
		stagedDeletions = next;
	}

	function commitDeletions() {
		if (!parsedReview || stagedCount === 0) return;
		const updated: ParsedReview = {
			preamble: parsedReview.preamble,
			sections: parsedReview.sections.filter((_, i) => !stagedDeletions.has(i))
		};
		const newMd = reconstructMarkdown(updated);
		persistResult(newMd);
		stagedDeletions = new Set();
	}

	function startNote(idx: number) {
		if (!parsedReview) return;
		noteSectionIdx = idx;
		noteDraft = parsedReview.sections[idx].userNote ?? '';
	}

	function cancelNote() {
		noteSectionIdx = null;
		noteDraft = '';
	}

	function saveNote() {
		if (!parsedReview || noteSectionIdx === null) return;
		const note = noteDraft.trim() || null;
		const updatedSection = setSectionUserNote(parsedReview.sections[noteSectionIdx], note);
		const updated: ParsedReview = {
			preamble: parsedReview.preamble,
			sections: parsedReview.sections.map((s, i) => i === noteSectionIdx ? updatedSection : s)
		};
		const newMd = reconstructMarkdown(updated);
		persistResult(newMd);
		cancelNote();
	}

	function removeNote(idx: number) {
		if (!parsedReview) return;
		const updatedSection = setSectionUserNote(parsedReview.sections[idx], null);
		const updated: ParsedReview = {
			preamble: parsedReview.preamble,
			sections: parsedReview.sections.map((s, i) => i === idx ? updatedSection : s)
		};
		const newMd = reconstructMarkdown(updated);
		persistResult(newMd);
	}

	// --- Refactor ---
	async function handleRefactor() {
		refactoring = true;
		try {
			// Flush any pending changes before starting
			if (stagedCount > 0) {
				const updated: ParsedReview = {
					preamble: parsedReview!.preamble,
					sections: parsedReview!.sections.filter((_, i) => !stagedDeletions.has(i))
				};
				await persistResult(reconstructMarkdown(updated));
				stagedDeletions = new Set();
			}
			await startRefactor(taskId);
			onclose();
		} catch (e) {
			refactoring = false;
		}
	}

	function autofocus(node: HTMLElement) {
		requestAnimationFrame(() => node.focus());
	}

	// Strip body of user note block for rendering (we show it separately)
	function bodyWithoutNote(body: string): string {
		return body.replace(/\n*> User response:\s*\n((?:> .*(?:\n|$))*)/, '').trimEnd();
	}
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
	onmousedown={(e) => { if (e.target === e.currentTarget) onclose(); }}
	onkeydown={(e) => { if (e.key === 'Escape') { if (noteSectionIdx !== null) cancelNote(); else onclose(); }}}
	role="dialog"
	tabindex="-1"
>
	<div
		class="bg-bourbon-900 border border-bourbon-800 rounded-2xl w-[90vw] max-w-5xl max-h-[85vh] flex flex-col overflow-hidden"
	>
		<!-- Header -->
		<div class="flex items-center justify-between px-6 py-4 border-b border-bourbon-800 shrink-0">
			<div class="flex items-center gap-3">
				<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">Review Result</h2>
				{#if !editing}
					<button
						onclick={() => { if (bodyEl) editHeight = bodyEl.offsetHeight; draft = result; editing = true; }}
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

		<!-- Body -->
		{#if editing}
			<div class="overflow-auto bg-bourbon-950" style:height={editHeight ? `${editHeight}px` : 'calc(85vh - 7rem)'}>
				<textarea
					bind:value={draft}
					class="w-full h-full bg-transparent text-xs font-mono text-bourbon-300 px-6 py-4 resize-none focus:outline-none select-text leading-relaxed"
				></textarea>
			</div>
		{:else if hasSections}
			<!-- Structured section view -->
			<div bind:this={bodyEl} class="overflow-auto flex-1 bg-bourbon-950">
				<!-- Preamble -->
				{#if parsedReview && parsedReview.preamble.trim()}
					<div class="px-6 pt-4 pb-2">
						<div class={proseClasses}>
							{@html renderMd(parsedReview.preamble)}
						</div>
					</div>
				{/if}

				<!-- Sections -->
				{#if parsedReview}
					<div class="flex flex-col gap-2 px-4 py-3">
						{#each parsedReview.sections as section, idx}
							{@const isStaged = stagedDeletions.has(idx)}
							<div class="group/section rounded-xl overflow-hidden transition-all
								{isStaged
									? 'bg-red-950/20 border border-red-500/20'
									: 'bg-bourbon-900/60 border border-bourbon-800/60'}">
								<!-- Section header -->
								<div class="flex items-center gap-3 px-4 py-2.5">
									<span class="text-[10px] font-display font-bold uppercase tracking-widest
										{isStaged ? 'text-red-400/50 line-through' : 'text-cmd-400'}">
										{section.category ? `${section.number}. ${section.category}` : `P${section.number}`}
									</span>
									<span class="text-xs truncate flex-1
										{isStaged ? 'text-bourbon-500 line-through' : 'text-bourbon-200'}">
										{section.title}
									</span>
									{#if isStaged}
										<button
											onclick={() => unstageDelete(idx)}
											class="flex items-center gap-1 p-1 text-bourbon-500 hover:text-bourbon-200 transition-colors cursor-pointer"
											title="Undo removal"
										>
											<Undo2 size={14} />
											<span class="text-[10px] font-mono">undo</span>
										</button>
									{:else}
										<div class="flex items-center gap-1 invisible group-hover/section:visible">
											<button
												onclick={() => startNote(idx)}
												class="p-1 text-bourbon-600 hover:text-run-400 transition-colors cursor-pointer"
												title="Add guidance note"
											>
												<MessageSquarePlus size={14} />
											</button>
											<button
												onclick={() => stageDelete(idx)}
												class="p-1 text-bourbon-600 hover:text-red-400 transition-colors cursor-pointer"
												title="Remove this finding"
											>
												<Trash2 size={14} />
											</button>
										</div>
									{/if}
								</div>

								<!-- Section body (collapsed when staged) -->
								{#if !isStaged}
									<div class="px-4 pb-3">
										<div class={proseClasses}>
											{@html renderMd(bodyWithoutNote(section.body))}
										</div>
									</div>

									<!-- Existing user note -->
									{#if section.userNote && noteSectionIdx !== idx}
										<div class="mx-4 mb-3 flex items-start gap-2 bg-run-500/8 border border-run-500/20 rounded-lg px-3 py-2">
											<span class="text-[10px] font-mono text-run-400 shrink-0 mt-0.5">your note:</span>
											<span class="text-xs text-bourbon-200 flex-1 select-text">{section.userNote}</span>
											<button
												onclick={() => startNote(idx)}
												class="shrink-0 text-bourbon-600 hover:text-run-400 transition-colors cursor-pointer mt-0.5"
												title="Edit note"
											>
												<Pencil size={14} />
											</button>
											<button
												onclick={() => removeNote(idx)}
												class="shrink-0 text-bourbon-600 hover:text-red-400 transition-colors cursor-pointer mt-0.5"
												title="Remove note"
											>
												<Trash2 size={14} />
											</button>
										</div>
									{/if}

									<!-- Note editor -->
									{#if noteSectionIdx === idx}
										<div class="mx-4 mb-3 border border-run-500/30 rounded-lg overflow-hidden">
											<textarea
												use:autofocus
												bind:value={noteDraft}
												placeholder="Add guidance for this finding... (e.g. 'Go with suggestion 1' or 'Skip this — not applicable yet')"
												class="w-full bg-run-500/5 text-xs text-bourbon-200 px-3 py-2 resize-none focus:outline-none placeholder:text-bourbon-700 select-text"
												rows="2"
												onkeydown={(e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); saveNote(); } if (e.key === 'Escape') cancelNote(); }}
											></textarea>
											<div class="flex items-center justify-between px-3 py-1.5">
												<span class="text-[9px] text-bourbon-700">Cmd+Enter to save</span>
												<div class="flex items-center gap-3">
													<button onclick={cancelNote} class="text-[10px] text-bourbon-600 hover:text-bourbon-400 cursor-pointer">cancel</button>
													<button onclick={saveNote} class="text-[10px] text-run-400 hover:text-run-300 cursor-pointer">save</button>
												</div>
											</div>
										</div>
									{/if}
								{/if}
							</div>
						{/each}
					</div>
				{/if}
			</div>
		{:else}
			<!-- Fallback: full rendered markdown -->
			<div bind:this={bodyEl} class="overflow-auto flex-1 px-6 py-4 bg-bourbon-950">
				<div class={proseClasses}>
					{@html fullHtml}
				</div>
			</div>
		{/if}

		<!-- Footer -->
		<div class="flex items-center justify-between px-6 py-3 border-t border-bourbon-800 shrink-0">
			<div class="flex items-center gap-3">
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
				{#if hasSections && parsedReview}
					<span class="text-[10px] font-mono text-bourbon-600">
						{parsedReview.sections.length} finding{parsedReview.sections.length !== 1 ? 's' : ''}
					</span>
				{/if}
				{#if stagedCount > 0}
					<button
						onclick={commitDeletions}
						class="flex items-center gap-1.5 text-[10px] font-mono text-red-400 hover:text-red-300 transition-colors cursor-pointer"
					>
						<Trash2 size={12} />
						Remove {stagedCount} finding{stagedCount !== 1 ? 's' : ''}
					</button>
				{/if}
			</div>
			<div class="flex items-center gap-3">
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
</div>
