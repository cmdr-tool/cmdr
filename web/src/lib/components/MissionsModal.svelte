<script lang="ts">
	import { onMount } from 'svelte';
	import { X, ArrowRight, GitBranch, Loader2, CircleCheck, CircleX } from 'lucide-svelte';
	import { getDelegations, type Delegation } from '$lib/api';
	import { renderMarkdown } from '$lib/markdown';
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
</script>

<svelte:window onkeydown={handleKeydown} />

<!-- svelte-ignore a11y_no_static_element_interactions -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
	onmousedown={(e) => { if (e.target === e.currentTarget) onclose(); }}
	onkeydown={(e) => { if (e.key === 'Escape') onclose(); }}
	role="dialog"
	tabindex="-1"
>
<div class="w-full max-w-5xl max-h-[80vh] overflow-y-auto bg-bourbon-900 rounded-2xl border border-bourbon-800">
	<!-- Header -->
	<div class="sticky top-0 z-10 bg-bourbon-900 px-6 py-4 border-b border-bourbon-800 flex items-center justify-between">
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
					<div class="flex flex-col gap-3">
						{#each activeDelegations as d}
							<div class="border border-bourbon-800 rounded-lg overflow-hidden">
								<!-- Header bar -->
								<div class="flex items-center justify-between px-4 py-2.5 bg-bourbon-950/50">
									<div class="flex items-center gap-2">
										<Loader2 size={14} class="text-run-400 animate-spin" />
										<span class="text-xs font-mono text-bourbon-300">{d.delegationFrom}</span>
										<ArrowRight size={10} class="text-bourbon-600" />
										<span class="text-xs font-mono text-bourbon-300">{d.delegationTo}</span>
									</div>
									<div class="flex items-center gap-3">
										{#if d.branch}
											<span class="flex items-center gap-1 text-[10px] font-mono text-cmd-400">
												<GitBranch size={10} />
												{d.branch}
											</span>
										{/if}
										<span class="text-[10px] text-bourbon-700">{$timeAgo(d.createdAt)}</span>
									</div>
								</div>

								<!-- Leader ask -->
								{#if d.summary}
									<div class="px-4 py-3 border-t border-bourbon-800/50">
										<div class="flex items-start gap-2.5">
											<span class="font-mono text-[10px] text-run-500 font-bold shrink-0 min-w-16 pt-0.5">{d.delegationFrom}</span>
											<p class="text-sm text-bourbon-300">{d.summary}</p>
										</div>
									</div>
								{/if}

								<!-- Waiting indicator -->
								<div class="px-4 py-2.5 border-t border-bourbon-800/50 bg-bourbon-950/30">
									<div class="flex items-center gap-2.5">
										<span class="font-mono text-[10px] text-bourbon-600 font-bold shrink-0 min-w-16">{d.delegationTo}</span>
										<span class="text-xs text-bourbon-600 italic">working...</span>
									</div>
								</div>
							</div>
						{/each}
					</div>
				</div>
			{/if}

			<!-- Completed -->
			{#if completedDelegations.length > 0}
				<div>
					<h3 class="font-display text-xs font-bold uppercase tracking-widest text-bourbon-500 mb-3">Completed</h3>
					<div class="flex flex-col gap-3">
						{#each completedDelegations as d}
							<div class="border border-bourbon-800 rounded-lg overflow-hidden">
								<!-- Header bar -->
								<div class="flex items-center justify-between px-4 py-2.5 bg-bourbon-950/50">
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
									<div class="flex items-center gap-3">
										{#if d.branch}
											<span class="flex items-center gap-1 text-[10px] font-mono text-bourbon-600">
												<GitBranch size={10} />
												{d.branch}
											</span>
										{/if}
										<span class="text-[10px] text-bourbon-700">{$timeAgo(d.completedAt || d.createdAt)}</span>
									</div>
								</div>

								<!-- Leader ask -->
								{#if d.summary}
									<div class="px-4 py-3 border-t border-bourbon-800/50">
										<div class="flex items-start gap-2.5">
											<span class="font-mono text-[10px] text-run-500/70 font-bold shrink-0 min-w-16 pt-0.5">{d.delegationFrom}</span>
											<p class="text-sm text-bourbon-400">{d.summary}</p>
										</div>
									</div>
								{/if}

								<!-- Agent debrief -->
								{#if d.result}
									<div class="px-4 py-3 border-t border-bourbon-800/50 bg-bourbon-950/30">
										<div class="flex items-start gap-2.5">
											<span class="font-mono text-[10px] text-cmd-400/70 font-bold shrink-0 min-w-16 pt-0.5">{d.delegationTo}</span>
											<div class="text-sm text-bourbon-400 overflow-hidden
												prose prose-invert prose-sm max-w-none
												prose-headings:text-bourbon-300 prose-headings:font-display prose-headings:tracking-wider prose-headings:text-xs
												prose-p:text-bourbon-400 prose-p:my-1
												prose-strong:text-bourbon-300
												prose-code:text-run-400 prose-code:bg-bourbon-800/50 prose-code:px-1 prose-code:py-0.5 prose-code:rounded
												prose-pre:bg-bourbon-900 prose-pre:border prose-pre:border-bourbon-800
												prose-li:text-bourbon-400 prose-li:my-0.5
												prose-ul:my-1 prose-ol:my-1">
												{@html renderMarkdown(d.result)}
											</div>
										</div>
									</div>
								{:else if d.status === 'completed'}
									<div class="px-4 py-2.5 border-t border-bourbon-800/50 bg-bourbon-950/30">
										<div class="flex items-center gap-2.5">
											<span class="font-mono text-[10px] text-bourbon-600 font-bold shrink-0 min-w-16">{d.delegationTo}</span>
											<span class="text-xs text-bourbon-600 italic">completed (no debrief)</span>
										</div>
									</div>
								{/if}
							</div>
						{/each}
					</div>
				</div>
			{/if}
		{/if}
	</div>
</div>
</div>
