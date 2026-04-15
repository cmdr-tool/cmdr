<script lang="ts">
	import { CircleCheck, CircleX, GitPullRequestArrow, X, Pencil, Plus, CircleQuestionMark, Users, Square } from 'lucide-svelte';
	import { getClaudeTaskResult, cancelTask, type ClaudeTask } from '$lib/api';
	import {
		loaded as loadedStore,
		visibleTasks as visibleTasksStore,
		activeCount as activeCountStore,
		dismissableCount as dismissableCountStore,
		dismiss as dismissTask,
		clearAllCompleted
	} from '$lib/taskStore';
	import { delegationSummaries } from '$lib/delegationStore';
	import { timeAgo } from '$lib/timeStore';

	let {
		onviewresult,
		ondraft,
		onask,
		onopenmissions
	}: {
		onviewresult: (task: ClaudeTask, result: string) => void;
		ondraft: (taskId?: number, repoPath?: string) => void;
		onask: (task: ClaudeTask) => void;
		onopenmissions: (squad: string) => void;
	} = $props();

	async function viewResult(task: ClaudeTask) {
		try {
			const { result } = await getClaudeTaskResult(task.id);
			onviewresult(task, result);
		} catch { /* silent */ }
	}

	function shortSha(sha: string): string { return sha.slice(0, 7); }

	function repoName(path: string): string {
		return path.split('/').pop() ?? path;
	}

	function fallbackTitle(task: ClaudeTask): string {
		const parts: string[] = [];
		if (task.intent) parts.push(task.intent.replaceAll('-', ' '));
		if (task.repoPath) parts.push(repoName(task.repoPath));
		if (task.commitSha) parts.push(shortSha(task.commitSha));
		return parts.join(': ') || 'Untitled';
	}

	function parsePrUrl(url: string): string {
		const m = url.match(/github\.com\/([^/]+)\/([^/]+)\/pull\/(\d+)/);
		if (m) return `${m[2]}#${m[3]}`;
		return url.length > 30 ? url.slice(0, 27) + '...' : url;
	}

	function badgeColor(type: string): string {
		switch (type) {
			case 'review': return 'text-teal-400 bg-teal-700/30';
			case 'directive': return 'text-blue-400 bg-blue-700/30';
			case 'ask': return 'text-cmd-400 bg-cmd-700/30';
			default: return 'text-bourbon-400 bg-bourbon-700/30';
		}
	}
</script>

