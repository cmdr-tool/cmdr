<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { X, Send, Trash2 } from 'lucide-svelte';
	import {
		getRepos,
		getClaudeTaskResult,
		createDirective,
		saveDirective,
		submitDirective,
		dismissClaudeTask,
		type MonitoredRepo
	} from '$lib/api';
	import {
		type Block,
		parseBlocks,
		serializeBlocks,
		ensureTrailingTextBlock
	} from '$lib/blocks';
	import BlockEditor from './blocks/BlockEditor.svelte';

	let {
		initial,
		onclose,
		onsubmit
	}: {
		initial?: { repoPath?: string; content?: string; taskId?: number };
		onclose: () => void;
		onsubmit?: () => void;
	} = $props();

	let repos = $state<MonitoredRepo[]>([]);
	let taskId = $state<number | null>(null);
	let blocks = $state<Block[]>([]);
	let repoPath = $state('');
	let saving = $state(false);
	let submitting = $state(false);
	let lastSavedContent = '';
	let lastSavedRepo = '';
	let loaded = $state(false);
	let editorRef: { focusLast: () => void } | undefined = $state(undefined);

	onMount(async () => {
		repos = await getRepos();

		let content = initial?.content ?? '';
		repoPath = initial?.repoPath ?? '';

		if (!repoPath && repos.length > 0) {
			repoPath = repos[0].path;
		}

		if (initial?.taskId) {
			taskId = initial.taskId;
			try {
				const { result } = await getClaudeTaskResult(taskId);
				content = result || '';
			} catch { /* use initial content */ }
		} else {
			const res = await createDirective(repoPath, content);
			taskId = res.id;
		}

		blocks = ensureTrailingTextBlock(parseBlocks(content));
		lastSavedContent = content;
		lastSavedRepo = repoPath;
		loaded = true;
	});

	// Serialize + auto-save when blocks or repo change
	let serialized = $derived(serializeBlocks(blocks));

	$effect(() => {
		const s = serialized;
		const r = repoPath;

		const timer = setTimeout(async () => {
			if (!taskId || !loaded || (s === lastSavedContent && r === lastSavedRepo)) return;
			saving = true;
			await saveDirective(taskId, r, s);
			lastSavedContent = s;
			lastSavedRepo = r;
			saving = false;
		}, 1500);

		return () => clearTimeout(timer);
	});

	onDestroy(() => {
		const s = serializeBlocks(blocks);
		if (taskId && (s !== lastSavedContent || repoPath !== lastSavedRepo)) {
			saveDirective(taskId, repoPath, s);
		}
	});

	async function handleSubmit() {
		if (!taskId || !serialized.trim() || !repoPath) return;
		submitting = true;

		await saveDirective(taskId, repoPath, serialized);

		try {
			await submitDirective(taskId);
			onsubmit?.();
			onclose();
		} catch {
			submitting = false;
		}
	}

	async function handleDelete() {
		if (!taskId) return;
		await dismissClaudeTask(taskId);
		onclose();
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
			e.preventDefault();
			handleSubmit();
		}
	}
</script>

<!-- svelte-ignore a11y_click_events_have_key_events -->
<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
	onmousedown={(e) => { if (e.target === e.currentTarget) onclose(); }}
	onkeydown={handleKeydown}
	role="dialog"
	tabindex="-1"
>
	<div class="bg-bourbon-900 border border-bourbon-800 rounded-2xl w-[90vw] max-w-3xl min-h-[60vh] max-h-[85vh] flex flex-col overflow-hidden">
		<!-- Header -->
		<div class="flex items-center justify-between px-6 py-4 border-b border-bourbon-800 shrink-0">
			<div class="flex items-center gap-3">
				<h2 class="font-display text-xs font-bold uppercase tracking-widest text-cmd-400">New Directive</h2>
				{#if saving}
					<span class="text-[9px] font-mono text-bourbon-600">saving...</span>
				{/if}
			</div>
			<div class="flex items-center gap-2">
				<button
					onclick={handleDelete}
					class="text-bourbon-600 hover:text-red-400 transition-colors cursor-pointer p-1"
					title="Delete draft"
				>
					<Trash2 size={14} />
				</button>
				<button
					onclick={onclose}
					class="text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
				>
					<X size={18} />
				</button>
			</div>
		</div>

		<!-- Repo selector -->
		<div class="px-6 py-3 border-b border-bourbon-800/50 shrink-0">
			<label class="flex items-center gap-3">
				<span class="text-[10px] font-display font-bold uppercase tracking-widest text-bourbon-500">Target</span>
				<select
					bind:value={repoPath}
					class="flex-1 bg-bourbon-950 border border-bourbon-800 rounded-lg px-3 py-1.5 text-xs font-mono text-bourbon-200 focus:outline-none focus:border-cmd-500/50"
				>
					{#each repos as repo}
						<option value={repo.path}>{repo.name}</option>
					{/each}
				</select>
			</label>
		</div>

		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<!-- Block editor -->
		<div
			class="flex-1 overflow-y-auto bg-bourbon-950 px-8 py-4"
			onclick={(e) => {
				// Click on empty area focuses the last text block
				if (e.target === e.currentTarget && editorRef) editorRef.focusLast();
			}}
		>
			{#if loaded}
				<BlockEditor
					bind:this={editorRef}
					bind:blocks
					{repoPath}
					onchange={() => { blocks = [...blocks]; }}
					onsubmit={handleSubmit}
				/>
			{/if}
		</div>

		<!-- Footer -->
		<div class="flex items-center justify-between px-6 py-3 border-t border-bourbon-800 shrink-0">
			<span class="text-[9px] text-bourbon-700">⌘+Enter to submit</span>
			{#if submitting}
				<div class="flex items-center gap-2 text-bourbon-600">
					<div class="w-3 h-3 border-2 border-bourbon-700 border-t-cmd-500 rounded-full animate-spin"></div>
					<span class="text-[10px] font-mono">launching</span>
				</div>
			{:else}
				<button
					onclick={handleSubmit}
					disabled={!serialized.trim() || !repoPath}
					class="flex items-center gap-1.5 text-xs text-cmd-400 hover:text-cmd-300 transition-colors cursor-pointer
						disabled:opacity-40 disabled:cursor-not-allowed"
				>
					<Send size={12} />
					Launch Claude
				</button>
			{/if}
		</div>
	</div>
</div>
