<script lang="ts">
	import type { AgentTask, GitCommit } from '$lib/api';
	import { getAgentTaskResult, getStatus } from '$lib/api';
	import { onMount } from 'svelte';
	import { ScanSearch } from 'lucide-svelte';
	import { connection } from '$lib/events';
	import { sessions, agentSessions } from '$lib/sessionStore';
	import { commits, commitsLoaded, unseenCount, toggleFlag, updateCommit } from '$lib/commitStore';

	import BrewCard from '$lib/components/BrewCard.svelte';
	import SessionCard from '$lib/components/SessionCard.svelte';
	import CommitCard from '$lib/components/CommitCard.svelte';
	import AskBubble from '$lib/components/AskBubble.svelte';
	import AgentInboxCard from '$lib/components/AgentInboxCard.svelte';
	import DiffModal from '$lib/components/DiffModal.svelte';
	import ReviewResultModal from '$lib/components/ReviewResultModal.svelte';
	import DesignResultModal from '$lib/components/DesignResultModal.svelte';
	import AgentResultModal from '$lib/components/AgentResultModal.svelte';
	import DraftModal from '$lib/components/DraftModal.svelte';
	import MissionsModal from '$lib/components/MissionsModal.svelte';

	const now = new Date();
	const hour = now.getHours();
	const greeting = hour < 12 ? 'good morning' : hour < 17 ? 'good afternoon' : 'good evening';
	const dateStr = now.toLocaleDateString('en-US', {
		weekday: 'long',
		month: 'long',
		day: 'numeric'
	});

	let userName = $state('');
	let askSkillAvailable = $state(false);
	onMount(async () => {
		try {
			const status = await getStatus();
			userName = status.user ?? '';
			askSkillAvailable = status.capabilities?.askSkill ?? false;
		} catch {
			// Silent — greeting falls back to just the time-of-day phrase.
		}
	});

	// --- Diff modal ---
	let modalCommit: GitCommit | null = $state(null);
	let modalDiff: string | null = $state(null);
	let modalFiles: string[] = $state([]);

	function handleOpenDiff(commit: GitCommit, diff: string, files: string[]) {
		modalCommit = commit;
		modalDiff = diff;
		modalFiles = files;
	}

	function handleToggleFlag() {
		if (!modalCommit) return;
		toggleFlag(modalCommit.id);
		modalCommit = { ...modalCommit, flagged: !modalCommit.flagged };
	}

	function closeDiffModal() {
		modalCommit = null;
		modalDiff = null;
	}

	// --- Review result modal ---
	let reviewTask: AgentTask | null = $state(null);

	// --- Design result modal ---
	let designResult: string | null = $state(null);
	let designTask: AgentTask | null = $state(null);

	// --- Claude result modal (ask, analysis, etc.) ---
	let resultTask: AgentTask | null = $state(null);

	// --- Missions modal ---
	let missionsSquad: string | null = $state(null);

	// --- Draft modal ---
	let showDraft = $state(false);
	let draftInitial: { repoPath?: string; content?: string; taskId?: number } | undefined = $state(undefined);

	function openDraft(repoPath?: string, content?: string, taskId?: number) {
		draftInitial = { repoPath, content, taskId };
		showDraft = true;
	}
</script>

