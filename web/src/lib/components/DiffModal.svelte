<script lang="ts">
	import { X, ExternalLink, Flag, Send, Trash2, Plus, Pencil } from 'lucide-svelte';
	import {
		getReviewComments,
		saveReviewComment,
		deleteReviewComment,
		submitReview,
		type GitCommit,
		type ReviewComment
	} from '$lib/api';

	let {
		commit,
		diff,
		format,
		files,
		loading,
		onclose,
		onflag,
		onsubmitreview,
		onclearreview
	}: {
		commit: GitCommit;
		diff: string | null;
		format: 'delta' | 'unified';
		files: string[];
		loading: boolean;
		onclose: () => void;
		onflag: () => void;
		onsubmitreview?: (taskId: number) => void;
		onclearreview?: () => void;
	} = $props();

	let comments = $state<ReviewComment[]>([]);
	let commentsLoaded = $state(false);
	let selectionStart: number | null = $state(null);
	let selectionEnd: number | null = $state(null);
	let activeCommentLine: number | null = $state(null);
	let commentDraft = $state('');
	let submitting = $state(false);
	let dragging = $state(false);

	let diffLines = $derived(diff ? diff.split('\n') : []);

	// Parse file line numbers from unified diff hunk headers
	interface LineInfo {
		oldNum: number | null;
		newNum: number | null;
	}

	let lineNumbers = $derived.by(() => {
		if (format === 'delta') return [] as LineInfo[];
		const result: LineInfo[] = [];
		let oldLine = 0;
		let newLine = 0;
		for (const line of diffLines) {
			const hunkMatch = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
			if (hunkMatch) {
				oldLine = parseInt(hunkMatch[1]);
				newLine = parseInt(hunkMatch[2]);
				result.push({ oldNum: null, newNum: null });
			} else if (line.startsWith('diff ') || line.startsWith('---') || line.startsWith('+++') || line.startsWith('index ')) {
				result.push({ oldNum: null, newNum: null });
			} else if (line.startsWith('-')) {
				result.push({ oldNum: oldLine, newNum: null });
				oldLine++;
			} else if (line.startsWith('+')) {
				result.push({ oldNum: null, newNum: newLine });
				newLine++;
			} else {
				result.push({ oldNum: oldLine, newNum: newLine });
				oldLine++;
				newLine++;
			}
		}
		return result;
	});

	let hasPendingInput = $derived(activeCommentLine !== null);

	$effect(() => {
		if (diff && !commentsLoaded) {
			getReviewComments(commit.repoPath, commit.sha)
				.then(c => { comments = c; })
				.catch(() => {})
				.finally(() => { commentsLoaded = true; });
		}
	});

	function getCommentAfterLine(idx: number): ReviewComment | null {
		return comments.find(c => c.lineEnd === idx + 1) ?? null;
	}

	function isLineSelected(idx: number): boolean {
		if (selectionStart === null) return false;
		const end = selectionEnd ?? selectionStart;
		const lo = Math.min(selectionStart, end);
		const hi = Math.max(selectionStart, end);
		return idx >= lo && idx <= hi;
	}

	function isLineCommented(idx: number): boolean {
		return comments.some(c => (idx + 1) >= c.lineStart && (idx + 1) <= c.lineEnd);
	}

	// --- Drag selection ---
	function handleGutterMouseDown(idx: number) {
		if (hasPendingInput) return; // Don't start new selection while editing
		selectionStart = idx;
		selectionEnd = idx;
		dragging = true;
		activeCommentLine = null;
			}

	function handleLineMouseEnter(idx: number) {
		if (dragging && selectionStart !== null) {
			selectionEnd = idx;
		}
	}

	function handleMouseUp() {
		if (dragging && selectionStart !== null) {
			dragging = false;
			const lo = Math.min(selectionStart, selectionEnd ?? selectionStart);
			const hi = Math.max(selectionStart, selectionEnd ?? selectionStart);
			selectionStart = lo;
			selectionEnd = hi;
			activeCommentLine = hi;
			const existing = comments.find(c => c.lineStart === lo + 1 && c.lineEnd === hi + 1);
			commentDraft = existing?.comment ?? '';
					}
	}

	function startEditComment(c: ReviewComment) {
		if (hasPendingInput) return; // Don't allow editing while another comment is open
		selectionStart = c.lineStart - 1;
		selectionEnd = c.lineEnd - 1;
		activeCommentLine = c.lineEnd - 1;
		commentDraft = c.comment;
			}

	function cancelComment() {
		selectionStart = null;
		selectionEnd = null;
		activeCommentLine = null;
				commentDraft = '';
	}

	async function saveComment() {
		if (!commentDraft.trim() || selectionStart === null) return;
		const lineStart = selectionStart + 1;
		const lineEnd = (selectionEnd ?? selectionStart) + 1;

		try {
			const { id } = await saveReviewComment({
				repoPath: commit.repoPath,
				sha: commit.sha,
				lineStart,
				lineEnd,
				comment: commentDraft.trim()
			});
			const newComment: ReviewComment = {
				id,
				repoPath: commit.repoPath,
				sha: commit.sha,
				lineStart,
				lineEnd,
				comment: commentDraft.trim(),
				createdAt: new Date().toISOString()
			};
			const existing = comments.findIndex(c => c.lineStart === lineStart && c.lineEnd === lineEnd);
			if (existing >= 0) {
				comments[existing] = newComment;
				comments = [...comments];
			} else {
				comments = [...comments, newComment];
			}
		} catch { /* silent */ }
		cancelComment();
	}

	async function removeComment(c: ReviewComment) {
		try {
			await deleteReviewComment(c.id);
			comments = comments.filter(x => x.id !== c.id);
		} catch { /* silent */ }
	}

	async function handleClearComments() {
		for (const c of comments) {
			try { await deleteReviewComment(c.id); } catch { /* silent */ }
		}
		comments = [];
		activeCommentLine = null;
		commentDraft = '';
		onclearreview?.();
	}

	async function handleSubmitReview() {
		submitting = true;
		try {
			const { id } = await submitReview(commit.repoPath, commit.sha);
			comments = [];
			onsubmitreview?.(id);
		} catch { /* silent */ }
		submitting = false;
	}

	function lineClass(line: string): string {
		if (format === 'delta') return '';
		if (line.startsWith('+') && !line.startsWith('+++')) return 'text-green-400 bg-green-950/30';
		if (line.startsWith('-') && !line.startsWith('---')) return 'text-red-400 bg-red-950/30';
		if (line.startsWith('@@')) return 'text-cmd-400';
		if (line.startsWith('diff ')) return 'text-bourbon-500 font-bold';
		return 'text-bourbon-500';
	}

	function autofocus(node: HTMLElement) {
		requestAnimationFrame(() => node.focus());
	}

	function shortSha(sha: string): string { return sha.slice(0, 7); }
	function firstLine(message: string): string { return message.split('\n')[0]; }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<!-- svelte-ignore a11y_click_events_have_key_events -->
