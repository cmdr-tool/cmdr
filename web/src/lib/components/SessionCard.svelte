<script lang="ts">
	import { ArrowRightLeft, Sparkles, X, Focus } from 'lucide-svelte';
	import ActivityChart from './ActivityChart.svelte';
	import {
		focusTmuxSession,
		switchTmuxSession,
		openFolder,
		killAgentInstance,
		type AgentSession
	} from '$lib/api';
	import {
		sessions as sessionsStore,
		agentSessions as agentSessionsStore,
		sessionsLoaded as sessionsLoadedStore,
		killSession,
		markAttached
	} from '$lib/sessionStore';

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
		await killSession(name);
		setTimeout(() => { killedSession = null; }, 3000);
	}

	function shortenPath(path: string): string {
		return path.replace(/^\/Users\/[^/]+/, '~');
	}

	function paneTarget(sessionName: string, winIdx: number, paneIdx: number): string {
		return `${sessionName}:${winIdx}.${paneIdx}`;
	}

	function displayAgentName(agentName: string): string {
		return agentName.charAt(0).toUpperCase() + agentName.slice(1);
	}

	// Map agent sessions by their tmux pane target
	let agentByTarget = $derived(
		new Map($agentSessionsStore.filter((c) => c.tmuxTarget).map((c) => [c.tmuxTarget, c]))
	);

	// Best agent status per tmux session (for session-level badge)
	const statusRank: Record<string, number> = { working: 3, waiting: 2, idle: 1, unknown: 0 };
	let agentBySession = $derived(() => {
		const map = new Map<string, AgentSession>();
		for (const c of $agentSessionsStore) {
			if (!c.tmuxTarget) continue;
			const sessName = c.tmuxTarget.split(':')[0];
			const existing = map.get(sessName);
			if (!existing || (statusRank[c.status] ?? 0) > (statusRank[existing.status] ?? 0)) {
				map.set(sessName, c);
			}
		}
		return map;
	});

	// Kill state for additional instances
	let holdingKillPid: number | null = $state(null);
	let holdProgressPid: number = $state(0);
	let holdRafPid: number | null = null;
	let holdStartPid: number = 0;
	let killedPids = $state<Set<number>>(new Set());

	function startHoldKillPid(pid: number) {
		holdingKillPid = pid;
		holdProgressPid = 0;
		holdStartPid = 0;
		function tick(timestamp: number) {
			if (!holdStartPid) holdStartPid = timestamp;
			holdProgressPid = Math.min((timestamp - holdStartPid) / HOLD_DURATION, 1);
			if (holdProgressPid >= 1) { completeKillPid(pid); return; }
			holdRafPid = requestAnimationFrame(tick);
		}
		holdRafPid = requestAnimationFrame(tick);
	}

	function cancelHoldKillPid() {
		if (holdRafPid) cancelAnimationFrame(holdRafPid);
		holdRafPid = null;
		holdingKillPid = null;
		holdProgressPid = 0;
	}

	async function completeKillPid(pid: number) {
		if (holdRafPid) cancelAnimationFrame(holdRafPid);
		holdRafPid = null;
		holdingKillPid = null;
		holdProgressPid = 0;
		await killAgentInstance(pid);
		killedPids = new Set([...killedPids, pid]);
		setTimeout(() => {
			killedPids.delete(pid);
			killedPids = killedPids;
		}, 3000);
	}

	// Group unmatched instances by agent name
	let unmatchedByAgent = $derived(() => {
		const groups = new Map<string, AgentSession[]>();
		for (const inst of $agentSessionsStore) {
			if (inst.tmuxTarget) continue;
			const list = groups.get(inst.agent) ?? [];
			list.push(inst);
			groups.set(inst.agent, list);
		}
		return groups;
	});
</script>

