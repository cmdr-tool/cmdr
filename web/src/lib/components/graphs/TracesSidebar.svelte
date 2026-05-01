<script lang="ts">
	import { AlertCircle, FileText, Plus, RefreshCw, Trash2, Loader2 } from 'lucide-svelte';
	import type { TraceRow } from '$lib/api';

	type Props = {
		traces: TraceRow[];
		selectedTraceId: number | null;
		loading?: boolean;
		error?: string | null;
		canGenerate?: boolean;
		onSelect: (id: number) => void;
		onCreate: () => void;
		onRegenerate: (id: number) => void;
		onDelete: (id: number) => void;
	};

	let {
		traces,
		selectedTraceId = $bindable(null),
		loading = false,
		error = null,
		canGenerate = true,
		onSelect,
		onCreate,
		onRegenerate,
		onDelete
	}: Props = $props();
</script>

<aside class="w-80 shrink-0 border-l border-bourbon-800 bg-bourbon-900/40 backdrop-blur-sm overflow-y-auto flex flex-col">
	<header class="shrink-0 h-11 px-4 flex items-center justify-between border-b border-bourbon-800">
		<span class="font-display text-xs font-bold uppercase tracking-widest text-run-500">
			traces
		</span>
		<button
			onclick={onCreate}
			disabled={!canGenerate}
			title={canGenerate ? 'Generate a new trace' : 'Build the graph first'}
			class="flex items-center gap-1 px-2 py-1 rounded-md
				text-[10px] font-display font-bold uppercase tracking-widest
				border backdrop-blur-sm transition-colors cursor-pointer
				{canGenerate
					? 'bg-cmd-700/40 border-cmd-600/30 text-cmd-400 hover:bg-cmd-700/60 hover:border-cmd-500/50 hover:text-cmd-300'
					: 'bg-bourbon-800/30 border-bourbon-800/40 text-bourbon-700 cursor-not-allowed'}"
		>
			<Plus size={11} />
			new
		</button>
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
	{:else if traces.length === 0}
		<div class="flex-1 flex flex-col items-center justify-center gap-3 px-6 py-8 text-center">
			<p class="text-bourbon-400 text-xs leading-relaxed">
				No traces yet.
			</p>
			<p class="text-[10px] text-bourbon-600 leading-relaxed">
				{canGenerate
					? 'Generate one to model a specific flow.'
					: 'Build the graph first to enable trace generation.'}
			</p>
			{#if canGenerate}
				<button
					onclick={onCreate}
					class="mt-2 flex items-center gap-1.5 px-3 py-1.5 rounded-md
						text-[10px] font-display font-bold uppercase tracking-widest
						bg-cmd-700/40 border border-cmd-600/30 text-cmd-400
						hover:bg-cmd-700/60 hover:border-cmd-500/50 hover:text-cmd-300
						transition-colors cursor-pointer"
				>
					<Plus size={11} />
					Generate trace
				</button>
			{/if}
		</div>
	{:else}
		<div class="flex flex-col">
			{#each traces as trace (trace.id)}
				{@const isSelected = selectedTraceId === trace.id}
				{@const isInFlight = trace.currentStatus === 'generating'}
				{@const isFailed = trace.currentStatus === 'failed'}
				<div
					class="border-b border-bourbon-800/40 transition-colors
						{isSelected
							? 'bg-bourbon-800/40 border-l-2 border-l-cmd-500'
							: 'hover:bg-bourbon-800/20'}"
				>
					<button
						onclick={() => onSelect(trace.id)}
						class="w-full text-left px-4 py-3 cursor-pointer"
					>
						<div class="flex items-start gap-2">
							{#if isInFlight}
								<Loader2 size={11} class="text-cmd-400 mt-1 shrink-0 animate-spin" />
							{:else if isFailed}
								<AlertCircle size={11} class="text-red-400 mt-1 shrink-0" />
							{:else}
								<FileText size={11} class="text-bourbon-600 mt-1 shrink-0" />
							{/if}
							<div class="min-w-0 flex-1">
								<div class="flex items-center gap-1.5">
									<div class="text-xs text-bourbon-200 leading-snug truncate flex-1">{trace.title}</div>
									{#if trace.stale}
										<span
											title="Code referenced by this trace has changed since it was generated. Regenerate to refresh."
											class="shrink-0 text-[8px] font-display font-bold uppercase tracking-widest
												px-1.5 py-0.5 rounded
												bg-run-700/30 border border-run-700/40 text-run-400"
										>
											stale
										</span>
									{/if}
								</div>
								<div class="text-[10px] text-bourbon-500 leading-relaxed mt-1 line-clamp-2">
									{trace.prompt}
								</div>
								{#if isFailed && trace.currentError}
									<div class="text-[10px] text-red-400/80 leading-relaxed mt-1 line-clamp-2 font-mono">
										{trace.currentError}
									</div>
								{/if}
							</div>
						</div>
					</button>
					{#if isSelected && !isInFlight}
						<div class="flex items-center gap-1 px-4 pb-3 -mt-1">
							<button
								onclick={() => onRegenerate(trace.id)}
								class="flex items-center gap-1 px-2 py-1 rounded
									text-[9px] font-display font-bold uppercase tracking-widest
									text-bourbon-400 hover:text-cmd-300 hover:bg-bourbon-800/60
									transition-colors cursor-pointer"
							>
								<RefreshCw size={10} />
								regenerate
							</button>
							<button
								onclick={() => onDelete(trace.id)}
								class="flex items-center gap-1 px-2 py-1 rounded
									text-[9px] font-display font-bold uppercase tracking-widest
									text-bourbon-500 hover:text-red-400 hover:bg-bourbon-800/60
									transition-colors cursor-pointer"
							>
								<Trash2 size={10} />
								delete
							</button>
						</div>
					{/if}
				</div>
			{/each}
		</div>
	{/if}
</aside>
