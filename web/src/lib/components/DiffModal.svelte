<script lang="ts">
	import { X, ExternalLink, Flag } from 'lucide-svelte';
	import type { GitCommit } from '$lib/api';

	let {
		commit,
		diff,
		format,
		files,
		loading,
		onclose,
		onflag
	}: {
		commit: GitCommit;
		diff: string | null;
		format: 'delta' | 'unified';
		files: string[];
		loading: boolean;
		onclose: () => void;
		onflag: () => void;
	} = $props();

	function shortSha(sha: string): string { return sha.slice(0, 7); }
	function firstLine(message: string): string { return message.split('\n')[0]; }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
	onclick={onclose}
	onkeydown={(e) => { if (e.key === 'Escape') onclose(); }}
	role="dialog"
	tabindex="-1"
>
	<div
		class="bg-bourbon-900 border border-bourbon-800 rounded-2xl w-[90vw] max-w-5xl max-h-[85vh] flex flex-col overflow-hidden"
		onclick={(e) => e.stopPropagation()}
	>
		<!-- Header -->
		<div class="flex items-center justify-between px-6 py-4 border-b border-bourbon-800 shrink-0">
			<div class="flex items-center gap-3 min-w-0">
				<button
					onclick={onflag}
					class="shrink-0 transition-colors cursor-pointer {commit.flagged ? 'text-run-400 hover:text-run-300' : 'text-bourbon-600 hover:text-run-400'}"
					title={commit.flagged ? 'Remove flag' : 'Flag for follow-up'}
				>
					<Flag size={14} fill={commit.flagged ? 'currentColor' : 'none'} />
				</button>
				<span class="font-mono text-sm text-cmd-400">{shortSha(commit.sha)}</span>
				<span class="text-bourbon-200 truncate">{firstLine(commit.message)}</span>
			</div>
			<div class="flex items-center gap-3 shrink-0">
				<span class="text-xs text-bourbon-500">{commit.author} &middot; {commit.repoName}</span>
				{#if commit.url}
					<a
						href={commit.url}
						target="_blank"
						rel="noopener"
						class="flex items-center gap-1 text-xs text-cmd-400 hover:text-cmd-300"
					>
						<ExternalLink size={10} />
						GitHub
					</a>
				{/if}
				<button
					onclick={onclose}
					class="text-bourbon-600 hover:text-bourbon-300 transition-colors cursor-pointer"
				>
					<X size={18} />
				</button>
			</div>
		</div>

		<!-- File jump -->
		{#if files.length > 1}
			<div class="flex items-center gap-2 px-6 py-2.5 border-b border-bourbon-800 shrink-0 bg-bourbon-950/50">
				<span class="text-xs text-bourbon-600">{files.length} files</span>
				<select
					onchange={(e) => {
						const idx = (e.target as HTMLSelectElement).value;
						if (idx !== '') {
							document.getElementById(`file-${idx}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
							(e.target as HTMLSelectElement).value = '';
						}
					}}
					class="bg-bourbon-900 border border-bourbon-700 rounded-md px-2 py-1 text-xs font-mono text-bourbon-300
						focus:outline-none focus:border-cmd-500 cursor-pointer"
				>
					<option value="">Jump to file...</option>
					{#each files as file, i}
						<option value={i}>{file}</option>
					{/each}
				</select>
			</div>
		{/if}

		<!-- Body -->
		<div class="overflow-auto flex-1" id="diff-body">
			{#if loading}
				<div class="flex items-center justify-center gap-2 py-12 text-bourbon-600">
					<div class="w-4 h-4 border-2 border-bourbon-700 border-t-cmd-500 rounded-full animate-spin"></div>
					<span class="text-sm">Loading diff...</span>
				</div>
			{:else if diff}
				{#if format === 'delta'}
					<pre class="text-xs leading-relaxed font-mono p-6 bg-bourbon-950 text-bourbon-400 min-w-fit">{@html diff}</pre>
				{:else}
					<pre class="text-xs leading-relaxed font-mono p-6 bg-bourbon-950 min-w-fit">{#each diff.split('\n') as line}<span class="{line.startsWith('+') && !line.startsWith('+++') ? 'text-green-400 bg-green-950/30' : line.startsWith('-') && !line.startsWith('---') ? 'text-red-400 bg-red-950/30' : line.startsWith('@@') ? 'text-cmd-400' : line.startsWith('diff ') ? 'text-bourbon-500 font-bold' : 'text-bourbon-500'}">{line}</span>
{/each}</pre>
				{/if}
			{/if}
		</div>
	</div>
</div>
