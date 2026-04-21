<script lang="ts">
	import { X, Pencil, Wrench, MessageSquarePlus, Trash2, Undo2, FileCheck } from 'lucide-svelte';
	import { renderMarkdown } from '$lib/markdown';
	import { updateAgentTaskResult, spawnTask } from '$lib/api';
	import LaunchGuard from './LaunchGuard.svelte';
	import {
		parseADR,
		reconstructADR,
		setSectionNote,
		type ParsedADR,
		type ADRSection
	} from '$lib/adrParser';

	let {
		result,
		taskId,
		repoPath = '',
		onclose,
		onupdate
	}: {
		result: string;
		taskId: number;
		repoPath?: string;
		onclose: () => void;
		onupdate?: (result: string) => void;
	} = $props();

	let editing = $state(false);
	let draft = $state('');
	let saving = $state(false);
	let commitADR = $state(true);
	let bodyEl: HTMLDivElement | undefined = $state(undefined);
	let editHeight: number | null = $state(null);

	// Section note editing
	let noteSectionIdx: number | null = $state(null);
	let noteDraft = $state('');

	let parsedADR = $derived(parseADR(result));
	let hasSections = $derived(parsedADR !== null && parsedADR.sections.length > 0);

	const proseClasses = `prose prose-invert prose-sm max-w-none
		prose-headings:text-bourbon-200 prose-headings:font-display prose-headings:tracking-wider
		prose-p:text-bourbon-300
		prose-strong:text-bourbon-200
		prose-code:text-run-400 prose-code:bg-bourbon-800/50 prose-code:px-1 prose-code:py-0.5 prose-code:rounded
		prose-pre:bg-bourbon-900 prose-pre:border prose-pre:border-bourbon-800
		[&_pre_code]:bg-transparent [&_pre_code]:p-0 [&_pre_code]:rounded-none
		prose-a:text-cmd-400 prose-a:no-underline hover:prose-a:text-cmd-300
		prose-li:text-bourbon-300
		prose-blockquote:border-l-cmd-500 prose-blockquote:text-bourbon-400`;

	function renderMd(md: string): string {
		return renderMarkdown(md);
	}

	let fullHtml = $derived(renderMd(editing ? draft : result));

	// --- Raw edit ---
	async function handleSave() {
		saving = true;
		try {
			await updateAgentTaskResult(taskId, draft);
			onupdate?.(draft);
			editing = false;
		} catch { /* silent */ }
		saving = false;
	}

	function handleCancel() {
		draft = result;
		editing = false;
	}

	// --- Section note actions ---
	async function persistResult(newResult: string) {
		try {
			await updateAgentTaskResult(taskId, newResult);
			onupdate?.(newResult);
		} catch { /* silent */ }
	}

	function startNote(idx: number) {
		if (!parsedADR) return;
		noteSectionIdx = idx;
		noteDraft = parsedADR.sections[idx].userNote ?? '';
	}

	function cancelNote() {
		noteSectionIdx = null;
		noteDraft = '';
	}

	function saveNote() {
		if (!parsedADR || noteSectionIdx === null) return;
		const note = noteDraft.trim() || null;
		const updatedSection = setSectionNote(parsedADR.sections[noteSectionIdx], note);
		const updated: ParsedADR = {
			...parsedADR,
			sections: parsedADR.sections.map((s, i) => i === noteSectionIdx ? updatedSection : s)
		};
		const newMd = reconstructADR(updated);
		persistResult(newMd);
		cancelNote();
	}

	function removeNote(idx: number) {
		if (!parsedADR) return;
		const updatedSection = setSectionNote(parsedADR.sections[idx], null);
		const updated: ParsedADR = {
			...parsedADR,
			sections: parsedADR.sections.map((s, i) => i === idx ? updatedSection : s)
		};
		const newMd = reconstructADR(updated);
		persistResult(newMd);
	}




	function autofocus(node: HTMLElement) {
		requestAnimationFrame(() => node.focus());
	}

	function bodyWithoutNote(body: string): string {
		return body.replace(/\n*> Reviewer note:\s*\n((?:> .*(?:\n|$))*)/, '').trimEnd();
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
				<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">Design Review</h2>
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
			<!-- Structured ADR view -->
			<div bind:this={bodyEl} class="overflow-auto flex-1 bg-bourbon-950">
				<!-- Title -->
				{#if parsedADR}
					<div class="px-6 pt-5 pb-2">
						<h1 class="font-display text-lg text-bourbon-100 tracking-wide">{parsedADR.title}</h1>
					</div>
					{#if parsedADR.preamble.trim()}
						<div class="px-6 pb-2">
							<div class={proseClasses}>
								{@html renderMd(parsedADR.preamble)}
							</div>
						</div>
					{/if}
				{/if}

				<!-- Sections -->
				{#if parsedADR}
					<div class="flex flex-col gap-2 px-4 py-3">
						{#each parsedADR.sections as section, idx}
							<div class="group/section rounded-xl overflow-hidden bg-bourbon-900/60 border border-bourbon-800/60">
								<!-- Section header -->
								<div class="flex items-center gap-3 px-4 py-2.5">
									<span class="text-[10px] font-display font-bold uppercase tracking-widest text-run-400">
										{section.heading}
									</span>
									<div class="flex items-center gap-1 ml-auto invisible group-hover/section:visible">
										<button
											onclick={() => startNote(idx)}
											class="p-1 text-bourbon-600 hover:text-run-400 transition-colors cursor-pointer"
											title="Add note"
										>
											<MessageSquarePlus size={14} />
										</button>
									</div>
								</div>

								<!-- Section body -->
								<div class="px-4 pb-3">
									<div class={proseClasses}>
										{@html renderMd(bodyWithoutNote(section.body))}
									</div>
								</div>

								<!-- Existing note -->
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
											placeholder="Add a note for the implementer... (e.g. 'Use the existing pattern from X' or 'Skip this — we'll handle it separately')"
											class="w-full bg-run-500/5 text-xs text-bourbon-200 px-3 py-2 resize-none focus:outline-none placeholder:text-bourbon-700 select-text"
											rows="2"
											onkeydown={(e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); saveNote(); } if (e.key === 'Escape') cancelNote(); }}
										></textarea>
										<div class="flex items-center justify-between px-3 py-1.5">
											<span class="text-[9px] text-bourbon-700">⌘+Enter to save</span>
											<div class="flex items-center gap-3">
												<button onclick={cancelNote} class="text-[10px] text-bourbon-600 hover:text-bourbon-400 cursor-pointer">cancel</button>
												<button onclick={saveNote} class="text-[10px] text-run-400 hover:text-run-300 cursor-pointer">save</button>
											</div>
										</div>
									</div>
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
			<div class="flex items-center gap-4">
				<!-- Commit ADR toggle -->
				<button
					onclick={() => { commitADR = !commitADR; }}
					class="flex items-center gap-2 cursor-pointer group"
				>
					<div class="w-7 h-4 rounded-full transition-colors relative
						{commitADR ? 'bg-cmd-500' : 'bg-bourbon-700'}">
						<div class="absolute top-0.5 w-3 h-3 rounded-full bg-bourbon-100 shadow transition-all
							{commitADR ? 'left-3.5' : 'left-0.5'}"></div>
					</div>
					<span class="text-[10px] font-mono transition-colors
						{commitADR ? 'text-bourbon-400' : 'text-bourbon-600'}">
						<FileCheck size={10} class="inline -mt-0.5" />
						commit ADR to repo
					</span>
				</button>
			</div>
			<LaunchGuard {repoPath} action={() => spawnTask(taskId, 'implementation', { commitADR })} onlaunched={onclose}>
				<Wrench size={12} />
				Start Implementation
			</LaunchGuard>
		</div>
	</div>
</div>
