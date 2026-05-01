<script lang="ts">
	import { AlertCircle, FileText } from 'lucide-svelte';
	import type { TraceResult } from '$lib/api';

	type Props = {
		traces: TraceResult | null;
		selectedTraceIdx?: number;
		loading?: boolean;
		error?: string | null;
	};

	let {
		traces,
		selectedTraceIdx = $bindable(0),
		loading = false,
		error = null
	}: Props = $props();
</script>

<aside class="w-80 shrink-0 border-l border-bourbon-800 bg-bourbon-900/40 backdrop-blur-sm overflow-y-auto flex flex-col">
	<header class="shrink-0 h-11 px-4 flex items-center border-b border-bourbon-800">
		<span class="font-display text-xs font-bold uppercase tracking-widest text-run-500">
			traces
		</span>
	</header>

	{#if loading}
		<div class="flex items-center gap-2 px-4 py-4 text-bourbon-600">
			<div class="w-3 h-3 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
			<span class="text-[10px] font-mono">loading</span>
		</div>
	{:else if error}
		<div class="flex items-center gap-2 px-4 py-4 text-red-400">
			<AlertCircle size={12} />
			<span class="text-[10px] font-mono">{error}</span>
		</div>
	{:else if !traces}
		<div class="flex-1 flex flex-col items-center justify-center gap-2 px-6 py-8 text-center">
			<p class="text-bourbon-500 text-xs leading-relaxed">
				No traces yet for this snapshot.
			</p>
			<p class="text-[10px] text-bourbon-600 leading-relaxed">
				Run a Build (with traces) from the graphs index to generate them.
			</p>
		</div>
	{:else if traces.traces.length === 0}
		<div class="flex-1 flex flex-col items-center justify-center gap-2 px-6 py-8 text-center">
			<p class="text-bourbon-500 text-xs leading-relaxed">
				The last run produced no traces.
			</p>
			<p class="text-[10px] text-bourbon-600 leading-relaxed">
				This usually means the configured agent didn't actually read files. Re-run with a different agent or revise your guidance from the graphs index.
			</p>
		</div>
	{:else}
		<div class="flex flex-col">
			{#each traces.traces as trace, i}
				<button
					onclick={() => (selectedTraceIdx = i)}
					class="text-left px-4 py-3 border-b border-bourbon-800/40 transition-colors cursor-pointer
						{selectedTraceIdx === i
							? 'bg-bourbon-800/40 border-l-2 border-l-cmd-500'
							: 'hover:bg-bourbon-800/20'}"
				>
					<div class="flex items-start gap-2">
						<FileText size={11} class="text-bourbon-600 mt-1 shrink-0" />
						<div class="min-w-0">
							<div class="text-xs text-bourbon-200 leading-snug">{trace.name}</div>
							{#if trace.description}
								<div
									class="text-[10px] text-bourbon-500 leading-relaxed mt-1"
									class:line-clamp-3={selectedTraceIdx !== i}
								>
									{trace.description}
								</div>
							{/if}
						</div>
					</div>
				</button>
			{/each}
		</div>
	{/if}
</aside>
