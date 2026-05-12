import { useMemo } from "react";
import { diffFromEditInput } from "../../lib/diff";

interface Props {
  input: unknown;
}

export function DiffPreview({ input }: Props) {
  const hunks = useMemo(() => diffFromEditInput(input), [input]);
  if (hunks.length === 0) return null;

  const totalAdded = hunks.reduce((n, h) => n + h.added, 0);
  const totalRemoved = hunks.reduce((n, h) => n + h.removed, 0);

  return (
    <div className="diff-preview">
      <div className="diff-summary">
        <span className="diff-add">+{totalAdded}</span>
        <span className="diff-rem">−{totalRemoved}</span>
        {hunks.length > 1 && (
          <span className="diff-hunks">{hunks.length} hunks</span>
        )}
      </div>
      <div className="diff-body">
        {hunks.map((h, i) => (
          <div key={i} className="diff-hunk">
            {h.changes.map((c, j) => {
              // Split each Change into its lines so we can color line-by-line.
              const lines = c.value.replace(/\n$/, "").split("\n");
              const cls = c.added
                ? "diff-line add"
                : c.removed
                  ? "diff-line rem"
                  : "diff-line ctx";
              const sign = c.added ? "+" : c.removed ? "−" : " ";
              return lines.map((ln, k) => (
                <div key={`${j}-${k}`} className={cls}>
                  <span className="diff-sign">{sign}</span>
                  <span className="diff-text">{ln || " "}</span>
                </div>
              ));
            })}
          </div>
        ))}
      </div>
    </div>
  );
}
