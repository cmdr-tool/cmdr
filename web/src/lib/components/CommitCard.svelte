<script lang="ts">
	import { ChevronDown, ChevronRight, ExternalLink, FileText, FilePlus, FileMinus, FilePenLine, Maximize2, Flag, MessageSquarePlus, Download } from 'lucide-svelte';
	import {
		getCommitFiles,
		getCommitDiff,
		pullRepo,
		type GitCommit,
		type CommitFile
	} from '$lib/api';
	import { commits as commitsStore, markSeen, toggleFlag } from '$lib/commitStore';

	let {
		onopendiff
	}: {
		onopendiff: (commit: GitCommit, diff: string, files: string[]) => void;
	} = $props();

	let expandedCommit: string | null = $state(null);
	let filesCache = $state(new Map<string, CommitFile[]>());
	let filesLoading: string | null = $state(null);
	let showSeen = $state(false);

	let unseenCount = $derived($commitsStore.filter(c => !c.seen).length);
	let inReviewCount = $derived($commitsStore.filter(c => c.reviewCount > 0).length);
	let flaggedCount = $derived($commitsStore.filter(c => c.flagged).length);
	let seenCount = $derived($commitsStore.filter(c => c.seen).length);

	function groupByRepo(list: GitCommit[]): { name: string; path: string; commits: GitCommit[] }[] {
		const groups: { name: string; path: string; commits: GitCommit[] }[] = [];
		const seen = new Map<string, number>();
		for (const c of list) {
			const idx = seen.get(c.repoPath);
			if (idx !== undefined) {
				groups[idx].commits.push(c);
			} else {
				seen.set(c.repoPath, groups.length);
				groups.push({ name: c.repoName, path: c.repoPath, commits: [c] });
			}
		}
		return groups;
	}

	let unseenByRepo = $derived(groupByRepo($commitsStore.filter(c => !c.seen)));
	let inReviewByRepo = $derived(groupByRepo($commitsStore.filter(c => c.reviewCount > 0)));
	let flaggedByRepo = $derived(groupByRepo($commitsStore.filter(c => c.flagged)));
	let seenByRepo = $derived(groupByRepo($commitsStore.filter(c => c.seen)));

	let pullingRepo: string | null = $state(null);

	async function handlePullRepo(repoPath: string) {
		pullingRepo = repoPath;
		try {
			await pullRepo(repoPath);
		} catch { /* silent */ }
		pullingRepo = null;
	}

	async function toggleFiles(commit: GitCommit) {
		const key = `${commit.repoPath}:${commit.sha}`;
		if (expandedCommit === key) { expandedCommit = null; return; }
		expandedCommit = key;

		if (!filesCache.has(key)) {
			filesLoading = key;
			try {
				const files = await getCommitFiles(commit.repoPath, commit.sha);
				filesCache.set(key, files);
				filesCache = new Map(filesCache);
			} catch {
				filesCache.set(key, []);
				filesCache = new Map(filesCache);
			}
			filesLoading = null;
		}
	}

	async function openDiffModal(commit: GitCommit) {
		if (!commit.seen) {
			markSeen([commit.id]);
		}

		try {
			const result = await getCommitDiff(commit.repoPath, commit.sha);
			onopendiff(commit, result.diff, result.files || []);
		} catch {
			onopendiff(commit, '(failed to load diff)', []);
		}
	}

	async function markRepoSeen(repoPath: string) {
		const unseenIds = $commitsStore.filter(c => c.repoPath === repoPath && !c.seen).map(c => c.id);
		if (unseenIds.length === 0) return;
		await markSeen(unseenIds);
	}

	function handleToggleFlag(commit: GitCommit) {
		toggleFlag(commit.id);
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
		if (days < 7) return `${days}d ago`;
		return date.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
	}

	function shortSha(sha: string): string { return sha.slice(0, 7); }
	function firstLine(message: string): string { return message.split('\n')[0]; }

	const fileStatusIcon: Record<string, typeof FileText> = {
		added: FilePlus,
		modified: FilePenLine,
		removed: FileMinus,
		renamed: FileText
	};

	const fileStatusColor: Record<string, string> = {
		added: 'text-green-400',
		modified: 'text-run-400',
		removed: 'text-red-400',
		renamed: 'text-cmd-400'
	};
