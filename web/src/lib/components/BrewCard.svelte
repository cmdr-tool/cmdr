<script lang="ts">
	import { onMount } from 'svelte';
	import { getBrewOutdated, brewUpgrade, type BrewOutdated } from '$lib/api';

	let data = $state<BrewOutdated | null>(null);
	let loaded = $state(false);
	let upgrading: string | null = $state(null);

	let total: number = $derived(data ? data.formulae.length + data.casks.length : 0);

	onMount(async () => {
		try { data = await getBrewOutdated(); } catch { /* silent */ }
		loaded = true;
	});

	async function handleUpgrade(formula?: string) {
		upgrading = formula ?? 'all';
		try {
			await brewUpgrade(formula);
			data = await getBrewOutdated();
		} catch { /* silent */ }
		upgrading = null;
	}

	export function isVisible(): boolean {
		return loaded && total > 0;
	}
</script>

{#if loaded && total > 0}
	<div class="bg-bourbon-900 rounded-2xl border border-bourbon-800 p-6">
		<div class="flex items-center gap-4 mb-4">
			<h2 class="font-display text-xs font-bold uppercase tracking-widest text-run-500">Homebrew</h2>
			<span class="text-xs font-medium text-run-400 bg-run-700/30 px-2.5 py-0.5 rounded-full">
				{total} update{total !== 1 ? 's' : ''}
			</span>
		</div>
		<div class="flex flex-col gap-1">
			{#each [...(data?.formulae ?? []), ...(data?.casks ?? [])] as pkg}
				<div class="group flex items-center gap-3 text-sm py-1">
					<span class="text-bourbon-100 font-mono text-xs">{pkg.name}</span>
					<span class="text-bourbon-600 font-mono text-[10px]">{pkg.installed_versions[0]}</span>
					<span class="text-bourbon-700 text-[10px]">→</span>
					<span class="text-run-400 font-mono text-[10px]">{pkg.current_version}</span>
					{#if upgrading === pkg.name}
						<div class="w-3 h-3 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin ml-auto"></div>
					{:else}
						<button
							onclick={() => handleUpgrade(pkg.name)}
							class="ml-auto text-[10px] font-mono text-bourbon-700 hover:text-run-400 transition-colors cursor-pointer opacity-0 group-hover:opacity-100"
						>upgrade</button>
					{/if}
				</div>
			{/each}
		</div>
		{#if total > 1}
			<div class="mt-3 pt-3 border-t border-bourbon-800">
				{#if upgrading === 'all'}
					<div class="flex items-center gap-2 text-bourbon-600">
						<div class="w-3 h-3 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
						<span class="text-[10px] font-mono">upgrading all</span>
					</div>
				{:else}
					<button
						onclick={() => handleUpgrade()}
						class="text-[10px] font-mono text-bourbon-600 hover:text-run-400 transition-colors cursor-pointer"
					>upgrade all ({total})</button>
				{/if}
			</div>
		{/if}
	</div>
{/if}
