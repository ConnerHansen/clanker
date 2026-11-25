// components/markdown-viewer/markdown-viewer.ts
import { marked } from 'marked';
import hljs from 'highlight.js';
// Optional: Import a specific theme for better visuals
import 'highlight.js/styles/github-dark.min.css';

export class MarkdownViewer {
  public async renderMarkdown(rawText: string, component: HTMLElement) {
    // 1. Parse Markdown to HTML
    // marked.parse() is async-safe and returns HTML string
    const html = await marked.parse(rawText);

    // 2. Set the raw HTML
    component.innerHTML = html;

    // 3. Apply syntax highlighting
    this.highlightCodeBlocks(component);

    // 4. Add other post-processing (like copy buttons)
    this.addCopyButtons(component);
  }

  /**
   * Traverses the rendered DOM to apply Highlight.js to code blocks.
   */
  private highlightCodeBlocks(component: HTMLElement): void {
    // Select all code blocks that are inside a <pre> tag
    // This is the standard structure for Markdown code blocks: <pre><code class="language-js">...</code></pre>
    const codeBlocks = component.querySelectorAll('pre code');

    codeBlocks.forEach(block => {
      // Highlight.js can automatically detect the language if no class is present,
      // but 'language-' class is standard.
      // If you want to force detection, remove the class first, but usually keeping it is safer.
      hljs.highlightElement(block as HTMLElement);
    });
  }

  private addCopyButtons(container: HTMLElement): void {
    // const codeBlocks = container.querySelectorAll('pre');
    // codeBlocks.forEach(block => {
    //   // Create copy button logic here if needed
    //   const button = document.createElement('button');
    //   button.className = 'markdown-viewer__copy-btn';
    //   button.textContent = 'Copy';
    //   button.onclick = () => {
    //     navigator.clipboard.writeText(block.textContent || '');
    //   };

    //   const pre = block.parentElement;
    //   if (pre) {
    //     pre.style.position = 'relative';
    //     pre.appendChild(button);
    //   }
    // });
    const pres = container.querySelectorAll('pre');
    pres.forEach(pre => {
      // Prevent attaching multiple buttons if renderMarkdown is called again
      if (pre.querySelector('.copy-btn')) return;

      // Capture the original code text before the button is appended
      const codeEl = pre.querySelector('code');
      const originalCodeText = codeEl?.textContent || pre.textContent || '';

      const btn = document.createElement('button');
      btn.className = 'copy-btn';
      btn.title = 'Copy code';
      btn.innerHTML = '<i class="mdi mdi-content-copy"></i>';

      btn.addEventListener('click', async (e) => {
        e.stopPropagation();
        try {
          await navigator.clipboard.writeText(originalCodeText);
          btn.classList.add('copied');
          btn.innerHTML = '<i class="mdi mdi-check"></i>';
          setTimeout(() => {
            btn.classList.remove('copied');
            btn.innerHTML = '<i class="mdi mdi-content-copy"></i>';
          }, 1500);
        } catch (err) {
          console.error('Failed to copy code:', err);
        }
      });

      pre.prepend(btn);
    });
  }
}