<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6">
	<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500 mb-4">Sessions</h2>

	<!-- Activity chart -->
	<div class="mb-4">
		<ActivityChart />
	</div>

	{#if !$sessionsLoadedStore}
		<div class="flex items-center gap-2 text-bourbon-600 py-4">
			<div class="w-3 h-3 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
			<span class="text-[10px] font-mono">loading sessions</span>
		</div>
	{:else if $sessionsStore.length === 0}
		<p class="text-bourbon-600 text-sm">No tmux sessions running.</p>
	{:else}
		<div class="flex flex-col gap-1.5">
			{#each $sessionsStore as session}
				{@const agentInst = agentBySession().get(session.name)}
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
							{#if agentInst}
								{@const statusStyle = {
									working: 'text-green-400 bg-green-900/30',
									waiting: 'text-run-400 bg-run-700/30 animate-pulse',
									idle: 'text-bourbon-500 bg-bourbon-800/30',
									unknown: 'text-cmd-400 bg-cmd-700/30 animate-pulse'
								}[agentInst.status]}
								{@const statusLabel = {
									working: `working`,
									waiting: `waiting`,
									idle: `idle${agentInst.uptime ? ` · ${agentInst.uptime}` : ''}`,
									unknown: `?${agentInst.uptime ? ` · ${agentInst.uptime}` : ''}`
								}[agentInst.status]}
								<span class="flex items-center gap-1 text-xs font-medium px-2.5 py-0.5 rounded-full {statusStyle}">
									<Sparkles size={10} />
									{statusLabel}
								</span>
							{/if}
						</div>
						<div class="flex flex-col gap-1 ml-4">
							{#each session.windows as window}
								{#each window.panes as pane}
									{@const paneClause = agentByTarget.get(paneTarget(session.name, window.index, pane.index))}
									{#if pane.command || pane.cwd || paneClause}
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
												{st === 'idle' ? `idle${paneClause.uptime ? ` · ${paneClause.uptime}` : ''}` : st === 'unknown' ? `?${paneClause.uptime ? ` · ${paneClause.uptime}` : ''}` : st}
											</span>
										{/if}
										{#if pane.cwd}
										<button
											onclick={(e) => { e.stopPropagation(); openFolder(pane.cwd); }}
											class="text-bourbon-500 font-mono text-xs hover:text-cmd-400 transition-colors cursor-pointer truncate min-w-0"
										>{shortenPath(pane.cwd)}</button>
										{/if}
									</div>
									{/if}
								{/each}
							{/each}
						</div>
					</div>
					<div class="absolute right-0 top-0 bottom-0 flex items-center gap-1.5 pr-4 pl-10 opacity-0 group-hover:opacity-100 transition-opacity bg-linear-to-r from-transparent to-30% {session.attached ? 'to-bourbon-800' : 'to-bourbon-900'}">
						{#if session.attached}
							<button
								onclick={() => focusTmuxSession(session.name)}
								class="btn-chiclet btn-chiclet-alt"
							>
								<Focus size={14} />
							</button>
						{:else}
							<button
								onclick={() => {
									switchTmuxSession(session.name);
									markAttached(session.name);
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
							class="btn-chiclet btn-chiclet-danger relative overflow-hidden"
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

	{#each [...unmatchedByAgent().entries()] as [agentName, instances]}
		<h3 class="text-xs font-semibold text-bourbon-500 mt-6 mb-2">Additional {displayAgentName(agentName)} instances</h3>
		<div class="flex flex-col gap-1.5">
			{#each instances as instance}
				{#if killedPids.has(instance.pid)}
					<div class="flex items-center justify-center border border-red-900/30 rounded-lg px-5 py-3.5 text-red-400 animate-fade-out">
						<span class="font-display text-xs font-bold uppercase tracking-widest">killed pid {instance.pid}</span>
					</div>
				{:else}
				<div class="group relative flex items-center gap-3 bg-bourbon-950/30 border border-bourbon-800 rounded-lg px-5 py-3.5 min-w-0">
					<span class="text-cmd-400 shrink-0"><Sparkles size={14} /></span>
					<span class="text-xs font-semibold text-bourbon-100 shrink-0">{instance.project}</span>
					<span
						class="text-xs text-bourbon-600 font-mono truncate min-w-0"
						style="direction: rtl; text-align: left;"
					><bdi>{shortenPath(instance.cwd)}</bdi></span>
					{#if instance.uptime}
						<span class="text-xs text-bourbon-600 shrink-0">&middot; {instance.uptime}</span>
					{/if}
					<span class="text-xs text-bourbon-600 shrink-0">&middot; pid {instance.pid}</span>
					<div class="ml-auto opacity-0 group-hover:opacity-100 transition-opacity shrink-0">
						<button
							onmousedown={() => startHoldKillPid(instance.pid)}
							onmouseup={cancelHoldKillPid}
							onmouseleave={cancelHoldKillPid}
							class="btn-chiclet btn-chiclet-danger relative overflow-hidden"
						>
							{#if holdingKillPid === instance.pid}
								<div
									class="absolute inset-x-0 bottom-0 bg-red-500/40 transition-none"
									style="height: {holdProgressPid * 100}%"
								></div>
							{/if}
							<X size={14} class="relative z-10" />
						</button>
					</div>
				</div>
				{/if}
			{/each}
		</div>
	{/each}
</div>
