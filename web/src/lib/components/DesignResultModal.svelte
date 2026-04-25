<script lang="ts">
	import { onMount } from 'svelte';
	import { X, Wrench, FileCheck, Trash2, Pencil, MessageSquarePlus, RotateCcw } from 'lucide-svelte';
	import { renderMarkdown, ensurePrismLoaded } from '$lib/markdown';
	import { renderMermaidBlocks } from '$lib/mermaid';
	import { spawnTask, reviseTask } from '$lib/api';
	import { playSound, SFX } from '$lib/sounds';
	import { loadAnnotations, saveAnnotations, type Annotation } from '$lib/annotations';
	import LaunchGuard from './LaunchGuard.svelte';
	import AnnotationLayer from './AnnotationLayer.svelte';

	let {
		result,
		taskId,
		repoPath = '',
		intent = '',
		onclose,
		onupdate
	}: {
		result: string;
		taskId: number;
		repoPath?: string;
		intent?: string;
		onclose: () => void;
		onupdate?: (result: string) => void;
	} = $props();

	let commitADR = $state(true);
	let bodyEl: HTMLDivElement | undefined = $state(undefined);
	let markdownVersion = $state(0);
	let revising = $state(false);
	let showNotesList = $state(false);
	let editingNoteId = $state<string | null>(null);
	let editingNoteDraft = $state('');

	// Annotation state (intentionally captures initial taskId — modal doesn't change tasks)
	// svelte-ignore state_referenced_locally
	let annotations = $state<Annotation[]>(loadAnnotations(taskId));

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
		markdownVersion;
		return renderMarkdown(md);
	}

	onMount(() => {
		void ensurePrismLoaded().then(() => { markdownVersion += 1; });
	});

	// Render mermaid diagrams after content mounts
	$effect(() => {
		markdownVersion;
		result;
		if (bodyEl) void renderMermaidBlocks(bodyEl);
	});

	// --- Annotation handlers ---
	function handleAnnotationsChange(updated: Annotation[]) {
		annotations = updated;
		saveAnnotations(taskId, updated);
	}

	// --- Note list actions ---
	function removeAnnotation(id: string) {
		const updated = annotations.filter(a => a.id !== id);
		handleAnnotationsChange(updated);
		if (updated.length === 0) showNotesList = false;
	}

	function startEditNote(ann: Annotation) {
		editingNoteId = ann.id;
		editingNoteDraft = ann.note;
	}

	function saveEditNote() {
		if (!editingNoteId) return;
		handleAnnotationsChange(annotations.map(a =>
			a.id === editingNoteId ? { ...a, note: editingNoteDraft.trim() } : a
		));
		editingNoteId = null;
		editingNoteDraft = '';
	}

	// --- Revise ---
	async function handleRevise() {
		if (annotations.length === 0) return;
		revising = true;
		try {
			await reviseTask(taskId, annotations.map(a => ({ exact: a.exact, note: a.note })));
			playSound(SFX.dispatch, 0.5);
			onclose();
		} catch {
			revising = false;
		}
	}
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
	onmousedown={(e) => { if (e.target === e.currentTarget) onclose(); }}
	onkeydown={(e) => { if (e.key === 'Escape') onclose(); }}
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
			</div>
			<button
				onclick={onclose}
				class="text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
			>
				<X size={18} />
			</button>
		</div>

		<!-- Body: rendered markdown with annotation layer -->
		<div bind:this={bodyEl} class="overflow-auto flex-1 px-6 py-4 bg-bourbon-950 relative">
			<div class={proseClasses}>
				{@html renderMd(result)}
			</div>
			{#if bodyEl}
				<AnnotationLayer
					containerEl={bodyEl}
					{annotations}
					onchange={handleAnnotationsChange}
				/>
			{/if}
		</div>

		<!-- Footer -->
		<div class="flex items-center justify-between px-6 py-3 border-t border-bourbon-800 shrink-0">
			{#if annotations.length > 0}
				<div class="relative">
					<button
						onclick={() => { showNotesList = !showNotesList; }}
						class="flex items-center gap-1 text-[10px] font-mono text-run-400 hover:text-run-300 transition-colors cursor-pointer"
					>
						<MessageSquarePlus size={10} />
						{annotations.length} note{annotations.length !== 1 ? 's' : ''}
					</button>

					{#if showNotesList}
						<!-- svelte-ignore a11y_no_static_element_interactions -->
						<div class="fixed inset-0 z-10" onclick={() => { showNotesList = false; }} onkeydown={() => {}}></div>
						<div class="absolute bottom-8 left-0 z-20 bg-bourbon-900 border border-bourbon-800 rounded-lg shadow-xl w-96 max-h-[calc(85vh-8rem)] overflow-auto">
							{#each annotations as ann}
								<div class="group/note flex items-start gap-2 px-3 py-2.5 border-b border-bourbon-800/50 last:border-b-0">
									{#if editingNoteId === ann.id}
										<div class="flex-1 flex flex-col gap-1.5">
											<div class="text-[10px] text-bourbon-500 italic line-clamp-1">{ann.exact}</div>
											<textarea
												bind:value={editingNoteDraft}
												class="w-full bg-bourbon-950 text-xs text-bourbon-200 px-2 py-1.5 rounded border border-bourbon-700 resize-none focus:outline-none focus:border-run-500/50"
												rows="4"
												onkeydown={(e) => {
													if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); saveEditNote(); }
													if (e.key === 'Escape') { editingNoteId = null; }
												}}
											></textarea>
											<div class="flex items-center justify-end gap-2">
												<button onclick={() => { editingNoteId = null; }} class="text-[10px] font-mono text-bourbon-600 hover:text-bourbon-400 cursor-pointer">cancel</button>
												<button onclick={saveEditNote} class="text-[10px] font-mono text-run-400 hover:text-run-300 cursor-pointer">save</button>
											</div>
										</div>
									{:else}
										<div class="flex-1 min-w-0">
											<div class="text-[10px] text-bourbon-500 italic truncate">{ann.exact}</div>
											<div class="text-xs text-bourbon-300 mt-0.5">{ann.note}</div>
										</div>
										<div class="flex items-center gap-1 shrink-0 invisible group-hover/note:visible">
											<button
												onclick={() => startEditNote(ann)}
												class="p-0.5 text-bourbon-600 hover:text-run-400 transition-colors cursor-pointer"
												title="Edit note"
											>
												<Pencil size={12} />
											</button>
											<button
												onclick={() => removeAnnotation(ann.id)}
												class="p-0.5 text-bourbon-600 hover:text-red-400 transition-colors cursor-pointer"
												title="Remove note"
											>
												<Trash2 size={12} />
											</button>
										</div>
									{/if}
								</div>
							{/each}
						</div>
					{/if}
				</div>
				<button
					onclick={handleRevise}
					disabled={revising}
					class="flex items-center gap-1.5 text-[10px] font-mono text-run-400 hover:text-run-300 transition-colors cursor-pointer disabled:opacity-50"
				>
					<RotateCcw size={12} class={revising ? 'animate-spin' : ''} />
					Revise design
				</button>
			{:else}
				{#if intent === 'new-feature'}
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
							commit as ADR
						</span>
					</button>
				{:else}
					<div></div>
				{/if}
				<LaunchGuard {repoPath} action={() => spawnTask(taskId, 'implementation', { commitADR: intent === 'new-feature' && commitADR })} onlaunched={onclose}>
					<Wrench size={12} />
					Start Implementation
				</LaunchGuard>
			{/if}
		</div>
	</div>
</div>
