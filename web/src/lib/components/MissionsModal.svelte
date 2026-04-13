<script lang="ts">
	import { onMount } from 'svelte';
	import { X, ArrowRight, GitBranch, Loader2, CircleCheck, CircleX } from 'lucide-svelte';
	import { getDelegations, type Delegation } from '$lib/api';
	import { events } from '$lib/events';
	import { timeAgo } from '$lib/timeStore';

	let {
		squad,
		onclose
	}: {
		squad: string;
		onclose: () => void;
	} = $props();

	let delegations: Delegation[] = $state([]);
	let loading = $state(true);

	let activeDelegations = $derived(
		delegations.filter(d => d.status === 'running' || d.status === 'pending')
	);
	let completedDelegations = $derived(
		delegations.filter(d => d.status !== 'running' && d.status !== 'pending')
	);

	async function fetchDelegations() {
		try {
			delegations = await getDelegations(squad);
		} catch { /* silent */ }
		loading = false;
	}

	onMount(() => {
		fetchDelegations();
		const unsub = events.on('delegation:update', (evt) => {
			if (evt.squad === squad) fetchDelegations();
		});
		return unsub;
	});

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') {
			e.preventDefault();
			onclose();
		}
	}

	let expandedResults: Set<number> = $state(new Set());

	function toggleResult(id: number) {
		const next = new Set(expandedResults);
		if (next.has(id)) next.delete(id);
		else next.add(id);
		expandedResults = next;
	}
</script>

<svelte:window onkeydown={handleKeydown} />

<!-- svelte-ignore a11y_no_static_element_interactions -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<div class="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm" onclick={onclose}></div>

<!-- Panel -->
<div class="fixed top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 z-50 w-full max-w-5xl max-h-[80vh] overflow-y-auto bg-bourbon-900 rounded-2xl border border-bourbon-800">
	<!-- Header -->
	<div class="sticky top-0 z-10 bg-bourbon-900/95 backdrop-blur px-6 py-4 border-b border-bourbon-800 flex items-center justify-between">
		<h2 class="font-display text-sm text-run-500 uppercase tracking-widest">
			Squad Missions: {squad}
		</h2>
		<button
			onclick={onclose}
			class="text-bourbon-600 hover:text-bourbon-400 transition-colors cursor-pointer"
		>
			<X size={18} />
		</button>
	</div>

	<div class="px-6 py-5">
		{#if loading}
			<div class="flex items-center justify-center gap-3 text-bourbon-600 py-8">
				<div class="w-4 h-4 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
				<span class="font-display text-xs uppercase tracking-widest">Loading</span>
			</div>
		{:else if delegations.length === 0}
			<p class="text-bourbon-600 text-sm py-4 text-center">No missions yet for this squad.</p>
		{:else}
			<!-- Active -->
			{#if activeDelegations.length > 0}
				<div class="mb-6">
					<h3 class="font-display text-xs font-bold uppercase tracking-widest text-run-500 mb-3">Active</h3>
					<div class="flex flex-col gap-2">
						{#each activeDelegations as d}
							<div class="bg-bourbon-900/50 border border-bourbon-800 rounded-lg px-4 py-3">
								<div class="flex items-center justify-between">
									<div class="flex items-center gap-2">
										<Loader2 size={14} class="text-run-400 animate-spin" />
										<span class="text-xs font-mono text-bourbon-300">{d.delegationFrom}</span>
										<ArrowRight size={10} class="text-bourbon-600" />
										<span class="text-xs font-mono text-bourbon-300">{d.delegationTo}</span>
									</div>
									<span class="text-[10px] text-bourbon-700">{$timeAgo(d.createdAt)}</span>
								</div>
								{#if d.title || d.summary}
									<div class="mt-1.5 text-sm text-bourbon-400">{d.title || d.summary}</div>
								{/if}
								{#if d.branch}
									<div class="mt-1.5 flex items-center gap-1 text-[10px] font-mono text-cmd-400">
										<GitBranch size={10} />
										{d.branch}
									</div>
								{/if}
							</div>
						{/each}
					</div>
				</div>
			{/if}

			<!-- Completed -->
			{#if completedDelegations.length > 0}
				<div>
					<h3 class="font-display text-xs font-bold uppercase tracking-widest text-bourbon-500 mb-3">Completed</h3>
					<div class="flex flex-col gap-2">
						{#each completedDelegations as d}
							<div class="bg-bourbon-950/30 border border-bourbon-800 rounded-lg px-4 py-3">
								<div class="flex items-center justify-between">
									<div class="flex items-center gap-2">
										{#if d.status === 'failed'}
											<CircleX size={14} class="text-red-400" />
										{:else}
											<CircleCheck size={14} class="text-green-500/60" />
										{/if}
										<span class="text-xs font-mono text-bourbon-400">{d.delegationFrom}</span>
										<ArrowRight size={10} class="text-bourbon-700" />
										<span class="text-xs font-mono text-bourbon-400">{d.delegationTo}</span>
									</div>
									<span class="text-[10px] text-bourbon-700">{$timeAgo(d.completedAt || d.createdAt)}</span>
								</div>
								{#if d.title || d.summary}
									<div class="mt-1.5 text-sm text-bourbon-500">{d.title || d.summary}</div>
								{/if}
								{#if d.branch}
									<div class="mt-1 flex items-center gap-1 text-[10px] font-mono text-bourbon-600">
										<GitBranch size={10} />
										{d.branch}
									</div>
								{/if}
								{#if d.result}
									<button
										onclick={() => toggleResult(d.id)}
										class="mt-2 text-[10px] font-mono text-cmd-400 hover:text-cmd-300 cursor-pointer"
									>
										{expandedResults.has(d.id) ? 'hide result' : 'show result'}
									</button>
									{#if expandedResults.has(d.id)}
										<div class="mt-2 text-xs text-bourbon-400 bg-bourbon-900/50 border border-bourbon-800 rounded px-3 py-2 whitespace-pre-wrap font-mono">
											{d.result}
										</div>
									{/if}
								{/if}
							</div>
						{/each}
					</div>
				</div>
			{/if}
		{/if}
	</div>
</div>
