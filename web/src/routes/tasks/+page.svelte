<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { Play, Plus, Pencil, Trash2, Power, PowerOff, ChevronDown, ChevronUp, Timer, Bot } from 'lucide-svelte';
	import {
		getTasks, runTask, type Task,
		getAgenticTasks, createAgenticTask, updateAgenticTask, deleteAgenticTask, runAgenticTask,
		type AgenticTask, getRepos, type MonitoredRepo
	} from '$lib/api';
	import { events } from '$lib/events';

	let systemTasks: Task[] = $state([]);
	let agenticTasks: AgenticTask[] = $state([]);
	let repos: MonitoredRepo[] = $state([]);
	let loading = $state(true);
	let runningSystem: string | null = $state(null);
	let runningAgentic: number | null = $state(null);
	let systemResult: { task: string; output: string } | null = $state(null);

	// Form state
	let showForm = $state(false);
	let editingId: number | null = $state(null);
	let formName = $state('');
	let formPrompt = $state('');
	let formSchedule = $state('');
	let formWorkDir = $state('');
	let formSaving = $state(false);

	// Expanded result view
	let expandedId: number | null = $state(null);

	onMount(async () => {
		try {
			[systemTasks, agenticTasks, repos] = await Promise.all([
				getTasks(),
				getAgenticTasks(),
				getRepos(),
			]);
		} catch { /* daemon might be down */ }
		loading = false;
	});

	// Live updates via SSE
	const unsub1 = events.on('agentic:run', (data) => {
		agenticTasks = agenticTasks.map(t =>
			t.id === data.id
				? { ...t, last_status: data.status, last_run_at: data.last_run_at ?? t.last_run_at }
				: t
		);
		if (data.status !== 'running') {
			runningAgentic = null;
			// Refresh to get full last_result
			getAgenticTasks().then(ts => { agenticTasks = ts; });
		}
	});

	const unsub2 = events.on('agentic:update', () => {
		getAgenticTasks().then(ts => { agenticTasks = ts; });
	});

	onDestroy(() => { unsub1(); unsub2(); });

	async function executeSystem(name: string) {
		runningSystem = name;
		systemResult = null;
		try {
			const res = await runTask(name);
			systemResult = { task: name, output: res.output };
		} catch (e) {
			systemResult = { task: name, output: e instanceof Error ? e.message : 'Failed' };
		}
		runningSystem = null;
	}

	async function executeAgentic(id: number) {
		runningAgentic = id;
		try {
			await runAgenticTask(id);
		} catch { /* SSE will update status */ }
	}

	function openCreateForm() {
		editingId = null;
		formName = '';
		formPrompt = '';
		formSchedule = '0 0 * * * *';
		formWorkDir = '';
		showForm = true;
	}

	function openEditForm(t: AgenticTask) {
		editingId = t.id;
		formName = t.name;
		formPrompt = t.prompt;
		formSchedule = t.schedule;
		formWorkDir = t.working_dir;
		showForm = true;
	}

	function closeForm() {
		showForm = false;
		editingId = null;
	}

	async function handleSave() {
		formSaving = true;
		try {
			if (editingId) {
				// Preserve the current enabled state when editing
				const existing = agenticTasks.find(t => t.id === editingId);
				await updateAgenticTask({
					id: editingId,
					name: formName.trim(),
					prompt: formPrompt.trim(),
					schedule: formSchedule.trim(),
					enabled: existing?.enabled ?? true,
					working_dir: formWorkDir,
				});
			} else {
				await createAgenticTask({
					name: formName.trim(),
					prompt: formPrompt.trim(),
					schedule: formSchedule.trim(),
					enabled: true,
					working_dir: formWorkDir,
				});
			}
			agenticTasks = await getAgenticTasks();
			closeForm();
		} catch { /* silent */ }
		formSaving = false;
	}

	async function handleDelete(id: number) {
		await deleteAgenticTask(id);
		agenticTasks = await getAgenticTasks();
		if (expandedId === id) expandedId = null;
	}

	async function handleToggleEnabled(t: AgenticTask) {
		await updateAgenticTask({
			id: t.id,
			name: t.name,
			prompt: t.prompt,
			schedule: t.schedule,
			enabled: !t.enabled,
			working_dir: t.working_dir,
		});
		agenticTasks = await getAgenticTasks();
	}

	function timeAgo(dateStr: string | null): string {
		if (!dateStr) return 'never';
		const diff = Date.now() - new Date(dateStr).getTime();
		const mins = Math.floor(diff / 60000);
		if (mins < 1) return 'just now';
		if (mins < 60) return `${mins}m ago`;
		const hrs = Math.floor(mins / 60);
		if (hrs < 24) return `${hrs}h ago`;
		return `${Math.floor(hrs / 24)}d ago`;
	}

	let formValid = $derived(formName.trim() && formPrompt.trim() && formSchedule.trim());
</script>

<div class="mb-6">
	<h1 class="font-display text-3xl font-bold text-bourbon-100 lowercase">tasks</h1>
	<p class="text-bourbon-600 mt-1">Background scheduled tasks</p>
</div>

