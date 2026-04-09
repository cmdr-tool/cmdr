<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { CheckCircle, XCircle, Wrench, GitPullRequestArrow, X } from 'lucide-svelte';
	import { getClaudeTasks, getClaudeTaskResult, dismissClaudeTask, dismissAllClaudeTasks, type ClaudeTask } from '$lib/api';
	import { events } from '$lib/events';

	let {
		onviewresult
	}: {
		onviewresult: (task: ClaudeTask, result: string) => void;
	} = $props();

	let tasks = $state<ClaudeTask[]>([]);
	let loaded = $state(false);
	let unsub: (() => void) | null = null;
	let unsubStatus: (() => void) | null = null;

	// Hide tasks that have completed their full lifecycle (refactored + completed)
	let visibleTasks = $derived(tasks.filter(t => !(t.refactored && t.status === 'completed')));
	let activeCount = $derived(visibleTasks.filter(t => t.status === 'running' || t.status === 'pending' || t.status === 'refactoring').length);
	let dismissableCount = $derived(visibleTasks.filter(t => t.status === 'completed' || t.status === 'failed' || t.status === 'resolved' || t.status === 'refactoring').length);

	onMount(async () => {
		try { tasks = await getClaudeTasks(); } catch { /* silent */ }
		loaded = true;

		unsub = events.on('claude:task', (evt) => {
			const idx = tasks.findIndex(t => t.id === evt.id);
			if (idx >= 0) {
				tasks[idx] = {
					...tasks[idx],
					status: evt.status as ClaudeTask['status'],
					...(evt.title && { title: evt.title as string }),
					...(evt.prUrl && { prUrl: evt.prUrl as string }),
				};
				tasks = [...tasks];
			} else {
				getClaudeTasks().then(t => { tasks = t; }).catch(() => {});
			}
		});

		// Refetch on SSE reconnect to catch any missed events
		let firstStatus = true;
		unsubStatus = events.on('status', () => {
			if (firstStatus) { firstStatus = false; return; } // skip initial connect
			getClaudeTasks().then(t => { tasks = t; }).catch(() => {});
		});
	});

	onDestroy(() => {
		if (unsub) unsub();
		if (unsubStatus) unsubStatus();
	});

	async function viewResult(task: ClaudeTask) {
		try {
			const { result } = await getClaudeTaskResult(task.id);
			onviewresult(task, result);
		} catch { /* silent */ }
	}

	async function dismiss(task: ClaudeTask) {
		await dismissClaudeTask(task.id);
		tasks = tasks.filter(t => t.id !== task.id);
	}

	async function clearAll() {
		await dismissAllClaudeTasks();
		tasks = tasks.filter(t => t.status === 'running' || t.status === 'pending');
	}

	function timeAgo(dateStr: string): string {
		const date = new Date(dateStr);
		const now = new Date();
		const seconds = Math.floor((now.getTime() - date.getTime()) / 1000);
		if (seconds < 60) return 'just now';
		const minutes = Math.floor(seconds / 60);
		if (minutes < 60) return `${minutes}m ago`;
		const hours = Math.floor(minutes / 60);
		if (hours < 24) return `${hours}h ago`;
		const days = Math.floor(hours / 24);
		return `${days}d ago`;
	}

	function shortSha(sha: string): string { return sha.slice(0, 7); }

	function repoName(path: string): string {
		return path.split('/').pop() ?? path;
	}
</script>

{#if loaded}
	<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6">
		<div class="flex items-center gap-4 mb-4">
			<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">Claude Inbox</h2>
			{#if activeCount > 0}
				<span class="text-xs font-medium text-run-400 bg-run-700/30 px-2.5 py-0.5 rounded-full animate-pulse">
					{activeCount} active
				</span>
			{/if}
		</div>

		{#if visibleTasks.length === 0}
			<p class="text-sm text-bourbon-600">No new messages.</p>
		{:else}
		<div class="flex flex-col gap-1">
			{#each visibleTasks as task}
				<button
					class="group relative flex items-start gap-3 rounded-lg px-3 py-2.5 -mx-1 text-left transition-colors cursor-pointer
						{task.status === 'completed' || task.status === 'resolved' || task.status === 'refactoring' ? 'hover:bg-bourbon-800/50' : ''}"
					onclick={() => {
						if (task.status === 'resolved' && task.prUrl) {
							window.open(task.prUrl, '_blank');
						} else if (task.status === 'completed' || task.status === 'resolved' || task.status === 'refactoring') {
							viewResult(task);
						}
					}}
					disabled={task.status !== 'completed' && task.status !== 'resolved' && task.status !== 'refactoring'}
				>
					<!-- Status icon -->
					<div class="pt-0.5 shrink-0">
						{#if task.status === 'running' || task.status === 'pending'}
							<div class="w-3.5 h-3.5 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
						{:else if task.status === 'refactoring'}
							<span class="text-cmd-400 animate-pulse"><Wrench size={15} /></span>
						{:else if task.status === 'resolved'}
							<span class="text-green-400"><GitPullRequestArrow size={15} /></span>
						{:else if task.status === 'completed' && task.refactored}
							<span class="text-green-400"><Wrench size={15} /></span>
						{:else if task.status === 'completed'}
							<span class="text-green-400"><CheckCircle size={15} /></span>
						{:else}
							<span class="text-red-400"><XCircle size={15} /></span>
						{/if}
					</div>

					<!-- Content -->
					<div class="flex flex-col gap-1 min-w-0 flex-1">
						<!-- Row 1: Title -->
						<span class="text-bourbon-100 text-xs leading-snug truncate">
							{task.title || `${repoName(task.repoPath)}/${task.commitSha ? shortSha(task.commitSha) : ''}`}
						</span>
						<!-- Row 2: Type badge + repo + sha + time -->
						<div class="flex items-center gap-2 text-[10px]">
							<span class="font-mono text-cmd-400 bg-cmd-700/30 px-1.5 py-0.5 rounded-full">{task.type}</span>
							<span class="font-mono text-bourbon-500">{repoName(task.repoPath)}</span>
							{#if task.commitSha}
								<span class="font-mono text-bourbon-600">{shortSha(task.commitSha)}</span>
							{/if}
							<span class="text-bourbon-700 ml-auto">{timeAgo(task.createdAt)}</span>
						</div>
					</div>

					<!-- Overlay actions -->
					{#if task.status !== 'running' && task.status !== 'pending'}
						<div class="absolute right-0 top-0 bottom-0 flex items-center gap-1.5 pr-3 pl-10 opacity-0 group-hover:opacity-100 transition-opacity bg-linear-to-r from-transparent to-30% to-bourbon-800 rounded-r-lg">
							<span
								role="button"
								tabindex="0"
								onclick={(e) => { e.stopPropagation(); dismiss(task); }}
								onkeydown={(e) => { if (e.key === 'Enter') dismiss(task); }}
								class="btn-chiclet-danger !w-6 !h-6"
								title="Dismiss"
							>
								<X size={14} />
							</span>
						</div>
					{/if}
				</button>
			{/each}
		</div>

		{#if dismissableCount > 1}
			<div class="mt-3 pt-3 border-t border-bourbon-800">
				<button
					onclick={clearAll}
					class="text-[10px] font-mono text-bourbon-600 hover:text-bourbon-400 transition-colors cursor-pointer"
				>clear all completed</button>
			</div>
		{/if}
		{/if}
	</div>
{/if}
