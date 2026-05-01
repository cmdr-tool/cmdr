let mermaidPromise: Promise<(typeof import('mermaid'))['default']> | null = null;
let mermaidInitialized = false;

async function getMermaid() {
	mermaidPromise ??= import('mermaid').then((mod) => mod.default);
	const mermaid = await mermaidPromise;
	if (!mermaidInitialized) {
		mermaid.initialize({
			startOnLoad: false,
			theme: 'dark',
			themeVariables: {
				primaryColor: '#332a1f',
				primaryTextColor: '#c4b5a2',
				primaryBorderColor: '#4a3d2e',
				lineColor: '#897562',
				secondaryColor: '#26215c',
				tertiaryColor: '#1a1510',
				fontFamily: 'Space Grotesk, sans-serif',
			},
		});
		mermaidInitialized = true;
	}
	return mermaid;
}

/**
 * Sweep document.body for stray scratch divs mermaid may have leaked
 * outside `container` on prior failed renders. Mermaid v11 appends
 * temporary nodes to body for rendering and doesn't always clean
 * them up on parse errors. Safe to call any time — only removes
 * elements with the well-known `[id^="dmermaid-"]` shape that we
 * generate ourselves (so we never touch user content).
 */
export function purgeMermaidStrays(): void {
	if (typeof document === 'undefined') return;
	for (const el of document.body.querySelectorAll('[id^="dmermaid-"], [id^="mermaid-"]')) {
		// Only remove if it's a direct child of body — anything inside
		// our app's DOM tree is the rendered diagram we want to keep.
		if (el.parentElement === document.body) el.remove();
	}
}

/** Render any mermaid placeholder divs within a container element. */
export async function renderMermaidBlocks(container: HTMLElement): Promise<void> {
	const blocks = container.querySelectorAll<HTMLElement>('.mermaid-block');
	if (blocks.length === 0) return;

	const mermaid = await getMermaid();
	for (const block of blocks) {
		const source = decodeURIComponent(block.dataset.mermaid ?? '');
		if (!source) continue;
		const id = `mermaid-${crypto.randomUUID().slice(0, 8)}`;
		try {
			// `parse` doesn't touch the DOM, so syntax-checking here avoids
			// the v11 leak where `render` appends an error <div> to body
			// and leaves it there when source can't parse.
			await mermaid.parse(source);
			const { svg } = await mermaid.render(id, source);
			block.innerHTML = svg;
			block.classList.add('mermaid-rendered');
		} catch {
			// Leave the raw source text visible. Remove any scratch
			// elements mermaid may have appended to body.
			document.getElementById(id)?.remove();
			document.getElementById(`d${id}`)?.remove();
		}
	}
}
