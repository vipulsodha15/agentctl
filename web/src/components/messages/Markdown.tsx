import { useState, type ReactNode } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeHighlight from "rehype-highlight";

interface Props {
  text: string;
}

// Markdown renderer used for assistant bubbles.
//
// - GFM (tables, strikethrough, task lists)
// - Syntax-highlighted fenced code via rehype-highlight (uses highlight.js)
// - Code blocks get a copy button
// - Inline code is styled in CSS (.markdown code)
//
// We intentionally do NOT pass `rehypeRaw` — raw HTML in assistant output is
// dropped to keep the surface XSS-safe.
export function Markdown({ text }: Props) {
  return (
    <div className="markdown">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeHighlight]}
        components={{
          pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
          a: ({ href, children }) => (
            <a href={href} target="_blank" rel="noreferrer noopener">
              {children}
            </a>
          ),
        }}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
}

function CodeBlock({ children }: { children: ReactNode }) {
  const [copied, setCopied] = useState(false);
  const onCopy = (e: React.MouseEvent) => {
    e.stopPropagation();
    const el = (e.currentTarget as HTMLElement)
      .closest(".code-block")
      ?.querySelector("code");
    const txt = el?.textContent ?? "";
    if (!txt) return;
    navigator.clipboard.writeText(txt).then(
      () => {
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1200);
      },
      () => {
        /* ignore */
      },
    );
  };
  return (
    <div className="code-block">
      <button
        type="button"
        className="copy-btn"
        onClick={onCopy}
        aria-label="Copy code"
      >
        {copied ? "Copied" : "Copy"}
      </button>
      <pre>{children}</pre>
    </div>
  );
}
