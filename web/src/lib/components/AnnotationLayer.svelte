<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { computePosition, flip, shift, offset } from '@floating-ui/dom';
	import { Trash2 } from 'lucide-svelte';
	import { anchorFromSelection, findAnchorRange, type Annotation } from '$lib/annotations';

	let {
		containerEl,
		annotations,
		onchange,
	}: {
		containerEl: HTMLElement;
		annotations: Annotation[];
		onchange: (annotations: Annotation[]) => void;
	} = $props();

	let popover = $state<{
		mode: 'create' | 'edit';
		annotationId?: string;
		note: string;
	} | null>(null);

	let popoverEl: HTMLDivElement | undefined = $state(undefined);
	let pendingAnchor = $state<{ prefix: string; exact: string; suffix: string } | null>(null);
	let referenceRect = $state<DOMRect | null>(null);

	// Portal container — appended to document.body to escape overflow clipping
	let portalEl: HTMLDivElement;

	// Svelte action: moves the node to document.body on mount
	function portal(node: HTMLElement) {
		portalEl = document.createElement('div');
		document.body.appendChild(portalEl);
		portalEl.appendChild(node);
		return {
			destroy() { portalEl?.remove(); }
		};
	}

	// Virtual reference for Floating UI
	function virtualRef(rect: DOMRect) {
		return { getBoundingClientRect: () => rect };
	}

	// Position with Floating UI after popover renders
	$effect(() => {
		if (popover && popoverEl && referenceRect) {
			computePosition(virtualRef(referenceRect), popoverEl, {
				strategy: 'fixed',
				placement: 'bottom',
				middleware: [offset(8), flip(), shift({ padding: 8 })],
			}).then(({ x, y }) => {
				if (popoverEl) {
					popoverEl.style.left = `${x}px`;
					popoverEl.style.top = `${y}px`;
					popoverEl.style.visibility = 'visible';
				}
			});
		}
	});

	// --- Highlight management ---

	function clearHighlights() {
		// Unwrap innermost marks first to avoid issues with nested marks
		let marks = containerEl.querySelectorAll('mark[data-annotation-id]');
		while (marks.length > 0) {
			for (const mark of marks) {
				const parent = mark.parentNode;
				if (!parent) continue;
				while (mark.firstChild) {
					parent.insertBefore(mark.firstChild, mark);
				}
				parent.removeChild(mark);
			}
			marks = containerEl.querySelectorAll('mark[data-annotation-id]');
		}
		containerEl.normalize(); // merge adjacent text nodes once at the end
	}

	function applyHighlights() {
		clearHighlights();
		for (const ann of annotations) {
			const range = findAnchorRange(ann, containerEl);
			if (!range) continue;
			highlightRange(range, ann.id);
		}
	}

	function createMark(annotationId: string): HTMLElement {
		const mark = document.createElement('mark');
		mark.setAttribute('data-annotation-id', annotationId);
		mark.style.backgroundColor = 'color-mix(in srgb, var(--color-run-400) 40%, transparent)';
		mark.style.borderBottom = '1px solid color-mix(in srgb, var(--color-run-400) 60%, transparent)';
		mark.style.borderRadius = '2px';
		mark.style.cursor = 'pointer';
		mark.style.color = 'inherit';
		return mark;
	}

	function highlightRange(range: Range, annotationId: string) {
		// Collect text nodes within the range, then wrap each individually.
		// This avoids extractContents which destroys cross-element DOM structure.
		const textNodes: { node: Text; start: number; end: number }[] = [];
		const walker = document.createTreeWalker(
			range.commonAncestorContainer.nodeType === Node.TEXT_NODE
				? range.commonAncestorContainer.parentElement!
				: range.commonAncestorContainer,
			NodeFilter.SHOW_TEXT
		);

		while (walker.nextNode()) {
			const node = walker.currentNode as Text;
			if (!range.intersectsNode(node)) continue;

			let start = 0;
			let end = node.length;
			if (node === range.startContainer) start = range.startOffset;
			if (node === range.endContainer) end = range.endOffset;
			if (start < end) {
				textNodes.push({ node, start, end });
			}
		}

		// Wrap in reverse order to preserve offsets
		for (let i = textNodes.length - 1; i >= 0; i--) {
			const { node, start, end } = textNodes[i];
			const mark = createMark(annotationId);
			const selected = node.splitText(start);
			selected.splitText(end - start);
			selected.parentNode!.replaceChild(mark, selected);
			mark.appendChild(selected);
		}
	}

	// --- Interaction handling ---

	// Single pointerup handler covers both text selection and mark clicks.
	function handlePointerUp(e: PointerEvent) {
		// Use setTimeout(0) — fires after Svelte's microtask flush,
		// ensuring tick() inside positionPopover works correctly.
		setTimeout(async () => {
			if (popover?.mode === 'edit') return;

			const selection = window.getSelection();
			const hasSelection = selection && !selection.isCollapsed && selection.toString().trim();

			if (hasSelection) {
				// --- Text selection: open create popover ---
				const anchor = anchorFromSelection(selection, containerEl);
				if (!anchor) return;

				if (popover) closePopover();

				const range = selection.getRangeAt(0);
				referenceRect = range.getBoundingClientRect();
				pendingAnchor = anchor;

				clearPendingHighlight();
				highlightRange(range, '__pending__');

				popover = { mode: 'create', note: '' };
				window.getSelection()?.removeAllRanges();
			} else {
				// --- Click (no selection): check for annotation mark ---
				const target = e.target as HTMLElement;
				const mark = target.closest('mark[data-annotation-id]')
					?? target.querySelector('mark[data-annotation-id]');

				if (mark) {
					const id = mark.getAttribute('data-annotation-id');
					if (!id || id === '__pending__') {
						if (!popover) clearPendingHighlight();
						return;
					}
					const ann = annotations.find(a => a.id === id);
					if (!ann) return;

					if (popover) closePopover();
					referenceRect = mark.getBoundingClientRect();
					window.getSelection()?.removeAllRanges();

					popover = { mode: 'edit', annotationId: id, note: ann.note };
				} else {
					if (!popover) clearPendingHighlight();
				}
			}
		}, 0);
	}

	// --- Actions ---

	function saveAnnotation() {
		if (!popover) return;

		if (popover.mode === 'create' && pendingAnchor) {
			const newAnn: Annotation = {
				id: crypto.randomUUID(),
				...pendingAnchor,
				note: popover.note.trim(),
			};
			onchange([...annotations, newAnn]);
		} else if (popover.mode === 'edit' && popover.annotationId) {
			onchange(annotations.map(a =>
				a.id === popover!.annotationId ? { ...a, note: popover!.note.trim() } : a
			));
		}

		closePopover();
	}

	function deleteAnnotation() {
		if (!popover?.annotationId) return;
		onchange(annotations.filter(a => a.id !== popover!.annotationId));
		closePopover();
	}

	function clearPendingHighlight() {
		const marks = containerEl.querySelectorAll('mark[data-annotation-id="__pending__"]');
		for (const mark of marks) {
			const parent = mark.parentNode;
			if (!parent) continue;
			while (mark.firstChild) parent.insertBefore(mark.firstChild, mark);
			parent.removeChild(mark);
			parent.normalize();
		}
	}

	function closePopover() {
		clearPendingHighlight();
		popover = null;
		pendingAnchor = null;
		referenceRect = null;
	}

	function autofocus(node: HTMLElement) {
		requestAnimationFrame(() => node.focus());
	}

	// --- Lifecycle ---

	$effect(() => {
		// Re-apply highlights whenever annotations change
		// Access annotations.length to track the reactive dependency
		if (containerEl && annotations.length >= 0) {
			applyHighlights();
		}
	});

	onMount(() => {
		containerEl.addEventListener('pointerup', handlePointerUp);
	});

	onDestroy(() => {
		containerEl.removeEventListener('pointerup', handlePointerUp);
		clearHighlights();
	});
