import { useEffect, useId } from "react";
import { createPortal } from "react-dom";

export interface ModalProps {
  open: boolean;
  title: string;
  size?: "sm" | "md" | "lg";
  busy?: boolean;
  onClose: () => void;
  children: React.ReactNode;
}

export function Modal({
  open,
  title,
  size = "md",
  busy = false,
  onClose,
  children,
}: ModalProps) {
  const labelId = useId();

  useEffect(() => {
    if (!open) return;
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        e.preventDefault();
        if (!busy) onClose();
      }
    }
    window.addEventListener("keydown", onKey);
    const prev = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      window.removeEventListener("keydown", onKey);
      document.body.style.overflow = prev;
    };
  }, [open, busy, onClose]);

  if (!open) return null;

  return createPortal(
    <div
      className="modal-scrim"
      onClick={() => {
        if (!busy) onClose();
      }}
      role="presentation"
    >
      <div
        className={`modal modal-form modal-form-${size}`}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby={labelId}
      >
        <h3 id={labelId}>{title}</h3>
        {children}
      </div>
    </div>,
    document.body,
  );
}