{#if loading}
	<div class="flex items-center justify-center gap-3 text-bourbon-600 py-12">
		<div class="w-4 h-4 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
		<span class="font-display text-xs uppercase tracking-widest">Loading</span>
	</div>
{:else}

<!-- System Tasks -->
<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6 mb-6">
	<div class="flex items-center justify-between mb-4">
		<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">
			<span class="flex items-center gap-2"><Timer size={12} /> System Tasks</span>
		</h2>
	</div>

	{#if systemTasks.length === 0}
		<p class="text-bourbon-600 text-sm">No system tasks registered.</p>
	{:else}
		<div class="flex flex-col gap-1.5">
			{#each systemTasks as task}
				<div class="flex items-center justify-between bg-bourbon-950/30 border border-bourbon-800 rounded-lg px-5 py-3.5">
					<div class="flex flex-col gap-0.5">
						<div class="flex items-center gap-3">
							<span class="text-bourbon-200">{task.name}</span>
							<span class="text-xs font-medium text-cmd-400 bg-cmd-700/40 px-2.5 py-0.5 rounded-full font-mono">{task.schedule}</span>
						</div>
						{#if task.description}
							<span class="text-sm text-bourbon-500">{task.description}</span>
						{/if}
					</div>
					<button
						onclick={() => executeSystem(task.name)}
						disabled={runningSystem === task.name}
						class="btn-chiclet"
					>
						{#if runningSystem === task.name}
							<div class="w-3.5 h-3.5 border-2 border-bourbon-700 border-t-cmd-300 rounded-full animate-spin"></div>
						{:else}
							<Play size={14} />
						{/if}
					</button>
				</div>
			{/each}
		</div>
	{/if}

	{#if systemResult}
		<div class="mt-4 pt-4 border-t border-bourbon-800/50">
			<div class="flex items-center gap-2 mb-3">
				<h3 class="font-display text-[10px] font-bold uppercase tracking-widest text-bourbon-500">Output</h3>
				<span class="text-xs font-medium text-cmd-400 bg-cmd-700/40 px-2.5 py-0.5 rounded-full">{systemResult.task}</span>
			</div>
			<pre class="text-sm whitespace-pre-wrap wrap-break-word text-bourbon-300 font-mono bg-bourbon-950 rounded-lg p-4">{systemResult.output}</pre>
		</div>
	{/if}
</div>

<!-- Agentic Tasks -->
<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6">
	<div class="flex items-center justify-between mb-4">
		<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">
			<span class="flex items-center gap-2"><Bot size={12} /> Agentic Tasks</span>
		</h2>
		<button onclick={openCreateForm} class="btn-chiclet">
			<Plus size={14} />
		</button>
	</div>

	<!-- Create / Edit Form -->
	{#if showForm}
		<div class="bg-bourbon-950/50 border border-bourbon-800 rounded-lg p-4 mb-4">
			<div class="flex items-center justify-between mb-3">
				<h3 class="font-display text-xs font-bold uppercase tracking-widest text-cmd-400">
					{editingId ? 'Edit Task' : 'New Task'}
				</h3>
				<button
					onclick={closeForm}
					class="text-bourbon-600 hover:text-bourbon-400 text-xs cursor-pointer"
				>
					cancel
				</button>
			</div>

			<div class="flex flex-col gap-3">
				<div class="flex gap-3">
					<label class="flex-1">
						<span class="block text-[10px] font-mono text-bourbon-600 mb-1">Name</span>
						<input
							type="text"
							bind:value={formName}
							placeholder="e.g. distill"
							class="w-full bg-bourbon-900 border border-bourbon-700 rounded-lg px-3 py-2 text-sm text-bourbon-200 placeholder:text-bourbon-600 focus:outline-none focus:border-cmd-500 transition-colors"
						/>
					</label>
					<label class="w-48">
						<span class="block text-[10px] font-mono text-bourbon-600 mb-1">Schedule (cron)</span>
						<input
							type="text"
							bind:value={formSchedule}
							placeholder="0 0 * * * *"
							class="w-full bg-bourbon-900 border border-bourbon-700 rounded-lg px-3 py-2 text-sm font-mono text-bourbon-200 placeholder:text-bourbon-600 focus:outline-none focus:border-cmd-500 transition-colors"
						/>
					</label>
				</div>

				<label>
					<span class="block text-[10px] font-mono text-bourbon-600 mb-1">Prompt</span>
					<textarea
						bind:value={formPrompt}
						placeholder="The prompt to send to claude -p"
						rows="3"
						class="w-full bg-bourbon-900 border border-bourbon-700 rounded-lg px-3 py-2 text-sm text-bourbon-200 placeholder:text-bourbon-600 focus:outline-none focus:border-cmd-500 transition-colors resize-none font-mono leading-relaxed"
					></textarea>
				</label>

				<label>
					<span class="block text-[10px] font-mono text-bourbon-600 mb-1">Working Directory</span>
					<select
						bind:value={formWorkDir}
						class="w-full bg-bourbon-900 border border-bourbon-700 rounded-lg px-3 h-9 text-sm text-bourbon-200 focus:outline-none focus:border-cmd-500 transition-colors font-mono"
					>
						<option value="">$HOME (default)</option>
						{#each repos as repo}
							<option value={repo.path}>{repo.name}</option>
						{/each}
					</select>
				</label>

				<div class="flex justify-end pt-1">
					<button
						onclick={handleSave}
						disabled={!formValid || formSaving}
						class="px-3 py-2 text-xs font-display font-bold uppercase tracking-widest
							text-cmd-400 hover:text-cmd-300 transition-colors cursor-pointer disabled:opacity-30"
					>
						{#if formSaving}
							<div class="w-3 h-3 border-2 border-bourbon-700 border-t-cmd-300 rounded-full animate-spin"></div>
						{:else}
							{editingId ? 'Save' : 'Create'}
						{/if}
					</button>
				</div>
			</div>
		</div>
	{/if}

	<!-- Task List -->
	{#if agenticTasks.length === 0 && !showForm}
		<p class="text-bourbon-600 text-sm">No agentic tasks configured. Click + to add one.</p>
	{:else}
		<div class="flex flex-col gap-1.5">
			{#each agenticTasks as task}
				{#if editingId === task.id}{:else}
				<div class="group bg-bourbon-950/30 border border-bourbon-800 rounded-lg overflow-hidden">
					<div class="flex items-center justify-between px-5 py-3.5">
						<div class="flex items-center gap-3 min-w-0 flex-1">
							<!-- Enable toggle -->
							<button
								onclick={() => handleToggleEnabled(task)}
								class="relative w-9 h-5 rounded-full transition-colors cursor-pointer shrink-0"
								class:bg-cmd-500={task.enabled}
								class:bg-bourbon-800={!task.enabled}
								title={task.enabled ? 'Enabled' : 'Disabled'}
							>
								<span
									class="absolute top-0.5 left-0.5 w-4 h-4 bg-bourbon-200 rounded-full flex items-center justify-center transition-transform"
									class:translate-x-4={task.enabled}
								>
									{#if task.enabled}
										<Power size={10} class="text-cmd-700" />
									{:else}
										<PowerOff size={10} class="text-bourbon-600" />
									{/if}
								</span>
							</button>

							<div class="flex flex-col gap-0.5 min-w-0">
								<div class="flex items-center gap-3">
									<span class="text-bourbon-200" class:text-bourbon-500={!task.enabled}>{task.name}</span>
									<span class="text-xs font-medium text-cmd-400 bg-cmd-700/40 px-2.5 py-0.5 rounded-full font-mono">{task.schedule}</span>
									{#if task.last_status === 'success'}
										<span class="text-[9px] font-mono text-green-500">passed</span>
									{:else if task.last_status === 'failed'}
										<span class="text-[9px] font-mono text-red-500">failed</span>
									{/if}
									{#if task.last_run_at}
										<span class="text-[9px] font-mono text-bourbon-700">{timeAgo(task.last_run_at)}</span>
									{/if}
								</div>
								<span class="text-sm text-bourbon-500 truncate">{task.prompt}</span>
							</div>
						</div>

						<div class="flex items-center gap-4 shrink-0 ml-4">
							<!-- Expand result -->
							{#if task.last_result}
								<button
									onclick={() => { expandedId = expandedId === task.id ? null : task.id; }}
									class="text-bourbon-600 hover:text-bourbon-400 transition-colors cursor-pointer"
									title="View last output"
								>
									{#if expandedId === task.id}
										<ChevronUp size={14} />
									{:else}
										<ChevronDown size={14} />
									{/if}
								</button>
							{/if}

							<!-- Edit (hover reveal) -->
							<button
								onclick={() => openEditForm(task)}
								class="opacity-0 group-hover:opacity-100 text-bourbon-700 hover:text-bourbon-400 transition-all cursor-pointer"
								title="Edit"
							>
								<Pencil size={14} />
							</button>

							<!-- Delete (hover reveal) -->
							<button
								onclick={() => handleDelete(task.id)}
								class="opacity-0 group-hover:opacity-100 text-bourbon-700 hover:text-red-400 transition-all cursor-pointer"
								title="Delete"
							>
								<Trash2 size={14} />
							</button>

							<!-- Run -->
							<button
								onclick={() => executeAgentic(task.id)}
								disabled={runningAgentic === task.id}
								class="btn-chiclet"
							>
								{#if runningAgentic === task.id}
									<div class="w-3.5 h-3.5 border-2 border-bourbon-700 border-t-cmd-300 rounded-full animate-spin"></div>
								{:else}
									<Play size={14} />
								{/if}
							</button>
						</div>
					</div>

					<!-- Expanded last result -->
					{#if expandedId === task.id && task.last_result}
						<div class="px-5 pb-4 border-t border-bourbon-800/50">
							<pre class="text-xs whitespace-pre-wrap wrap-break-word text-bourbon-400 font-mono bg-bourbon-950 rounded-lg p-3 mt-3 max-h-64 overflow-y-auto">{task.last_result}</pre>
						</div>
					{/if}
				</div>
				{/if}
			{/each}
		</div>
	{/if}
</div>

{/if}
