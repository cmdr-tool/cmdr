import { marked } from 'marked';

type PrismModule = typeof import('prismjs');

let prism: PrismModule | null = null;
let prismPromise: Promise<PrismModule> | null = null;

function escapeHtml(text: string): string {
	return text.replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

async function loadPrism() {
	const prismModule = await import('prismjs');
	await Promise.all([
		import('prismjs/components/prism-typescript'),
		import('prismjs/components/prism-bash'),
		import('prismjs/components/prism-json'),
		import('prismjs/components/prism-go'),
		import('prismjs/components/prism-sql'),
		import('prismjs/components/prism-yaml'),
		import('prismjs/components/prism-diff'),
		import('prismjs/components/prism-markdown')
	]);
	prism = ((prismModule as unknown as { default?: PrismModule }).default ?? prismModule) as PrismModule;
	return prism;
}

export async function ensurePrismLoaded() {
	if (prism) return prism;
	prismPromise ??= loadPrism();
	return prismPromise;
}

export function highlightCode(code: string, lang?: string): string {
	const language = lang && prism?.languages[lang] ? lang : 'plaintext';
	const grammar = prism?.languages[language];
	return grammar ? prism!.highlight(code, grammar, language) : escapeHtml(code);
}

// Configure marked to use Prism for code blocks when available.
marked.setOptions({
	renderer: Object.assign(new marked.Renderer(), {
		link({ href, text }: { href: string; text: string }) {
			return `<a href="${href}" target="_blank" rel="noopener noreferrer">${text}</a>`;
		},
		code({ text, lang }: { text: string; lang?: string }) {
			// Mermaid blocks: emit a placeholder div for client-side rendering
			if (lang === 'mermaid') {
				return `<div class="mermaid-block" data-mermaid="${encodeURIComponent(text)}">${escapeHtml(text)}</div>`;
			}
			const language = lang && prism?.languages[lang] ? lang : 'plaintext';
			const highlighted = highlightCode(text, language);
			return `<pre class="language-${language} group/pre relative"><code class="language-${language}">${highlighted}</code>`
				+ `<button onclick="let c=this.parentElement.querySelector('code').textContent;navigator.clipboard.writeText(c);this.textContent='copied!';setTimeout(()=>this.textContent='copy',1500)" class="absolute top-2 right-2 invisible group-hover/pre:visible text-[10px] font-mono text-bourbon-500 hover:text-bourbon-200 bg-bourbon-800 hover:bg-bourbon-700 border border-bourbon-700 px-2 py-0.5 rounded cursor-pointer transition-colors" aria-label="Copy code">copy</button>`
				+ `</pre>`;
		}
	})
});

/** Render markdown to HTML with syntax-highlighted code blocks when Prism has loaded. */
export function renderMarkdown(md: string): string {
	return marked(md, { breaks: true }) as string;
}

