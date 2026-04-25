<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { X, Send, Trash2 } from 'lucide-svelte';
	import LaunchGuard from './LaunchGuard.svelte';
	import { compositeAndSerialize } from '$lib/composite';
	import {
		getRepos,
		getAgentTaskResult,
		getDirectiveIntents,
		saveDirective,
		submitDirective,
		type MonitoredRepo,
		type DirectiveIntent
	} from '$lib/api';
	import { create as createTask, dismiss as dismissTask } from '$lib/taskStore';

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
	let intents = $state<DirectiveIntent[]>([]);
	let selectedIntent = $state('');
	let taskId = $state<number | null>(null);
	let blocks = $state<Block[]>([]);
	let repoPath = $state('');
	let saving = $state(false);
	let submitting = $state(false);
	let submitProgress = $state('');
	let lastSavedContent = '';
	let lastSavedRepo = '';
	let loaded = $state(false);
	let editorRef: { focusLast: () => void } | undefined = $state(undefined);

	onMount(async () => {
		[repos, intents] = await Promise.all([getRepos(), getDirectiveIntents()]);

		let content = initial?.content ?? '';
		repoPath = initial?.repoPath ?? '';

		if (!repoPath && repos.length > 0) {
			repoPath = repos[0].path;
		}

		if (initial?.taskId) {
			taskId = initial.taskId;
			try {
				const data = await getAgentTaskResult(taskId);
				content = data.result || '';
				if (data.intent) selectedIntent = data.intent;
			} catch { /* use initial content */ }
		} else {
			const res = await createTask(repoPath, content);
			taskId = res.id;
		}

		blocks = ensureTrailingTextBlock(parseBlocks(content));
		lastSavedContent = content;
		lastSavedRepo = repoPath;
		loaded = true;
	});

	// Serialize + auto-save when blocks or repo change
	let serialized = $derived(serializeBlocks(blocks));

	let lastSavedIntent = '';

	$effect(() => {
		const s = serialized;
		const r = repoPath;
		const i = selectedIntent;

		const timer = setTimeout(async () => {
			if (!taskId || !loaded || (s === lastSavedContent && r === lastSavedRepo && i === lastSavedIntent)) return;
			saving = true;
			await saveDirective(taskId, r, s, i || undefined);
			lastSavedContent = s;
			lastSavedRepo = r;
			lastSavedIntent = i;
			saving = false;
		}, 1500);

		return () => clearTimeout(timer);
	});

	onDestroy(() => {
		const s = serializeBlocks(blocks);
		if (taskId && (s !== lastSavedContent || repoPath !== lastSavedRepo || selectedIntent !== lastSavedIntent)) {
			saveDirective(taskId, repoPath, s, selectedIntent || undefined);
		}
	});

	async function handleSubmit() {
		if (!taskId || !serialized.trim() || !repoPath) return;
		submitting = true;
		submitProgress = 'preparing';

		try {
			// Composite images/sketches with annotations into flat PNGs
			const hasRichBlocks = blocks.some(b => b.type === 'image' && (b.meta || b.path === 'sketch'));
			let finalMarkdown = serialized;

			if (hasRichBlocks) {
				submitProgress = 'compositing images';
				finalMarkdown = await compositeAndSerialize(blocks, (cur, total) => {
					submitProgress = `compositing image ${cur}/${total}`;
				});
			}

			// Save the cleaned markdown
			submitProgress = 'saving';
			await saveDirective(taskId, repoPath, finalMarkdown);

			// Dispatch to Claude
			submitProgress = 'dispatching';
			await submitDirective(taskId, selectedIntent || undefined);
			onsubmit?.();
			onclose();
		} catch {
			submitting = false;
			submitProgress = '';
		}
	}

	async function handleDelete() {
		if (!taskId) return;
		await dismissTask(taskId);
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
	<div class="bg-bourbon-900 border border-bourbon-800 rounded-2xl w-[90vw] max-w-5xl min-h-[60vh] max-h-[85vh] flex flex-col overflow-hidden">
		<!-- Header -->
		<div class="flex items-center justify-between px-6 py-4 border-b border-bourbon-800 shrink-0">
			<div class="flex items-center gap-3">
				<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">New Directive</h2>
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
		<div class="px-6 py-3 border-b border-bourbon-800/50 shrink-0 flex items-center gap-3">
			<span class="text-[10px] font-display font-bold uppercase tracking-widest text-bourbon-500 w-16 shrink-0">Target</span>
				<select
					bind:value={repoPath}
					class="flex-1 bg-bourbon-950 border border-bourbon-800 rounded-lg px-3 h-8 text-xs font-mono text-bourbon-200 focus:outline-none focus:border-cmd-500/50"
				>
					{#each repos as repo}
						<option value={repo.path}>{repo.name}</option>
					{/each}
				</select>
		</div>

		<!-- Intent selector -->
		{#if intents.length > 0}
			{@const intentLabels: Record<string, string> = {
				'bug-fix': 'Fix a Bug',
				'new-feature': 'Design & Build',
				'refactor': 'Restructure Code',
				'analysis': 'Analyze Code',
			}}
			<div class="px-6 py-2 border-b border-bourbon-800/50 shrink-0 flex items-center gap-3">
				<span class="text-[10px] font-display font-bold uppercase tracking-widest text-bourbon-500 w-16 shrink-0">Intent</span>
				<div class="flex flex-wrap gap-1.5">
					{#each intents as intent}
						<button
							onclick={() => { selectedIntent = selectedIntent === intent.id ? '' : intent.id; }}
							class="px-2.5 py-1 rounded-full text-[10px] font-mono transition-colors cursor-pointer
								{selectedIntent === intent.id
									? 'bg-cmd-500/20 text-cmd-400 border border-cmd-500/40'
									: 'text-bourbon-600 border border-bourbon-800 hover:text-bourbon-400 hover:border-bourbon-700'}"
						>
							{intentLabels[intent.id] ?? intent.name}
						</button>
					{/each}
				</div>
			</div>
		{/if}

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
					<span class="text-[10px] font-mono">{submitProgress || 'dispatching'}</span>
				</div>
			{:else}
				<LaunchGuard {repoPath} action={handleSubmit} disabled={!serialized.trim() || !repoPath}>
					<Send size={12} />
					Dispatch to Claude
				</LaunchGuard>
			{/if}
		</div>
	</div>
</div>