<div
	class="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
	onmousedown={(e) => { if (e.target === e.currentTarget) onclose(); }}
	onkeydown={(e) => { if (e.key === 'Escape') { if (hasPendingInput) cancelComment(); else onclose(); }}}
	onmouseup={handleMouseUp}
	role="dialog"
	tabindex="-1"
>
	<div
		class="bg-bourbon-900 border border-bourbon-800 rounded-2xl w-[90vw] max-w-5xl max-h-[85vh] flex flex-col overflow-hidden"
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
		<div class="overflow-auto flex-1 select-none" id="diff-body">
			{#if loading}
				<div class="flex items-center justify-center gap-2 py-12 text-bourbon-600">
					<div class="w-4 h-4 border-2 border-bourbon-700 border-t-cmd-500 rounded-full animate-spin"></div>
					<span class="text-sm">Loading diff...</span>
				</div>
			{:else if diff}
				<div class="text-xs leading-relaxed font-mono bg-bourbon-950 min-w-fit">
					{#each diffLines as line, idx}
						{@const commented = isLineCommented(idx)}
						{@const selected = isLineSelected(idx)}
						<div
							class="flex group/line
								{selected ? 'bg-run-500/10' :
								 commented ? 'bg-cmd-500/5' :
								 'hover:bg-bourbon-900/50'}"
							onmouseenter={() => handleLineMouseEnter(idx)}
						>
							<!-- Gutter + Line numbers: sticky left -->
							<div class="sticky left-0 z-10 flex shrink-0 bg-bourbon-950">
								<div class="w-8 flex items-center justify-center border-r
									{selected ? 'border-r-run-500' :
									 commented ? 'border-r-cmd-500/50' :
									 'border-r-bourbon-800/50'}">
									{#if !commented}
									<button
										onmousedown={(e) => { e.preventDefault(); handleGutterMouseDown(idx); }}
										class="w-4 h-4 flex items-center justify-center rounded-sm
											bg-bourbon-800 text-bourbon-400 border border-bourbon-700
											hover:bg-run-600 hover:text-white hover:border-run-500
											cursor-pointer
											{hasPendingInput ? 'invisible' : 'invisible group-hover/line:visible'}"
									>
										<Plus size={12} strokeWidth={3} />
									</button>
								{/if}
								</div>
								{#if format !== 'delta'}
									{#if lineNumbers[idx]}
										<span class="w-10 text-right pr-1 text-[10px] leading-relaxed select-none self-center pt-px
											{lineNumbers[idx].oldNum && !lineNumbers[idx].newNum ? 'text-red-400/60' : 'text-bourbon-700'}">{lineNumbers[idx].oldNum ?? ''}</span>
										<span class="w-10 text-right pr-2 text-[10px] leading-relaxed select-none self-center pt-px
											{lineNumbers[idx].newNum && !lineNumbers[idx].oldNum ? 'text-green-400/60' : 'text-bourbon-700'}">{lineNumbers[idx].newNum ?? ''}</span>
									{:else}
										<span class="w-20"></span>
									{/if}
								{/if}
							</div>
							<!-- Content -->
							{#if format === 'delta'}
								<span class="flex-1 px-2 text-bourbon-400 select-text py-px whitespace-pre">{@html line}</span>
							{:else}
								<span class="flex-1 px-2 select-text py-px {lineClass(line)}">{line}</span>
							{/if}
						</div>

						<!-- Inline comment input (pending — amber/run scheme) -->
						{#if activeCommentLine === idx && !dragging}
							<div class="sticky left-8 z-20 border-l-3 border-l-run-500 bg-bourbon-900 ml-8 -translate-x-px w-[calc(min(90vw,64rem)-2rem)]">
								<textarea
									use:autofocus
									bind:value={commentDraft}
									placeholder="Add review comment..."
									class="w-full bg-transparent text-xs text-bourbon-200 px-4 py-3 resize-none focus:outline-none placeholder:text-bourbon-700 select-text"
									rows="2"
									onkeydown={(e) => { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) saveComment(); if (e.key === 'Escape') cancelComment(); }}
								></textarea>
								<div class="flex items-center justify-between px-4 py-1.5 border-t border-bourbon-800/50">
									<span class="text-[9px] text-bourbon-700">⌘+Enter to save</span>
									<div class="flex items-center gap-3">
										<button onclick={cancelComment} class="text-[10px] text-bourbon-600 hover:text-bourbon-400 cursor-pointer">cancel</button>
										<button onclick={saveComment} class="text-[10px] text-run-400 hover:text-run-300 cursor-pointer">save</button>
									</div>
								</div>
							</div>
						{/if}

						<!-- Persisted comment (saved — purple/cmd scheme) -->
						{@const existingComment = getCommentAfterLine(idx)}
						{#if existingComment && activeCommentLine !== idx}
							<div class="sticky left-8 z-20 flex items-center border-l-3 border-l-cmd-500/40 bg-cmd-500/5 ml-8 -translate-x-px px-4 py-2 w-[calc(min(90vw,64rem)-2rem)]">
								<span class="flex-1 text-xs text-bourbon-300 select-text">{existingComment.comment}</span>
								<button
									onclick={() => startEditComment(existingComment)}
									class="shrink-0 text-bourbon-700 hover:text-cmd-400 transition-colors cursor-pointer ml-3
										{hasPendingInput ? 'pointer-events-none opacity-30' : ''}"
									title="Edit comment"
								>
									<Pencil size={14} strokeWidth={2} />
								</button>
								<button
									onclick={() => removeComment(existingComment)}
									class="shrink-0 text-bourbon-700 hover:text-red-400 transition-colors cursor-pointer ml-2"
									title="Remove comment"
								>
									<Trash2 size={14} />
								</button>
							</div>
						{/if}
					{/each}
				</div>
			{/if}
		</div>

		<!-- Footer -->
		<div class="flex items-center justify-between px-6 py-3 border-t border-bourbon-800 shrink-0">
			<div class="flex items-center gap-3">
				{#if comments.length > 0}
					<span class="text-[10px] font-mono text-run-400">
						{comments.length} review comment{comments.length !== 1 ? 's' : ''}
					</span>
					<button
						onclick={handleClearComments}
						class="text-[10px] font-mono text-bourbon-600 hover:text-red-400 transition-colors cursor-pointer"
					>
						clear
					</button>
				{/if}
			</div>
			<div class="flex items-center gap-3">
				{#if submitting}
					<div class="flex items-center gap-2 text-bourbon-600">
						<div class="w-3 h-3 border-2 border-bourbon-700 border-t-run-500 rounded-full animate-spin"></div>
						<span class="text-[10px] font-mono">submitting review</span>
					</div>
				{:else}
					<button
						onclick={handleSubmitReview}
						class="flex items-center gap-1.5 text-xs text-run-400 hover:text-run-300 transition-colors cursor-pointer"
					>
						<Send size={12} />
						{comments.length > 0 ? 'Submit review' : 'Review'}
					</button>
				{/if}
			</div>
		</div>
	</div>
</div>