<!-- Greeting -->
<div class="mb-6">
	<h1 class="font-display text-3xl font-bold text-bourbon-100 lowercase">{greeting}{userName ? `, ${userName}` : ''}</h1>
		<p class="text-bourbon-600 mt-1">
			{dateStr}
			&middot; {$sessions.length} session{$sessions.length !== 1 ? 's' : ''}
		&middot; {$agentSessions.length} agent{$agentSessions.length !== 1 ? 's' : ''}
		{#if $unseenCount > 0}
			&middot; {$unseenCount} unseen commit{$unseenCount !== 1 ? 's' : ''}
		{/if}
	</p>
</div>

{#if $connection.reconnecting}
	<div class="fixed bottom-4 left-1/2 -translate-x-1/2 z-50 flex items-center gap-2 bg-bourbon-900 border border-run-500/40 rounded-full px-5 py-2.5 shadow-lg shadow-run-500/10">
		<div class="w-3 h-3 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
		<span class="font-display text-[10px] uppercase tracking-widest text-run-400">Reconnecting</span>
	</div>
{/if}

<div class="grid grid-cols-1 lg:grid-cols-2 gap-4 items-start">

	<!-- Left column: Sessions -->
	<div class="flex flex-col gap-4">
		<BrewCard />
		<SessionCard />
	</div>

	<!-- Right column: Inbox + Commits -->
	<div class="flex flex-col gap-4">
		<AgentInboxCard
			ontaskclick={async (task) => {
				if (task.type === 'review') {
					reviewTask = task;
				} else if (task.type === 'ask' || task.intent === 'analysis') {
					resultTask = task;
				} else if ((task.status === 'resolved' || task.status === 'completed') && task.prUrl) {
					window.open(task.prUrl, '_blank');
				} else if (task.status === 'resolved' && (task.intent === 'new-feature' || task.intent === 'refactor' || task.type === 'revision')) {
					try {
						const { result } = await getAgentTaskResult(task.id);
						designTask = task;
						designResult = result;
					} catch { /* silent */ }
				}
			}}
			ondraft={(taskId, repoPath) => openDraft(repoPath, undefined, taskId)}
			onopenmissions={(squad) => { missionsSquad = squad; }}
		/>

		{#if $commitsLoaded}
			<CommitCard onopendiff={handleOpenDiff} />
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

<!-- Diff Modal -->
{#if modalCommit}
	<DiffModal
		commit={modalCommit}
		diff={modalDiff}
		files={modalFiles}
		onclose={closeDiffModal}
		onflag={handleToggleFlag}
		onsubmitreview={() => closeDiffModal()}
		ondraft={(repoPath, content) => { closeDiffModal(); openDraft(repoPath, content); }}
		onclearreview={() => {
			if (modalCommit) {
				updateCommit(modalCommit.id, { reviewCount: 0 });
				modalCommit = { ...modalCommit, reviewCount: 0 };
			}
		}}
	/>
{/if}

<!-- Design Result Modal -->
{#if designResult}
	<DesignResultModal
		result={designResult}
		taskId={designTask?.id ?? 0}
		repoPath={designTask?.repoPath ?? ''}
		onclose={() => { designResult = null; designTask = null; }}
		onupdate={(r) => { designResult = r; }}
	/>
{/if}

<!-- Claude Result Modal (ask, analysis, etc.) -->
{#if resultTask}
	<AgentResultModal
		taskId={resultTask.id}
		title={resultTask.intent === 'analysis' ? 'Analysis' : 'Ask Claude'}
		titleClass="text-run-500"
		icon={resultTask.intent === 'analysis' ? ScanSearch : undefined}
		emptyHint={resultTask.intent === 'analysis' ? 'analyzing' : 'thinking'}
		outputFormat={resultTask.outputFormat ?? 'markdown'}
		onclose={() => { resultTask = null; }}
	/>
{/if}

<!-- Review Result Modal -->
{#if reviewTask}
	<ReviewResultModal
		taskId={reviewTask.id}
		prUrl={reviewTask.prUrl}
		repoPath={reviewTask.repoPath ?? ''}
		commitSha={reviewTask.commitSha ?? ''}
		commitUrl={$commits.find(c => c.sha === reviewTask?.commitSha)?.url ?? ''}
		onclose={() => { reviewTask = null; }}
	/>
{/if}

<!-- Draft Modal -->
{#if showDraft}
	<DraftModal
		initial={draftInitial}
		onclose={() => { showDraft = false; draftInitial = undefined; }}
	/>
{/if}

<!-- Missions Modal -->
{#if missionsSquad}
	<MissionsModal squad={missionsSquad} onclose={() => { missionsSquad = null; }} />
{/if}

<!-- Ask Bubble (only when /ask skill is available) -->
{#if askSkillAvailable}
	<AskBubble />
{/if}
