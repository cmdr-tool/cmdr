<script lang="ts">
	import { onMount } from 'svelte';
	import { FolderGit2, FolderCode, Plus, Trash2, RefreshCw, Users, Eye, EyeOff } from 'lucide-svelte';
	import {
		getRepos,
		discoverRepos,
		addRepo,
		removeRepo,
		syncRepos,
		getSquads,
		createSquad,
		deleteSquad,
		assignRepoSquad,
		updateRepoMonitor,
		type MonitoredRepo,
		type DiscoveredRepo,
		type Squad
	} from '$lib/api';

	let repos: MonitoredRepo[] = $state([]);
	let squads: Squad[] = $state([]);
	let discovered: DiscoveredRepo[] = $state([]);
	let loading = $state(true);
	let syncing = $state(false);
	let showAddRepo = $state(false);
	let discovering = $state(false);
	let repoSearch = $state('');

	// Squad creation
	let showNewSquad = $state(false);
	let newSquadName = $state('');

	onMount(async () => {
		try {
			[repos, squads] = await Promise.all([getRepos(), getSquads()]);
		} catch {
			// daemon might be down
		}
		loading = false;
	});

	async function openAddRepo() {
		showAddRepo = true;
		discovering = true;
		repoSearch = '';
		try {
			discovered = await discoverRepos();
		} catch {
			discovered = [];
		}
		discovering = false;
	}

	async function handleAddRepo(repo: DiscoveredRepo) {
		await addRepo(repo);
		discovered = discovered.filter(r => r.path !== repo.path);
		repos = await getRepos();
	}

	async function handleRemoveRepo(id: number) {
		await removeRepo(id);
		repos = await getRepos();
	}

	async function handleSync() {
		syncing = true;
		await syncRepos();
		setTimeout(async () => {
			repos = await getRepos();
			syncing = false;
		}, 3000);
	}

	async function handleCreateSquad() {
		const name = newSquadName.trim().toLowerCase().replace(/\s+/g, '-');
		if (!name) return;
		await createSquad(name);
		squads = await getSquads();
		newSquadName = '';
		showNewSquad = false;
	}

	async function handleDeleteSquad(name: string) {
		await deleteSquad(name);
		[repos, squads] = await Promise.all([getRepos(), getSquads()]);
	}

	async function handleSquadAssign(repoId: number, squad: string) {
		await assignRepoSquad(repoId, squad, '');
		[repos, squads] = await Promise.all([getRepos(), getSquads()]);
	}

	async function handleToggleMonitor(repo: MonitoredRepo) {
		await updateRepoMonitor(repo.id, !repo.monitor);
		repos = await getRepos();
	}

	let filteredDiscovered = $derived(
		repoSearch
			? discovered.filter(r => r.name.toLowerCase().includes(repoSearch.toLowerCase()))
			: discovered
	);

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

	function shortenPath(path: string): string {
		return path.replace(/^\/Users\/[^/]+/, '~');
	}
</script>

<div class="mb-6">
	<h1 class="font-display text-3xl font-bold text-bourbon-100 lowercase">settings</h1>
	<p class="text-bourbon-600 mt-1">Configure repos and squads</p>
</div>

