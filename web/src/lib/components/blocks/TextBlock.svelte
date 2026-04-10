<script lang="ts">
	import type { TextBlock } from '$lib/blocks';

	let {
		block,
		onchange,
		onpaste,
		ontrigger
	}: {
		block: TextBlock;
		onchange: (content: string) => void;
		onpaste?: (e: ClipboardEvent) => void;
		ontrigger?: (type: string, query: string, rect: DOMRect) => void;
	} = $props();

	let textarea: HTMLTextAreaElement | undefined = $state(undefined);
	let localContent = $state('');

	// Sync from parent when block identity changes
	$effect(() => {
		localContent = block.content;
	});

	// Auto-resize textarea to fit content
	function resize() {
		if (textarea) {
			textarea.style.height = 'auto';
			textarea.style.height = textarea.scrollHeight + 'px';
		}
	}

	$effect(() => {
		void localContent;
		requestAnimationFrame(resize);
	});

	function handleInput() {
		onchange(localContent);
		checkAtTrigger();
	}

	function handlePaste(e: ClipboardEvent) {
		onpaste?.(e);
	}

	function checkAtTrigger() {
		if (!textarea || !ontrigger) return;
		const pos = textarea.selectionStart;
		const text = localContent.slice(0, pos);

		// Find @ not preceded by backtick
		const atIdx = text.lastIndexOf('@');
		if (atIdx < 0 || (atIdx > 0 && text[atIdx - 1] === '`')) {
			ontrigger('dismiss', '', textarea.getBoundingClientRect());
			return;
		}

		const query = text.slice(atIdx + 1);
		if (query.length < 3 || /\s/.test(query)) {
			ontrigger('dismiss', '', textarea.getBoundingClientRect());
			return;
		}

		const cursorRect = getCursorRect(textarea, pos);
		ontrigger('file', query, cursorRect);
	}

	function getCursorRect(el: HTMLTextAreaElement, pos: number): DOMRect {
		const elRect = el.getBoundingClientRect();
		const style = getComputedStyle(el);

		// Count lines up to cursor position
		const textBefore = el.value.slice(0, pos);
		const lines = textBefore.split('\n');
		const lineHeight = parseFloat(style.lineHeight) || parseFloat(style.fontSize) * 1.5;
		const paddingTop = parseFloat(style.paddingTop) || 0;

		const cursorLine = lines.length - 1;
		const cursorY = elRect.top + paddingTop + (cursorLine * lineHeight) - el.scrollTop;

		return new DOMRect(elRect.left, cursorY + lineHeight, 0, lineHeight);
	}

	export function focus() {
		textarea?.focus();
	}

	export function insertAtCursor(text: string) {
		if (!textarea) return;
		const start = textarea.selectionStart;
		const end = textarea.selectionEnd;
		localContent = localContent.slice(0, start) + text + localContent.slice(end);
		onchange(localContent);
		requestAnimationFrame(() => {
			if (textarea) {
				textarea.selectionStart = textarea.selectionEnd = start + text.length;
			}
		});
	}
</script>

<textarea
	bind:this={textarea}
	bind:value={localContent}
	oninput={handleInput}
	onpaste={handlePaste}
	placeholder="Type here... Use @ to reference files"
	class="w-full bg-transparent text-sm text-bourbon-200 resize-none overflow-hidden focus:outline-none placeholder:text-bourbon-700 font-mono leading-relaxed select-text min-h-[2rem]"
	rows="1"
></textarea>