</script>

{#snippet commitList(commitItems: GitCommit[])}
	<div class="flex flex-col gap-1">
		{#each commitItems as commit}
			{@const key = `${commit.repoPath}:${commit.sha}`}
			{@const isExpanded = expandedCommit === key}
			{@const files = filesCache.get(key)}
			<div class="border border-bourbon-800 rounded-lg overflow-hidden {commit.seen ? 'bg-bourbon-950/20' : 'bg-bourbon-950/50 border-l-2 border-l-run-500'}">
				<button
					onclick={() => toggleFiles(commit)}
					class="w-full flex items-start gap-3 px-4 py-2.5 text-left hover:bg-bourbon-800/30 transition-colors cursor-pointer"
				>
					<span class="text-bourbon-600 shrink-0 mt-0.5">
						{#if isExpanded}
							<ChevronDown size={14} />
						{:else}
							<ChevronRight size={14} />
						{/if}
					</span>
					<span class="font-mono text-xs text-cmd-400 shrink-0 mt-0.5">{shortSha(commit.sha)}</span>
					{#if commit.flagged}<span class="text-run-400 shrink-0 mt-0.5"><Flag size={12} fill="currentColor" /></span>{/if}
					{#if commit.reviewCount > 0}<span class="text-cmd-400 shrink-0 mt-0.5"><MessageSquarePlus size={12} /></span>{/if}
					<div class="flex-1 flex flex-col gap-1 min-w-0">
						<div class="flex items-baseline gap-2 min-w-0">
							<span class="text-sm text-bourbon-200 truncate min-w-0 flex-1">{firstLine(commit.message)}</span>
							<span class="text-xs text-bourbon-700 shrink-0">{timeAgo(commit.committedAt)}</span>
						</div>
						{#if !isExpanded}
							<div class="flex items-center gap-3 text-[10px] font-mono text-bourbon-600">
								{#if commit.filesChanged > 0}
									<span>{commit.filesChanged} file{commit.filesChanged !== 1 ? 's' : ''}</span>
									<span class="text-green-600">+{commit.additions}</span>
									<span class="text-red-600">-{commit.deletions}</span>
								{/if}
								<span class="ml-auto">{commit.author}</span>
							</div>
						{/if}
					</div>
				</button>

				{#if isExpanded}
					<div class="border-t border-bourbon-800">
						<div class="px-4 py-2.5 flex flex-wrap items-center gap-4 text-xs text-bourbon-500 bg-bourbon-900/50">
							<span>{commit.author}</span>
							<span>{new Date(commit.committedAt).toLocaleString()}</span>
							{#if commit.url}
								<a
									href={commit.url}
									target="_blank"
									rel="noopener"
									class="flex items-center gap-1 text-cmd-400 hover:text-cmd-300"
									onclick={(e) => e.stopPropagation()}
								>
									<ExternalLink size={10} />
									GitHub
								</a>
							{/if}
							<button
								onclick={() => openDiffModal(commit)}
								class="flex items-center gap-1 text-cmd-400 hover:text-cmd-300 ml-auto cursor-pointer"
							>
								<Maximize2 size={10} />
								Review diff
							</button>
						</div>

						<div class="px-4 py-2">
							{#if filesLoading === key}
								<div class="flex items-center justify-center gap-2 py-3 text-bourbon-600">
									<div class="w-3 h-3 border-2 border-bourbon-700 border-t-cmd-500 rounded-full animate-spin"></div>
									<span class="text-xs">Loading files...</span>
								</div>
							{:else if files}
								<div class="flex flex-col gap-0.5">
									{#each files as file}
										{@const Icon = fileStatusIcon[file.status] || FileText}
										<div class="flex items-center gap-2 py-1 text-xs">
											<span class="{fileStatusColor[file.status] || 'text-bourbon-500'}">
												<Icon size={12} />
											</span>
											<span class="font-mono text-bourbon-300 truncate flex-1">{file.filename}</span>
											{#if file.additions > 0}
												<span class="text-green-400">+{file.additions}</span>
											{/if}
											{#if file.deletions > 0}
												<span class="text-red-400">-{file.deletions}</span>
											{/if}
										</div>
									{/each}
								</div>
							{/if}
						</div>
					</div>
				{/if}
			</div>
		{/each}
	</div>
{/snippet}

<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6">
	<div class="flex items-center gap-4 mb-4">
		<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">Recent Commits</h2>
		{#if unseenCount > 0}
			<span class="text-xs font-medium text-run-400 bg-run-700/30 px-2.5 py-0.5 rounded-full">
				{unseenCount} new
			</span>
		{/if}
	</div>

	{#if $commitsStore.length === 0}
		<p class="text-bourbon-600 text-sm">No commits yet. Add repos in <a href="/settings" class="text-cmd-400 hover:text-cmd-300">settings</a>.</p>
	{:else}
		<!-- Unseen commits -->
		{#if unseenCount > 0}
			<div class="flex flex-col gap-5">
				{#each unseenByRepo as group}
					<div class="break-inside-avoid">
						<div class="flex items-center justify-between mb-2">
							<h3 class="text-xs font-semibold text-bourbon-500">{group.name}</h3>
							<button
								onclick={() => markRepoSeen(group.path)}
								class="text-xs text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
							>
								mark {group.commits.length} seen
							</button>
						</div>
						{@render commitList(group.commits)}
					</div>
				{/each}
			</div>
		{:else}
			<p class="text-bourbon-600 text-sm">All caught up.</p>
		{/if}

		<!-- In review -->
		{#if inReviewCount > 0}
			<div class="mt-3 pt-3 border-t border-bourbon-800">
				<h3 class="flex items-center gap-2 text-xs font-semibold text-cmd-400 mb-3">
					<MessageSquarePlus size={12} />
					{inReviewCount} in review
				</h3>
				<div class="flex flex-col gap-5">
					{#each inReviewByRepo as group}
						<div class="break-inside-avoid">
							<h3 class="text-xs font-semibold text-bourbon-500 mb-2">{group.name}</h3>
							{@render commitList(group.commits)}
						</div>
					{/each}
				</div>
			</div>
		{/if}

		<!-- Flagged commits -->
		{#if flaggedCount > 0}
			<div class="mt-3 pt-3 border-t border-bourbon-800">
				<h3 class="flex items-center gap-2 text-xs font-semibold text-run-400 mb-3">
					<Flag size={12} fill="currentColor" />
					{flaggedCount} flagged
				</h3>
				<div class="flex flex-col gap-5">
					{#each flaggedByRepo as group}
						<div class="break-inside-avoid">
							<h3 class="text-xs font-semibold text-bourbon-500 mb-2">{group.name}</h3>
							{@render commitList(group.commits)}
						</div>
					{/each}
				</div>
			</div>
		{/if}

		<!-- Seen commits (collapsed) -->
		{#if seenCount > 0}
			<button
				onclick={() => showSeen = !showSeen}
				class="flex items-center gap-2 mt-3 pt-3 border-t border-bourbon-800 text-[10px] font-mono text-bourbon-600 hover:text-bourbon-400 transition-colors cursor-pointer w-full"
			>
				<span class="shrink-0">
					{#if showSeen}<ChevronDown size={12} />{:else}<ChevronRight size={12} />{/if}
				</span>
				{seenCount} reviewed commit{seenCount !== 1 ? 's' : ''}
			</button>
			{#if showSeen}
				<div class="flex flex-col gap-5 mt-3">
					{#each seenByRepo as group}
						{@const ahead = group.commits.filter(c => !c.local).length}
						<div class="break-inside-avoid">
							<div class="flex items-center mb-2">
								<h3 class="text-xs font-semibold text-bourbon-500">{group.name}</h3>
								{#if ahead > 0}<span class="ml-auto"></span>
									<button
										onclick={() => handlePullRepo(group.path)}
										disabled={pullingRepo === group.path}
										class="flex items-center gap-1 text-[9px] font-mono text-bourbon-600 hover:text-run-400 transition-colors cursor-pointer disabled:opacity-50"
									>
										{#if pullingRepo === group.path}
											<div class="w-2.5 h-2.5 border border-bourbon-600 border-t-run-400 rounded-full animate-spin"></div>
											syncing
										{:else}
											<Download size={10} />
											pull {ahead}
										{/if}
									</button>
								{/if}
							</div>
							{@render commitList(group.commits)}
						</div>
					{/each}
				</div>
			{/if}
		{/if}
	{/if}
</div>
