<script lang="ts">
	import { GitBranch, TriangleAlert } from 'lucide-svelte';
	import { pushRepo } from '$lib/api';
	import { playSound, SFX } from '$lib/sounds';
	import type { Snippet } from 'svelte';

	let {
		repoPath,
		action,
		onlaunched,
		children,
		disabled = false
	}: {
		repoPath: string;
		action: () => Promise<any>;
		onlaunched?: () => void;
		children: Snippet;
		disabled?: boolean;
	} = $props();

	let launching = $state(false);
	let unpushed = $state<number | null>(null);
	let pushing = $state(false);

	async function checkUnpushed(): Promise<boolean> {
		if (!repoPath) return false;
		try {
			const res = await fetch(`/api/repos/unpushed?repo=${encodeURIComponent(repoPath)}`);
			const data = await res.json();
			if (data.unpushed > 0) {
				unpushed = data.unpushed;
				return true;
			}
		} catch { /* treat as no unpushed */ }
		return false;
	}

	async function handleLaunch() {
		if (launching || disabled) return;
		launching = true;
		unpushed = null;

		if (await checkUnpushed()) {
			launching = false;
			return;
		}

		try {
			await action();
			playSound(SFX.dispatch, 0.5);
			onlaunched?.();
		} catch { /* action failed for non-unpushed reasons */ }
		launching = false;
	}

	async function handlePush() {
		if (!repoPath || pushing) return;
		pushing = true;
		try {
			await pushRepo(repoPath);
			unpushed = null;
		} catch { /* silent */ }
		pushing = false;
	}
</script>

{#if unpushed}
	<div class="flex items-center gap-4">
		<span class="text-[10px] font-mono text-red-400 flex items-center gap-1.5">
			<TriangleAlert size={12} class="text-red-500/50" />
			{unpushed} unpushed commit{unpushed !== 1 ? 's' : ''}
		</span>
		<button
			onclick={handlePush}
			disabled={pushing}
			class="flex items-center gap-1.5 text-[10px] font-mono text-run-400 hover:text-run-300 transition-colors cursor-pointer disabled:opacity-50"
		>
			<GitBranch size={14} />
			{pushing ? 'pushing...' : 'Push'}
		</button>
	</div>
{:else}
	<button
		onclick={handleLaunch}
		disabled={launching || disabled}
		class="flex items-center gap-1.5 text-[10px] font-mono text-cmd-400 hover:text-cmd-300 transition-colors cursor-pointer disabled:opacity-50"
	>
		{@render children()}
	</button>
{/if}
