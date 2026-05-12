import { useState } from "react";

export function CopyButton({
  text,
  label = "Copy",
  className = "copy-btn",
}: {
  text: string;
  label?: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className={className}
      onClick={(e) => {
        e.stopPropagation();
        navigator.clipboard.writeText(text).then(
          () => {
            setCopied(true);
            window.setTimeout(() => setCopied(false), 1200);
          },
          () => {
            /* ignore */
          },
        );
      }}
      aria-label={label}
    >
      {copied ? "Copied" : label}
    </button>
  );
}
