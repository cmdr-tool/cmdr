import { marked } from 'marked';
import Prism from 'prismjs';

// Load common languages
import 'prismjs/components/prism-typescript';
import 'prismjs/components/prism-bash';
import 'prismjs/components/prism-json';
import 'prismjs/components/prism-go';
import 'prismjs/components/prism-sql';
import 'prismjs/components/prism-yaml';
import 'prismjs/components/prism-diff';
import 'prismjs/components/prism-markdown';

// Configure marked to use Prism for code blocks
marked.setOptions({
	renderer: Object.assign(new marked.Renderer(), {
		link({ href, text }: { href: string; text: string }) {
			return `<a href="${href}" target="_blank" rel="noopener noreferrer">${text}</a>`;
		},
		code({ text, lang }: { text: string; lang?: string }) {
			// Mermaid blocks: emit a placeholder div for client-side rendering
			if (lang === 'mermaid') {
				const escaped = text.replace(/</g, '&lt;').replace(/>/g, '&gt;');
				return `<div class="mermaid-block" data-mermaid="${encodeURIComponent(text)}">${escaped}</div>`;
			}
			const language = lang && Prism.languages[lang] ? lang : 'plaintext';
			const grammar = Prism.languages[language];
			const highlighted = grammar
				? Prism.highlight(text, grammar, language)
				: text.replace(/</g, '&lt;').replace(/>/g, '&gt;');
			return `<pre class="language-${language} group/pre relative"><code class="language-${language}">${highlighted}</code>`
				+ `<button onclick="let c=this.parentElement.querySelector('code').textContent;navigator.clipboard.writeText(c);this.textContent='copied!';setTimeout(()=>this.textContent='copy',1500)" class="absolute top-2 right-2 invisible group-hover/pre:visible text-[10px] font-mono text-bourbon-500 hover:text-bourbon-200 bg-bourbon-800 hover:bg-bourbon-700 border border-bourbon-700 px-2 py-0.5 rounded cursor-pointer transition-colors" aria-label="Copy code">copy</button>`
				+ `</pre>`;
		}
	})
});

/** Render markdown to HTML with syntax-highlighted code blocks. */
export function renderMarkdown(md: string): string {
	return marked(md, { breaks: true }) as string;
}

