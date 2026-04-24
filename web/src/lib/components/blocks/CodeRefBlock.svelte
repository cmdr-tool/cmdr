<script lang="ts">
	import { onMount } from 'svelte';
	import { FileCode } from 'lucide-svelte';
	import { getCodeSnippet, type CodeSnippet } from '$lib/api';
	import type { CodeRefBlock } from '$lib/blocks';
	import { ensurePrismLoaded, highlightCode } from '$lib/markdown';

	let {
		block,
		repoPath,
		onchange,
		ontrigger
	}: {
		block: CodeRefBlock;
		repoPath: string;
		onchange: (ref: string) => void;
		ontrigger?: (type: string, query: string, rect: DOMRect) => void;
	} = $props();

	let localRef = $state('');
	let input: HTMLInputElement | undefined = $state(undefined);
	let snippet = $state<CodeSnippet | null>(null);
	let snippetError = $state(false);
	let prismVersion = $state(0);

	onMount(() => {
		void ensurePrismLoaded().then(() => { prismVersion += 1; });
	});

	$effect(() => {
		localRef = block.ref;
	});

	// Debounced preview fetch when ref changes
	$effect(() => {
		const ref = block.ref;
		if (!ref || !repoPath) {
			snippet = null;
			snippetError = false;
			return;
		}

		const timer = setTimeout(async () => {
			const parsed = parseRef(ref);
			if (!parsed) { snippet = null; return; }

			try {
				snippet = await getCodeSnippet(repoPath, parsed.file, parsed.start, parsed.end);
				snippetError = false;
			} catch {
				snippet = null;
				snippetError = true;
			}
		}, 500);

		return () => clearTimeout(timer);
	});

	function parseRef(ref: string): { file: string; start?: number; end?: number } | null {
		// Parse "file:L10-L25" or "file:L10" or "file"
		const colonIdx = ref.indexOf(':L');
		if (colonIdx < 0) return { file: ref };

		const file = ref.slice(0, colonIdx);
		const lineSpec = ref.slice(colonIdx + 2); // after ":L"
		const dashIdx = lineSpec.indexOf('-L');
		if (dashIdx >= 0) {
			const start = parseInt(lineSpec.slice(0, dashIdx));
			const end = parseInt(lineSpec.slice(dashIdx + 2));
			if (!isNaN(start) && !isNaN(end)) return { file, start, end };
		}
		const start = parseInt(lineSpec);
		if (!isNaN(start)) return { file, start, end: Math.min(start + 20, start + 50) };

		return { file };
	}

	function handleInput() {
		onchange(localRef);
		checkTrigger();
	}

	function checkTrigger() {
		if (!input || !ontrigger) return;
		// Dismiss if we're in the line range portion (after :)
		if (localRef.includes(':')) {
			ontrigger('dismiss', '', input.getBoundingClientRect());
			return;
		}
		const query = localRef;
		if (query.length >= 3) {
			ontrigger('file', query, input.getBoundingClientRect());
		} else {
			ontrigger('dismiss', '', input.getBoundingClientRect());
		}
	}

	export function focus() {
		input?.focus();
	}

	export function setRef(ref: string) {
		localRef = ref;
		onchange(localRef);
	}

	const extLangMap: Record<string, string> = {
		js: 'javascript', ts: 'typescript', tsx: 'typescript', jsx: 'javascript',
		go: 'go', json: 'json', sh: 'bash', bash: 'bash', zsh: 'bash',
		sql: 'sql', yaml: 'yaml', yml: 'yaml', md: 'markdown', diff: 'diff',
		css: 'css', html: 'html', svelte: 'html', swift: 'javascript',
	};

	function highlightSnippet(code: string, file: string): string {
		prismVersion;
		const ext = file.split('.').pop()?.toLowerCase() ?? '';
		const lang = extLangMap[ext] ?? 'plaintext';
		return highlightCode(code, lang);
	}
</script>

<div class="rounded-lg border border-bourbon-800 overflow-hidden">
	<!-- Input row -->
	<div class="flex items-center gap-2 bg-bourbon-950 px-3 py-2">
		<span class="text-cmd-400 shrink-0"><FileCode size={14} /></span>
		<span class="text-cmd-400 text-sm font-mono shrink-0">@</span>
		<input
			bind:this={input}
			type="text"
			bind:value={localRef}
			oninput={handleInput}
			placeholder="path/to/file:L10-L25"
			class="flex-1 bg-transparent text-sm text-cmd-300 font-mono focus:outline-none placeholder:text-bourbon-700"
		/>
		{#if snippet}
			<span class="text-[9px] font-mono text-bourbon-600 shrink-0">
				{snippet.start}–{snippet.end} of {snippet.totalLines}
			</span>
		{:else if snippetError}
			<span class="text-[9px] font-mono text-red-400 shrink-0">not found</span>
		{/if}
	</div>

	<!-- Preview pane -->
	{#if snippet && snippet.lines.length > 0}
		<div class="max-h-48 overflow-y-auto bg-bourbon-900/50 border-t border-bourbon-800">
			<pre class="text-[11px] font-mono leading-relaxed"><code>{#each snippet.lines as line, i}<span class="inline-block w-10 text-right pr-3 text-bourbon-700 select-none">{snippet.start + i}</span><span class="text-bourbon-300">{@html highlightSnippet(line, snippet.file)}</span>
{/each}</code></pre>
		</div>
	{/if}
</div>