{#if $loadedStore}
	<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6">
		<div class="flex items-center gap-4 mb-4">
			<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">Claude Inbox</h2>
			{#if $activeCountStore > 0}
				<span class="text-xs font-medium text-run-400 bg-run-700/30 px-2.5 py-0.5 rounded-full animate-pulse">
					{$activeCountStore} active
				</span>
			{/if}
		</div>

		{#if $visibleTasksStore.length === 0 && $delegationSummaries.length === 0}
			<p class="text-sm text-bourbon-600">No new messages.</p>
		{:else}
		<div class="flex flex-col gap-2">
			{#each $delegationSummaries as summary}
				<button
					class="group relative flex items-start gap-3 border-l-2 border-l-run-500 rounded-lg px-3 py-2.5 -mx-1 text-left transition-colors border bg-bourbon-950/30 bg-lattice hover:bg-bourbon-800/50 cursor-pointer
						{summary.activeCount > 0 ? 'border-shine' : 'border-bourbon-800'}"
					onclick={() => onopenmissions(summary.squad)}
				>
					<div class="pt-0.5 shrink-0">
						<Users size={15} class="text-run-500/60" />
					</div>
					<div class="flex flex-col gap-1 min-w-0 flex-1">
						<div class="flex items-center gap-2">
							<span class="font-display text-[10px] font-bold uppercase tracking-widest text-run-500">{summary.squad}</span>
							{#if summary.activeCount > 0}
								<span class="text-[10px] font-mono text-run-400 animate-pulse">{summary.activeCount} active</span>
								<span class="text-bourbon-700">·</span>
							{/if}
							<span class="text-[10px] text-bourbon-600">{summary.totalCount - summary.activeCount} completed</span>
						</div>
						<div class="flex items-center gap-2 text-[10px]">
							<span class="font-mono text-bourbon-600">{summary.members.join(' · ')}</span>
							<span class="text-bourbon-700 ml-auto">{$timeAgo(summary.latestAt)}</span>
						</div>
					</div>
				</button>
			{/each}
			{#each $visibleTasksStore as task}
				<!-- Using div instead of button+disabled so the dismiss overlay always receives click events -->
			<div
				role="button"
				tabindex="0"
				class="group relative flex items-start gap-3 rounded-lg px-3 py-2.5 -mx-1 text-left transition-colors hover:bg-bourbon-800/50
					{task.type === 'ask' || task.status === 'draft' || (task.status === 'completed' && (task.type === 'review' || task.intent === 'new-feature' || task.intent === 'analysis' || task.prUrl)) ? 'cursor-pointer' : ''}"
				onclick={() => {
					if (task.type === 'ask') {
						onask(task);
					} else if (task.intent === 'analysis' && (task.status === 'completed' || task.status === 'running')) {
						onask(task);
					} else if (task.status === 'draft') {
						ondraft(task.id, task.repoPath);
					} else if (task.status === 'completed' && task.prUrl) {
						window.open(task.prUrl, '_blank');
					} else if (task.status === 'completed' && (task.type === 'review' || task.intent === 'new-feature')) {
						viewResult(task);
					}
				}}
				onkeydown={(e) => { if (e.key === 'Enter') e.currentTarget.click(); }}
			>
					<!-- Status icon -->
					<div class="pt-0.5 shrink-0">
						{#if task.type === 'ask' && task.status === 'completed'}
							<span class="text-cmd-400"><CircleQuestionMark size={15} /></span>
						{:else if task.status === 'draft'}
							<span class="text-cmd-400"><Pencil size={15} /></span>
						{:else if task.type === 'ask' && task.status === 'running'}
							<div class="w-3.5 h-3.5 border-2 border-bourbon-700 border-t-cmd-500 rounded-full animate-spin"></div>
						{:else if task.status === 'running' || task.status === 'pending'}
							<div class="w-3.5 h-3.5 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
						{:else if task.status === 'completed' && task.prUrl}
							<span class="text-green-400"><GitPullRequestArrow size={15} /></span>
						{:else if task.status === 'completed'}
							<span class="text-green-400"><CircleCheck size={15} /></span>
						{:else}
							<span class="text-red-400"><CircleX size={15} /></span>
						{/if}
					</div>

					<!-- Content -->
					<div class="flex flex-col gap-1 min-w-0 flex-1">
						<!-- Row 1: Title + type badge -->
						<div class="flex items-center gap-2">
							<span class="text-xs leading-snug truncate min-w-0 flex-1
								{task.status === 'failed' ? 'text-bourbon-400 line-through' : task.status === 'completed' ? 'text-bourbon-300' : 'text-bourbon-100'}">
								{task.title || fallbackTitle(task)}
							</span>
							<span class="font-mono text-[10px] px-1.5 py-0.5 rounded-full shrink-0 {badgeColor(task.type)}">{task.type}</span>
						</div>
						<!-- Row 2: Contextual metadata + timestamp -->
						<div class="flex items-center gap-2 text-[10px]">
							{#if task.status === 'failed' && task.errorMsg}
								<span class="font-mono text-red-400/70 truncate">{task.errorMsg}</span>
							{:else if task.type === 'review'}
								{#if task.repoPath}<span class="font-mono text-bourbon-500">{repoName(task.repoPath)}</span>{/if}
								{#if task.repoPath && task.commitSha}<span class="text-bourbon-700">·</span>{/if}
								{#if task.commitSha}<span class="font-mono text-bourbon-600">{shortSha(task.commitSha)}</span>{/if}
							{:else if task.type === 'directive'}
								{#if task.prUrl}
									{#if task.repoPath}<span class="font-mono text-bourbon-500">{repoName(task.repoPath)}</span>{/if}
									{#if task.repoPath}<span class="text-bourbon-700">·</span>{/if}
									<span class="font-mono text-cmd-400">{parsePrUrl(task.prUrl)}</span>
								{:else}
									{#if task.intent}<span class="font-mono text-bourbon-500">{task.intent.replaceAll('-', ' ')}</span>{/if}
									{#if task.intent && task.repoPath}<span class="text-bourbon-700">·</span>{/if}
									{#if task.repoPath}<span class="font-mono text-bourbon-500">{repoName(task.repoPath)}</span>{/if}
								{/if}
							{:else}
								{#if task.repoPath}<span class="font-mono text-bourbon-500">{repoName(task.repoPath)}</span>{/if}
							{/if}
							<span class="text-bourbon-700 ml-auto">{$timeAgo(task.createdAt)}</span>
						</div>
					</div>

					<!-- Overlay actions -->
					{#if (task.type === 'directive' || task.type === 'ask') && task.status === 'running'}
						<div class="absolute right-0 top-0 bottom-0 flex items-center gap-1.5 pr-3 pl-20 invisible group-hover:visible bg-linear-to-r from-transparent to-30% to-bourbon-800 rounded-r-lg">
							<span
								role="button"
								tabindex="0"
								onclick={(e) => { e.stopPropagation(); cancelTask(task.id); }}
								onkeydown={(e) => { if (e.key === 'Enter') cancelTask(task.id); }}
								class="btn-chiclet-sm btn-chiclet-danger"
								title={task.type === 'directive' ? 'Cancel and return to draft' : 'Cancel'}
							>
								<Square size={12} />
							</span>
						</div>
					{:else if task.status !== 'running' && task.status !== 'pending'}
						<div class="absolute right-0 top-0 bottom-0 flex items-center gap-1.5 pr-3 pl-20 invisible group-hover:visible bg-linear-to-r from-transparent to-30% to-bourbon-800 rounded-r-lg">
							<span
								role="button"
								tabindex="0"
								onclick={(e) => { e.stopPropagation(); dismissTask(task.id); }}
								onkeydown={(e) => { if (e.key === 'Enter') dismissTask(task.id); }}
								class="btn-chiclet-sm btn-chiclet-danger"
								title="Dismiss"
							>
								<X size={14} />
							</span>
						</div>
					{/if}
				</div>
			{/each}
		</div>

		{/if}

		<div class="mt-3 pt-3 border-t border-bourbon-800 flex items-center">
			<button
				onclick={() => ondraft()}
				class="flex items-center gap-1.5 text-[10px] font-mono text-bourbon-600 hover:text-cmd-400 transition-colors cursor-pointer"
			>
				<Plus size={12} />
				new directive
			</button>
			{#if $dismissableCountStore > 0}
				<button
					onclick={clearAllCompleted}
					class="ml-auto text-[10px] font-mono text-bourbon-600 hover:text-bourbon-400 transition-colors cursor-pointer"
				>clear done</button>
			{/if}
		</div>
	</div>
{/if}