</script>

{#if popover}
	{@const selectedText = popover!.mode === 'edit'
		? annotations.find(a => a.id === popover!.annotationId)?.exact ?? ''
		: pendingAnchor?.exact ?? ''}
	<!-- svelte-ignore a11y_no_static_element_interactions -->
	<div
		use:portal
		class="fixed z-[100]"
		style="visibility:hidden"
		bind:this={popoverEl}
		onkeydown={(e) => {
			if (e.key === 'Escape') { e.stopPropagation(); closePopover(); }
			if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); saveAnnotation(); }
		}}
	>
		<div class="bg-bourbon-900 border border-bourbon-800 rounded-lg shadow-xl w-96">
			{#if selectedText}
				<div class="px-3 pt-2.5 pb-1">
					<div class="text-[10px] text-bourbon-300 italic line-clamp-3 border-l-2 border-run-500/40 pl-2">
						{selectedText}
					</div>
				</div>
			{/if}
			<textarea
				use:autofocus
				bind:value={popover.note}
				placeholder={popover.mode === 'create' ? 'Add a note about this selection...' : 'Edit note...'}
				class="w-full bg-transparent text-xs text-bourbon-200 px-3 py-2.5 resize-none focus:outline-none placeholder:text-bourbon-700 select-text"
				rows="3"
			></textarea>
			<div class="flex items-center justify-between px-3 py-1.5 border-t border-bourbon-800">
				<span class="text-[9px] text-bourbon-700">⌘+Enter to save</span>
				<div class="flex items-center gap-2">
					{#if popover.mode === 'edit'}
						<button
							onclick={deleteAnnotation}
							class="p-1 text-bourbon-600 hover:text-red-400 transition-colors cursor-pointer"
							title="Delete annotation"
						>
							<Trash2 size={12} />
						</button>
					{/if}
					<button
						onclick={closePopover}
						class="text-[10px] font-mono text-bourbon-600 hover:text-bourbon-400 cursor-pointer"
					>cancel</button>
					<button
						onclick={saveAnnotation}
						class="text-[10px] font-mono text-run-400 hover:text-run-300 cursor-pointer"
					>save</button>
				</div>
			</div>
		</div>
	</div>
{/if}