{#if loading}
	<div class="flex items-center justify-center gap-3 text-bourbon-600 py-12">
		<div class="w-4 h-4 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
		<span class="font-display text-xs uppercase tracking-widest">Loading</span>
	</div>
{:else}

<!-- Repos -->
<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6 mb-6">
	<div class="flex items-center justify-between mb-4">
		<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">
			<span class="flex items-center gap-2"><FolderGit2 size={12} /> Repos</span>
		</h2>
		<div class="flex items-center gap-2">
			{#if repos.some(r => r.monitor)}
				<button
					onclick={handleSync}
					disabled={syncing}
					class="flex items-center gap-1.5 px-3 py-1.5 rounded-md
						text-xs font-display font-bold uppercase tracking-widest
						border backdrop-blur-sm transition-colors cursor-pointer
						bg-bourbon-800/40 border-bourbon-700/40 text-bourbon-400
						hover:bg-bourbon-800/60 hover:border-bourbon-600/50 hover:text-bourbon-200
						disabled:opacity-40 disabled:cursor-default"
				>
					<RefreshCw size={12} class={syncing ? 'animate-spin' : ''} />
					Sync now
				</button>
			{/if}
			<button
				onclick={openAddRepo}
				class="flex items-center gap-1.5 px-3 py-1.5 rounded-md
					text-xs font-display font-bold uppercase tracking-widest
					border backdrop-blur-sm transition-colors cursor-pointer
					bg-cmd-700/40 border-cmd-600/30 text-cmd-400
					hover:bg-cmd-700/60 hover:border-cmd-500/50 hover:text-cmd-300"
			>
				<Plus size={12} />
				Add repo
			</button>
		</div>
	</div>

	<!-- Add Repo Panel -->
	{#if showAddRepo}
		<div class="mb-4 bg-bourbon-950/50 border border-bourbon-800 rounded-lg p-4">
			<div class="flex items-center justify-between mb-3">
				<h3 class="font-display text-xs font-bold uppercase tracking-widest text-cmd-400">Add Repository</h3>
				<button
					onclick={() => { showAddRepo = false; repoSearch = ''; }}
					class="text-bourbon-600 hover:text-bourbon-400 text-xs cursor-pointer"
				>
					cancel
				</button>
			</div>

			{#if !discovering}
				<input
					type="text"
					placeholder="Filter repos..."
					bind:value={repoSearch}
					class="w-full bg-bourbon-900 border border-bourbon-700 rounded-lg px-3 py-2 text-sm text-bourbon-200
						placeholder:text-bourbon-600 focus:outline-none focus:border-cmd-500 mb-3"
				/>
			{/if}

			<div class="max-h-64 overflow-y-auto flex flex-col gap-1">
				{#if discovering}
					<div class="flex items-center justify-center gap-2 py-4 text-bourbon-600">
						<div class="w-3 h-3 border-2 border-bourbon-700 border-t-cmd-500 rounded-full animate-spin"></div>
						<span class="text-xs">Scanning for repos...</span>
					</div>
				{:else}
					{#each filteredDiscovered as repo}
						<button
							onclick={() => handleAddRepo(repo)}
							class="flex items-center justify-between px-3 py-2 rounded-md text-left
								text-bourbon-300 hover:bg-bourbon-800/50 transition-colors cursor-pointer"
						>
							<div class="flex items-center gap-2">
								<FolderCode size={12} class="text-bourbon-600" />
								<span class="text-sm">{repo.name}</span>
								<span class="text-xs text-bourbon-600 font-mono">{shortenPath(repo.path)}</span>
							</div>
							<Plus size={14} class="text-bourbon-600" />
						</button>
					{:else}
						<p class="text-bourbon-600 text-sm px-3 py-2">
							{repoSearch ? 'No matching repos' : 'No new repos found'}
						</p>
					{/each}
				{/if}
			</div>
		</div>
	{/if}

	{#if repos.length === 0}
		<p class="text-bourbon-600 text-sm">No repos monitored yet. Click Add repo to add one.</p>
	{:else}
		<div class="flex flex-col gap-1.5">
			{#each repos as repo}
				<div class="group flex items-center justify-between bg-bourbon-950/30 border border-bourbon-800 rounded-lg px-5 py-3.5">
					<div class="flex items-center gap-3">
						<FolderCode size={14} class="text-cmd-400" />
						<span class="text-bourbon-200">{repo.name}</span>
						<span class="text-xs text-bourbon-600 font-mono">{shortenPath(repo.path)}</span>
						{#if repo.monitor && repo.lastSyncedAt}
							<span class="text-xs text-bourbon-600">&middot; synced {timeAgo(repo.lastSyncedAt)}</span>
						{:else if repo.monitor}
							<span class="text-xs text-run-500">syncing...</span>
						{/if}
					</div>
					<div class="flex items-center gap-3">
						<select
							value={repo.squad}
							onchange={(e) => handleSquadAssign(repo.id, (e.target as HTMLSelectElement).value)}
							disabled={squads.length === 0}
							class="bg-transparent border border-bourbon-800 rounded px-2 py-0.5 text-xs font-mono
								text-bourbon-400 focus:outline-none focus:border-cmd-500 cursor-pointer
								disabled:opacity-30 disabled:cursor-default"
						>
							<option value="">no squad</option>
							{#each squads as s}
								<option value={s.name}>{s.name}</option>
							{/each}
						</select>
						<!-- Monitor toggle switch -->
						<button
							onclick={() => handleToggleMonitor(repo)}
							class="relative w-9 h-5 rounded-full transition-colors cursor-pointer shrink-0"
							class:bg-cmd-500={repo.monitor}
							class:bg-bourbon-800={!repo.monitor}
							title={repo.monitor ? 'Monitoring commits' : 'Not monitoring commits'}
						>
							<span
								class="absolute top-0.5 left-0.5 w-4 h-4 rounded-full bg-bourbon-200 transition-transform flex items-center justify-center"
								class:translate-x-4={repo.monitor}
							>
								{#if repo.monitor}
									<Eye size={10} class="text-cmd-700" />
								{:else}
									<EyeOff size={10} class="text-bourbon-600" />
								{/if}
							</span>
						</button>
						<button
							onclick={() => handleRemoveRepo(repo.id)}
							class="opacity-0 group-hover:opacity-100 text-bourbon-700 hover:text-red-400 transition-all cursor-pointer"
						>
							<Trash2 size={14} />
						</button>
					</div>
				</div>
			{/each}
		</div>
	{/if}

</div>

<!-- Squads -->
<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6">
	<div class="flex items-center justify-between mb-4">
		<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">
			<span class="flex items-center gap-2"><Users size={12} /> Squads</span>
		</h2>
		<button
			onclick={() => { showNewSquad = !showNewSquad; newSquadName = ''; }}
			class="flex items-center gap-1.5 px-3 py-1.5 rounded-md
				text-xs font-display font-bold uppercase tracking-widest
				border backdrop-blur-sm transition-colors cursor-pointer
				bg-cmd-700/40 border-cmd-600/30 text-cmd-400
				hover:bg-cmd-700/60 hover:border-cmd-500/50 hover:text-cmd-300"
		>
			<Plus size={12} />
			Add squad
		</button>
	</div>

	{#if showNewSquad}
		<div class="flex items-center gap-2 mb-4">
			<input
				type="text"
				placeholder="squad name (e.g. minicart)"
				bind:value={newSquadName}
				onkeydown={(e) => { if (e.key === 'Enter') handleCreateSquad(); if (e.key === 'Escape') showNewSquad = false; }}
				class="flex-1 bg-bourbon-950 border border-bourbon-700 rounded-lg px-3 py-2 text-sm text-bourbon-200
					placeholder:text-bourbon-600 focus:outline-none focus:border-cmd-500 font-mono"
			/>
			<button
				onclick={handleCreateSquad}
				disabled={!newSquadName.trim()}
				class="px-3 py-2 text-xs font-display font-bold uppercase tracking-widest
					text-cmd-400 hover:text-cmd-300 transition-colors cursor-pointer disabled:opacity-30"
			>
				Create
			</button>
		</div>
	{/if}

	{#if squads.length === 0}
		<p class="text-bourbon-600 text-sm">No squads yet. Create one to group repos for inter-Claude delegation.</p>
	{:else}
		<div class="flex flex-col gap-3">
			{#each squads as s}
				<div class="group bg-bourbon-950/30 border border-bourbon-800 rounded-lg px-5 py-3.5">
					<div class="flex items-center justify-between">
						<span class="text-bourbon-200">{s.name}</span>
						<div class="flex items-center gap-3">
							<span class="text-xs text-bourbon-600">
								{s.repos.length} member{s.repos.length !== 1 ? 's' : ''}
							</span>
							<button
								onclick={() => handleDeleteSquad(s.name)}
								class="opacity-0 group-hover:opacity-100 text-bourbon-700 hover:text-red-400 transition-all cursor-pointer"
							>
								<Trash2 size={14} />
							</button>
						</div>
					</div>
					{#if s.repos.length > 0}
						<div class="mt-2 flex flex-wrap gap-2">
							{#each s.repos as member}
								<span class="text-xs font-mono text-bourbon-400 bg-bourbon-900/50 border border-bourbon-800 rounded px-2 py-0.5">
									{member.alias || member.name}
								</span>
							{/each}
						</div>
					{/if}
				</div>
			{/each}
		</div>
	{/if}
</div>

{/if}
