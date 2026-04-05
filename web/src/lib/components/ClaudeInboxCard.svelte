<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { CheckCircle, XCircle, Eye, X } from 'lucide-svelte';
	import { getClaudeTasks, getClaudeTaskResult, dismissClaudeTask, dismissAllClaudeTasks, type ClaudeTask } from '$lib/api';
	import { events } from '$lib/events';

	let {
		onviewresult
	}: {
		onviewresult: (result: string) => void;
	} = $props();

	let tasks = $state<ClaudeTask[]>([]);
	let loaded = $state(false);
	let unsub: (() => void) | null = null;
	let pendingCount = $derived(tasks.filter(t => t.status === 'running' || t.status === 'pending').length);
	let completedCount = $derived(tasks.filter(t => t.status === 'completed' || t.status === 'failed').length);

	onMount(async () => {
		try { tasks = await getClaudeTasks(); } catch { /* silent */ }
		loaded = true;

		unsub = events.on('claude:task', (evt) => {
			const idx = tasks.findIndex(t => t.id === evt.id);
			if (idx >= 0) {
				tasks[idx] = { ...tasks[idx], status: evt.status as ClaudeTask['status'] };
				tasks = [...tasks];
			} else {
				getClaudeTasks().then(t => { tasks = t; }).catch(() => {});
			}
		});
	});

	onDestroy(() => {
		if (unsub) unsub();
	});

	async function viewResult(task: ClaudeTask) {
		try {
			const { result } = await getClaudeTaskResult(task.id);
			onviewresult(result);
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

{#if loaded && tasks.length > 0}
	<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6">
		<div class="flex items-center gap-4 mb-4">
			<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">Claude Inbox</h2>
			{#if pendingCount > 0}
				<span class="text-xs font-medium text-run-400 bg-run-700/30 px-2.5 py-0.5 rounded-full animate-pulse">
					{pendingCount} running
				</span>
			{/if}
		</div>

		<div class="flex flex-col gap-1">
			{#each tasks as task}
				<div class="group flex items-center gap-3 text-sm py-1.5">
					<!-- Status icon -->
					{#if task.status === 'running' || task.status === 'pending'}
						<div class="w-3 h-3 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin shrink-0"></div>
					{:else if task.status === 'completed'}
						<span class="text-green-400 shrink-0"><CheckCircle size={14} /></span>
					{:else}
						<span class="text-red-400 shrink-0"><XCircle size={14} /></span>
					{/if}

					<!-- Type badge -->
					<span class="text-[10px] font-mono text-bourbon-500 bg-bourbon-800/50 px-1.5 py-0.5 rounded shrink-0">{task.type}</span>

					<!-- Metadata -->
					<span class="text-bourbon-100 font-mono text-xs">{repoName(task.repoPath)}</span>
					{#if task.commitSha}
						<span class="text-cmd-400 font-mono text-[10px]">{shortSha(task.commitSha)}</span>
					{/if}

					<span class="text-bourbon-700 text-[10px] ml-auto shrink-0">{timeAgo(task.createdAt)}</span>

					<!-- Actions -->
					<div class="flex items-center gap-2 shrink-0 opacity-0 group-hover:opacity-100 transition-opacity">
						{#if task.status === 'completed'}
							<button
								onclick={() => viewResult(task)}
								class="text-cmd-400 hover:text-cmd-300 transition-colors cursor-pointer"
								title="View result"
							>
								<Eye size={14} />
							</button>
						{/if}
						{#if task.status !== 'running' && task.status !== 'pending'}
							<button
								onclick={() => dismiss(task)}
								class="text-bourbon-700 hover:text-red-400 transition-colors cursor-pointer"
								title="Dismiss"
							>
								<X size={14} />
							</button>
						{/if}
					</div>
				</div>
			{/each}
		</div>

		{#if completedCount > 1}
			<div class="mt-3 pt-3 border-t border-bourbon-800">
				<button
					onclick={clearAll}
					class="text-[10px] font-mono text-bourbon-600 hover:text-bourbon-400 transition-colors cursor-pointer"
				>clear all completed</button>
			</div>
		{/if}
	</div>
{/if}
