<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { getCommits, toggleCommitFlag, type TmuxSession, type ClaudeSession, type GitCommit, type ClaudeTask } from '$lib/api';
	import { events, connection } from '$lib/events';

	import BrewCard from '$lib/components/BrewCard.svelte';
	import SessionCard from '$lib/components/SessionCard.svelte';
	import CommitCard from '$lib/components/CommitCard.svelte';
	import ClaudeInboxCard from '$lib/components/ClaudeInboxCard.svelte';
	import DiffModal from '$lib/components/DiffModal.svelte';
	import ReviewResultModal from '$lib/components/ReviewResultModal.svelte';

	let sessions: TmuxSession[] = $state([]);
	let claudeSessions: ClaudeSession[] = $state([]);
	let commits: GitCommit[] = $state([]);
	let error: string | null = $state(null);
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

	let unseenCount = $derived(commits.filter(c => !c.seen).length);

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
		sessionsLoaded = true;
	});

	const unsubClaude = events.on('claude:sessions', (data) => {
		claudeSessions = data;
	});

	const unsubCommits = events.on('commits:sync', async () => {
		commits = await getCommits();
	});

	onDestroy(() => {
		unsubTmux();
		unsubClaude();
		unsubCommits();
	});

	// --- Diff modal ---
	let modalCommit: GitCommit | null = $state(null);
	let modalDiff: string | null = $state(null);
	let modalFormat: 'delta' | 'unified' = $state('unified');
	let modalFiles: string[] = $state([]);
	let modalLoading = $state(false);

	function handleOpenDiff(commit: GitCommit, diff: string, format: 'delta' | 'unified', files: string[]) {
		modalCommit = commit;
		modalDiff = diff;
		modalFormat = format;
		modalFiles = files;
		modalLoading = false;
	}

	function handleToggleFlag() {
		if (!modalCommit) return;
		const newState = !modalCommit.flagged;
		toggleCommitFlag(modalCommit.id, newState);
		commits = commits.map(c => c.id === modalCommit!.id ? { ...c, flagged: newState } : c);
		modalCommit = { ...modalCommit, flagged: newState };
	}

	function closeDiffModal() {
		modalCommit = null;
		modalDiff = null;
	}

	// --- Review result modal ---
	let reviewResult: string | null = $state(null);
	let reviewTask: ClaudeTask | null = $state(null);
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

{#if $connection.reconnecting}
	<div class="flex items-center justify-center gap-3 text-bourbon-600 py-4 mb-4">
		<div class="w-3 h-3 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
		<span class="font-display text-[10px] uppercase tracking-widest">Reconnecting</span>
	</div>
{/if}

<div class="grid grid-cols-1 lg:grid-cols-2 gap-4 items-start">

	<!-- Left column: Sessions -->
	<div class="flex flex-col gap-4">
		<BrewCard />
		<SessionCard bind:sessions {claudeSessions} {sessionsLoaded} />
	</div>

	<!-- Right column: Inbox + Commits -->
	<div class="flex flex-col gap-4">
		<ClaudeInboxCard onviewresult={(task, r) => { reviewTask = task; reviewResult = r; }} />

		{#if commitsLoaded}
			<CommitCard bind:commits onopendiff={handleOpenDiff} />
		{:else}
			<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6">
				<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500 mb-4">Recent Commits</h2>
				<div class="flex items-center gap-2 text-bourbon-600 py-4">
					<div class="w-3 h-3 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
					<span class="text-[10px] font-mono">loading commits</span>
				</div>
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

<!-- Diff Modal -->
{#if modalCommit}
	<DiffModal
		commit={modalCommit}
		diff={modalDiff}
		format={modalFormat}
		files={modalFiles}
		loading={modalLoading}
		onclose={closeDiffModal}
		onflag={handleToggleFlag}
		onsubmitreview={(taskId) => { closeDiffModal(); }}
		onclearreview={() => {
			if (modalCommit) {
				commits = commits.map(c => c.id === modalCommit!.id ? { ...c, reviewCount: 0 } : c);
				modalCommit = { ...modalCommit, reviewCount: 0 };
			}
		}}
	/>
{/if}

<!-- Review Result Modal -->
{#if reviewResult}
	<ReviewResultModal result={reviewResult} taskId={reviewTask?.id ?? 0} prUrl={reviewTask?.prUrl} onclose={() => { reviewResult = null; reviewTask = null; }} onupdate={(r) => { reviewResult = r; }} />
{/if}
