<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { ArrowRightLeft, Sparkles, X, ChevronDown, ChevronRight, ExternalLink, FileText, FilePlus, FileMinus, FileEdit, Maximize2, Focus } from 'lucide-svelte';
	import ActivityChart from '$lib/components/ActivityChart.svelte';
	import {
		killTmuxSession,
		focusTmuxSession,
		switchTmuxSession,
		openFolder,
		getCommits,
		getCommitFiles,
		getCommitDiff,
		markCommitsSeen,
		type TmuxSession,
		type ClaudeSession,
		type GitCommit,
		type CommitFile
	} from '$lib/api';
	import { events } from '$lib/events';

	let sessions: TmuxSession[] = $state([]);
	let claudeSessions: ClaudeSession[] = $state([]);
	let commits: GitCommit[] = $state([]);
	let error: string | null = $state(null);
	let sseConnected = $state(false);
	let sessionsLoaded = $state(false);
	let commitsLoaded = $state(false);

	const now = new Date();
	const hour = now.getHours();
	const greeting = hour < 12 ? 'good morning' : hour < 17 ? 'good afternoon' : 'good evening';
	const dateStr = now.toLocaleDateString('en-US', {
		weekday: 'long',
		month: 'long',
		day: 'numeric'
	});

	onMount(async () => {
		try {
			commits = await getCommits();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to connect to daemon';
		}
		commitsLoaded = true;
	});

	const unsubTmux = events.on('tmux:sessions', (data) => {
		sessions = data;
		sseConnected = true;
		sessionsLoaded = true;
	});

	const unsubClaude = events.on('claude:sessions', (data) => {
		claudeSessions = data;
		sseConnected = true;
	});

	onDestroy(() => {
		unsubTmux();
		unsubClaude();
	});

	// --- Session kill ---
	let holdingKill: string | null = $state(null);
	let holdProgress: number = $state(0);
	let holdRaf: number | null = null;
	let holdStart: number = 0;
	let killedSession: string | null = $state(null);
	const HOLD_DURATION = 800;

	function startHoldKill(name: string) {
		holdingKill = name;
		holdProgress = 0;
		holdStart = 0;
		function tick(timestamp: number) {
			if (!holdStart) holdStart = timestamp;
			holdProgress = Math.min((timestamp - holdStart) / HOLD_DURATION, 1);
			if (holdProgress >= 1) { completeKill(name); return; }
			holdRaf = requestAnimationFrame(tick);
		}
		holdRaf = requestAnimationFrame(tick);
	}

	function cancelHoldKill() {
		if (holdRaf) cancelAnimationFrame(holdRaf);
		holdRaf = null;
		holdingKill = null;
		holdProgress = 0;
	}

	async function completeKill(name: string) {
		if (holdRaf) cancelAnimationFrame(holdRaf);
		holdRaf = null;
		holdingKill = null;
		holdProgress = 0;
		killedSession = name;
		await killTmuxSession(name);
		setTimeout(() => {
			sessions = sessions.filter((s) => s.name !== name);
			killedSession = null;
		}, 3000);
	}


	function shortenPath(path: string): string {
		return path.replace(/^\/Users\/[^/]+/, '~');
	}

	function truncatePath(path: string, maxLen = 25): string {
		const short = shortenPath(path);
		if (short.length <= maxLen) return short;
		const parts = short.split('/');
		// Keep last 2 segments with ellipsis prefix
		if (parts.length > 2) return '…/' + parts.slice(-2).join('/');
		return '…' + short.slice(-maxLen + 1);
	}

	// Map Claude sessions by their tmux pane target (e.g. "stasher:1.3")
	let claudeByTarget = $derived(
		new Map(claudeSessions.filter((c) => c.tmuxTarget).map((c) => [c.tmuxTarget, c]))
	);

	// Best Claude status per tmux session (for session-level badge)
	const statusRank: Record<string, number> = { working: 3, waiting: 2, idle: 1, unknown: 0 };
	let claudeBySession = $derived(() => {
		const map = new Map<string, ClaudeSession>();
		for (const c of claudeSessions) {
			if (!c.tmuxTarget) continue;
			const sessName = c.tmuxTarget.split(':')[0];
			const existing = map.get(sessName);
			if (!existing || (statusRank[c.status] ?? 0) > (statusRank[existing.status] ?? 0)) {
				map.set(sessName, c);
			}
		}
		return map;
	});

	let unmatchedClaude = $derived(
		claudeSessions.filter((c) => !c.tmuxTarget)
	);

	function paneTarget(sessionName: string, winIdx: number, paneIdx: number): string {
		return `${sessionName}:${winIdx}.${paneIdx}`;
	}

	// --- Commits ---
	let expandedCommit: string | null = $state(null);
	let filesCache = $state(new Map<string, CommitFile[]>());
	let filesLoading: string | null = $state(null);
	let unseenCount = $derived(commits.filter(c => !c.seen).length);

	// Group commits by repo, preserving order of first appearance
	let commitsByRepo = $derived(() => {
		const groups: { name: string; path: string; commits: GitCommit[] }[] = [];
		const seen = new Map<string, number>();
		for (const c of commits) {
			const idx = seen.get(c.repoPath);
			if (idx !== undefined) {
				groups[idx].commits.push(c);
			} else {
				seen.set(c.repoPath, groups.length);
				groups.push({ name: c.repoName, path: c.repoPath, commits: [c] });
			}
		}
		return groups;
	});

	// Diff modal
	let modalCommit: GitCommit | null = $state(null);
	let modalDiff: string | null = $state(null);
	let modalFormat: 'delta' | 'unified' = $state('unified');
	let modalFiles: string[] = $state([]);
	let modalLoading = $state(false);

	async function toggleFiles(commit: GitCommit) {
		const key = `${commit.repoPath}:${commit.sha}`;
		if (expandedCommit === key) { expandedCommit = null; return; }
		expandedCommit = key;

		// Mark as seen when expanded
		if (!commit.seen) {
			markCommitsSeen([commit.id]);
			commits = commits.map(c => c.id === commit.id ? { ...c, seen: true } : c);
		}

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
		modalCommit = commit;
		modalDiff = null;
		modalFormat = 'unified';
		modalFiles = [];
		modalLoading = true;
		try {
			const result = await getCommitDiff(commit.repoPath, commit.sha);
			modalDiff = result.diff;
			modalFormat = result.format;
			modalFiles = result.files || [];
		} catch {
			modalDiff = '(failed to load diff)';
			modalFormat = 'unified';
			modalFiles = [];
		}
		modalLoading = false;
	}

	function closeDiffModal() {
		modalCommit = null;
		modalDiff = null;
	}

	async function markRepoSeen(repoFullName: string) {
		const unseenIds = commits.filter(c => c.repoPath === repoFullName && !c.seen).map(c => c.id);
		if (unseenIds.length === 0) return;
		await markCommitsSeen(unseenIds);
		commits = commits.map(c => unseenIds.includes(c.id) ? { ...c, seen: true } : c);
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
		modified: FileEdit,
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

<!-- Greeting -->
<div class="mb-6">
	<h1 class="font-display text-3xl font-bold text-bourbon-100 lowercase">{greeting}, mike</h1>
	<p class="text-bourbon-600 mt-1">
		{dateStr}
		&middot; {sessions.length} session{sessions.length !== 1 ? 's' : ''}
		&middot; {claudeSessions.length} claude instance{claudeSessions.length !== 1 ? 's' : ''}
		{#if unseenCount > 0}
			&middot; {unseenCount} unseen commit{unseenCount !== 1 ? 's' : ''}
		{/if}
	</p>
</div>

{#if !sseConnected}
	<div class="flex items-center justify-center gap-3 text-bourbon-600 py-12">
		<div class="w-4 h-4 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
		<span class="font-display text-xs uppercase tracking-widest">Loading</span>
	</div>
{:else}

<div class="columns-1 lg:columns-2 gap-4 [&>*]:mb-4 [&>*]:break-inside-avoid">

	<!-- Sessions -->
	<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6">
		<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500 mb-4">Sessions</h2>

		<!-- Activity chart -->
		<div class="mb-4">
			<ActivityChart />
		</div>

		{#if !sessionsLoaded}
			<div class="flex items-center gap-2 text-bourbon-600 py-4">
				<div class="w-3 h-3 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
				<span class="text-[10px] font-mono">loading sessions</span>
			</div>
		{:else if sessions.length === 0}
			<p class="text-bourbon-600 text-sm">No tmux sessions running.</p>
		{:else}
			<div class="flex flex-col gap-1.5">
				{#each sessions as session}
					{@const claude = claudeBySession().get(session.name)}
					{#if killedSession === session.name}
						<div class="flex items-center justify-center border border-red-900/30 rounded-lg px-5 py-3.5 text-red-400
							animate-fade-out">
							<span class="font-display text-xs font-bold uppercase tracking-widest">killed {session.name}</span>
						</div>
					{:else}
					<div class="group relative overflow-hidden {session.attached ? 'bg-bourbon-800/40' : 'bg-bourbon-950/30'} border border-bourbon-800 rounded-lg px-5 py-3.5">
						<div class="min-w-0">
							<div class="flex items-center gap-2 mb-2">
								<div
									class="w-2 h-2 rounded-full {session.attached
										? 'bg-run-500'
										: 'bg-bourbon-700'}"
								></div>
								<span class="font-semibold text-bourbon-100">{session.name}</span>
								<span class="text-xs text-bourbon-600">{session.windows.length} window{session.windows.length !== 1 ? 's' : ''}</span>
								{#if session.attached}
									<span class="text-xs font-medium text-run-500 bg-run-700/30 px-2.5 py-0.5 rounded-full">attached</span>
								{/if}
								{#if claude}
									{@const statusStyle = {
										working: 'text-green-400 bg-green-900/30',
										waiting: 'text-run-400 bg-run-700/30 animate-pulse',
										idle: 'text-bourbon-500 bg-bourbon-800/30',
										unknown: 'text-cmd-400 bg-cmd-700/30 animate-pulse'
									}[claude.status]}
									{@const statusLabel = {
										working: 'claude · working',
										waiting: 'claude · waiting',
										idle: `claude · idle · ${claude.uptime}`,
										unknown: `claude · ? · ${claude.uptime}`
									}[claude.status]}
									<span class="flex items-center gap-1 text-xs font-medium px-2.5 py-0.5 rounded-full {statusStyle}">
										<Sparkles size={10} />
										{statusLabel}
									</span>
								{/if}
							</div>
							<div class="flex flex-col gap-1 ml-4">
								{#each session.windows as window}
									{#each window.panes as pane}
										{@const paneClause = claudeByTarget.get(paneTarget(session.name, window.index, pane.index))}
										<div class="flex items-center gap-3 text-sm min-w-0">
											<span class="font-mono text-xs shrink-0 {pane.active ? 'text-run-600' : 'text-bourbon-600'}">{pane.command}</span>
											{#if paneClause}
												{@const st = paneClause.status}
												<span class="inline-flex items-center gap-1 text-[10px] font-mono px-1.5 py-0.5 rounded whitespace-nowrap shrink-0
													{st === 'working' ? 'text-green-400 bg-green-900/30' :
													 st === 'waiting' ? 'text-run-400 bg-run-700/30 animate-pulse' :
													 st === 'idle' ? 'text-bourbon-500 bg-bourbon-800/30' :
													 'text-cmd-400 bg-cmd-700/30 animate-pulse'}">
													<span class="w-1.5 h-1.5 rounded-full
														{st === 'working' ? 'bg-green-500' :
														 st === 'waiting' ? 'bg-run-500' :
														 st === 'idle' ? 'bg-bourbon-600' :
														 'bg-cmd-500'}"></span>
													{st === 'idle' ? `idle · ${paneClause.uptime}` : st === 'unknown' ? `? · ${paneClause.uptime}` : st}
												</span>
											{/if}
											<button
											onclick={(e) => { e.stopPropagation(); openFolder(pane.cwd); }}
											class="text-bourbon-500 font-mono text-xs hover:text-cmd-400 transition-colors cursor-pointer text-left"
										>{shortenPath(pane.cwd)}</button>
										</div>
									{/each}
								{/each}
							</div>
						</div>
						<div class="absolute right-0 top-0 bottom-0 flex items-center gap-1.5 pr-4 pl-10 opacity-0 group-hover:opacity-100 transition-opacity bg-linear-to-r from-transparent to-bourbon-900 to-30%">
							{#if session.attached}
								<button
									onclick={() => focusTmuxSession(session.name)}
									class="btn-chiclet-alt"
								>
									<Focus size={14} />
								</button>
							{:else}
								<button
									onclick={() => {
										switchTmuxSession(session.name);
										sessions = sessions.map(s => ({ ...s, attached: s.name === session.name }));
									}}
									class="btn-chiclet"
								>
									<ArrowRightLeft size={14} />
								</button>
							{/if}
							<button
								onmousedown={() => startHoldKill(session.name)}
								onmouseup={cancelHoldKill}
								onmouseleave={cancelHoldKill}
								class="btn-chiclet-danger relative overflow-hidden"
							>
								{#if holdingKill === session.name}
									<div
										class="absolute inset-x-0 bottom-0 bg-red-500/40 transition-none"
										style="height: {holdProgress * 100}%"
									></div>
								{/if}
								<X size={14} class="relative z-10" />
							</button>
						</div>
					</div>
					{/if}
				{/each}
			</div>
		{/if}
	</div>

	<!-- Recent Commits -->
	<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6">
		<div class="flex items-center gap-4 mb-4">
			<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">Recent Commits</h2>
			{#if unseenCount > 0}
				<span class="text-xs font-medium text-run-400 bg-run-700/30 px-2.5 py-0.5 rounded-full">
					{unseenCount} new
				</span>
			{/if}
		</div>

		{#if !commitsLoaded}
			<div class="flex items-center gap-2 text-bourbon-600 py-4">
				<div class="w-3 h-3 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
				<span class="text-[10px] font-mono">loading commits</span>
			</div>
		{:else if commits.length === 0}
			<p class="text-bourbon-600 text-sm">No commits yet. Add repos in <a href="/settings" class="text-cmd-400 hover:text-cmd-300">settings</a>.</p>
		{:else}
			<div class="flex flex-col gap-5">
				{#each commitsByRepo() as group}
					{@const repoUnseen = group.commits.filter(c => !c.seen).length}
					<div class="break-inside-avoid">
						<div class="flex items-center justify-between mb-2">
							<h3 class="text-xs font-semibold text-bourbon-500">{group.name}</h3>
							{#if repoUnseen > 0}
								<button
									onclick={() => markRepoSeen(group.path)}
									class="text-xs text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
								>
									mark {repoUnseen} seen
								</button>
							{/if}
						</div>
						<div class="flex flex-col gap-1">
							{#each group.commits as commit}
								{@const key = `${commit.repoPath}:${commit.sha}`}
								{@const isExpanded = expandedCommit === key}
								{@const files = filesCache.get(key)}
								<div class="border border-bourbon-800 rounded-lg overflow-hidden {commit.seen ? 'bg-bourbon-950/20' : 'bg-bourbon-950/50 border-l-2 border-l-run-500'}">
									<button
										onclick={() => toggleFiles(commit)}
										class="w-full flex items-center gap-3 px-4 py-2.5 text-left hover:bg-bourbon-800/30 transition-colors cursor-pointer"
									>
										<span class="text-bourbon-600 shrink-0">
											{#if isExpanded}
												<ChevronDown size={14} />
											{:else}
												<ChevronRight size={14} />
											{/if}
										</span>
										<span class="font-mono text-xs text-cmd-400 shrink-0">{shortSha(commit.sha)}</span>
										<span class="text-sm text-bourbon-200 truncate flex-1">{firstLine(commit.message)}</span>
										<span class="text-xs text-bourbon-700 shrink-0">{timeAgo(commit.committedAt)}</span>
									</button>

									{#if isExpanded}
										<div class="border-t border-bourbon-800">
											<!-- Commit meta -->
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
													View full diff
												</button>
											</div>

											<!-- File list -->
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
					</div>
				{/each}
			</div>
		{/if}

		{#if unmatchedClaude.length > 0}
			<h3 class="text-xs font-semibold text-bourbon-500 mt-6 mb-2">Orphaned Claude Instances</h3>
			<div class="flex flex-col gap-1.5">
				{#each unmatchedClaude as instance}
					<div class="flex items-center gap-3 bg-bourbon-950/30 border border-bourbon-800 rounded-lg px-5 py-3.5">
						<span class="text-cmd-400"><Sparkles size={14} /></span>
						<span class="font-semibold text-bourbon-100">{instance.project}</span>
						<span class="text-xs text-bourbon-600 font-mono">{shortenPath(instance.cwd)}</span>
						<span class="text-xs text-bourbon-600">&middot; {instance.uptime}</span>
						<span class="text-xs text-bourbon-600">&middot; pid {instance.pid}</span>
					</div>
				{/each}
			</div>
		{/if}
	</div>

</div>

<!-- Note -->
{#if error}
	<div class="border-l-2 border-run-500 bg-bourbon-900 rounded-r-lg px-5 py-4 mt-4">
		<h3 class="font-display text-xs font-bold uppercase tracking-widest text-run-500 mb-2">Note</h3>
		<p class="text-bourbon-400">{error}</p>
	</div>
{/if}

{/if}

<!-- Diff Modal -->
{#if modalCommit}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<div
		class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
		onclick={closeDiffModal}
		onkeydown={(e) => { if (e.key === 'Escape') closeDiffModal(); }}
		role="dialog"
		tabindex="-1"
	>
		<div
			class="bg-bourbon-900 border border-bourbon-800 rounded-2xl w-[90vw] max-w-5xl max-h-[85vh] flex flex-col overflow-hidden"
			onclick={(e) => e.stopPropagation()}
		>
			<!-- Modal header -->
			<div class="flex items-center justify-between px-6 py-4 border-b border-bourbon-800 shrink-0">
				<div class="flex items-center gap-3 min-w-0">
					<span class="font-mono text-sm text-cmd-400">{shortSha(modalCommit.sha)}</span>
					<span class="text-bourbon-200 truncate">{firstLine(modalCommit.message)}</span>
				</div>
				<div class="flex items-center gap-3 shrink-0">
					<span class="text-xs text-bourbon-500">{modalCommit.author} &middot; {modalCommit.repoName}</span>
					{#if modalCommit.url}
						<a
							href={modalCommit.url}
							target="_blank"
							rel="noopener"
							class="flex items-center gap-1 text-xs text-cmd-400 hover:text-cmd-300"
						>
							<ExternalLink size={10} />
							GitHub
						</a>
					{/if}
					<button
						onclick={closeDiffModal}
						class="text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
					>
						<X size={18} />
					</button>
				</div>
			</div>

			<!-- File jump -->
			{#if modalFiles.length > 1}
				<div class="flex items-center gap-2 px-6 py-2.5 border-b border-bourbon-800 shrink-0 bg-bourbon-950/50">
					<span class="text-xs text-bourbon-600">{modalFiles.length} files</span>
					<select
						onchange={(e) => {
							const idx = (e.target as HTMLSelectElement).value;
							if (idx !== '') {
								document.getElementById(`file-${idx}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
								(e.target as HTMLSelectElement).value = '';
							}
						}}
						class="bg-bourbon-900 border border-bourbon-700 rounded-md px-2 py-1 text-xs font-mono text-bourbon-300
							focus:outline-none focus:border-cmd-500 cursor-pointer"
					>
						<option value="">Jump to file...</option>
						{#each modalFiles as file, i}
							<option value={i}>{file}</option>
						{/each}
					</select>
				</div>
			{/if}

			<!-- Modal body -->
			<div class="overflow-auto flex-1" id="diff-body">
				{#if modalLoading}
					<div class="flex items-center justify-center gap-2 py-12 text-bourbon-600">
						<div class="w-4 h-4 border-2 border-bourbon-700 border-t-cmd-500 rounded-full animate-spin"></div>
						<span class="text-sm">Loading diff...</span>
					</div>
				{:else if modalDiff}
					{#if modalFormat === 'delta'}
						<pre class="text-xs leading-relaxed font-mono p-6 bg-bourbon-950 text-bourbon-400 min-w-fit">{@html modalDiff}</pre>
					{:else}
						<pre class="text-xs leading-relaxed font-mono p-6 bg-bourbon-950 min-w-fit">{#each modalDiff.split('\n') as line}<span class="{line.startsWith('+') && !line.startsWith('+++') ? 'text-green-400 bg-green-950/30' : line.startsWith('-') && !line.startsWith('---') ? 'text-red-400 bg-red-950/30' : line.startsWith('@@') ? 'text-cmd-400' : line.startsWith('diff ') ? 'text-bourbon-500 font-bold' : 'text-bourbon-500'}">{line}</span>
{/each}</pre>
					{/if}
				{/if}
			</div>
		</div>
	</div>
{/if}
