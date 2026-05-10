import { useEffect, useRef, useState } from "react";
import { apiJson } from "../api";
import type { ListSkillsResponse, SkillEntry } from "../types";

interface Props {
  sessionId: string;
  // The current input text. We only show suggestions when it starts with "/".
  value: string;
  onPick: (skillName: string) => void;
}

// Lazy + cached fetch of /v1/sessions/{id}/skills. Per R9 the manifest is
// frozen for the session's lifetime, so caching for the lifetime of the
// component is fine.
function useSessionSkills(sessionId: string): {
  skills: SkillEntry[] | null;
  error: string | null;
} {
  const [skills, setSkills] = useState<SkillEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    setSkills(null);
    setError(null);
    apiJson<ListSkillsResponse>(
      `/v1/sessions/${encodeURIComponent(sessionId)}/skills`,
    )
      .then((r) => {
        if (cancelled) return;
        setSkills(r.skills ?? []);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
    };
  }, [sessionId]);
  return { skills, error };
}

export function SkillAutocomplete({ sessionId, value, onPick }: Props) {
  const { skills } = useSessionSkills(sessionId);
  const [activeIndex, setActiveIndex] = useState(0);
  const ref = useRef<HTMLDivElement>(null);

  // Skill name is the first whitespace-delimited token after the leading "/".
  const isCommand = value.startsWith("/");
  const firstToken = value.slice(1).split(/\s+/)[0] ?? "";
  const showsAfterName = /\s/.test(value);

  const filtered: SkillEntry[] = (() => {
    if (!isCommand || showsAfterName || !skills) return [];
    const prefix = firstToken.toLowerCase();
    return skills
      .filter((s) => s.name.toLowerCase().startsWith(prefix))
      .slice(0, 8);
  })();

  // Reset active index whenever the filter set changes.
  useEffect(() => {
    setActiveIndex(0);
  }, [filtered.length, firstToken]);

  // Expose keyboard nav via a global handler attached by MessageInput.
  useEffect(() => {
    (window as unknown as { __skillNav?: unknown }).__skillNav = {
      hasItems: filtered.length > 0,
      next: () =>
        setActiveIndex((i) => (filtered.length === 0 ? 0 : (i + 1) % filtered.length)),
      prev: () =>
        setActiveIndex((i) =>
          filtered.length === 0 ? 0 : (i - 1 + filtered.length) % filtered.length,
        ),
      pick: () => {
        if (filtered.length > 0) onPick(filtered[activeIndex].name);
      },
    };
  }, [filtered, activeIndex, onPick]);

  if (filtered.length === 0) return null;

  return (
    <div className="skill-popover" ref={ref}>
      {filtered.map((s, i) => (
        <div
          key={s.name}
          className={`item ${i === activeIndex ? "active" : ""}`}
          onMouseDown={(e) => {
            // mousedown so we beat the textarea blur.
            e.preventDefault();
            onPick(s.name);
          }}
          onMouseEnter={() => setActiveIndex(i)}
        >
          /<strong>{s.name}</strong>
          {s.description && (
            <span className="skill-desc">— {s.description}</span>
          )}
          {s.overrides && (
            <span className="skill-desc"> · overrides built-in</span>
          )}
        </div>
      ))}
    </div>
  );
}

export interface SkillNav {
  hasItems: boolean;
  next: () => void;
  prev: () => void;
  pick: () => void;
}

export function getSkillNav(): SkillNav | null {
  const w = window as unknown as { __skillNav?: SkillNav };
  return w.__skillNav ?? null;
}
