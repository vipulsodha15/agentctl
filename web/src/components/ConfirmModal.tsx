import { useEffect, useRef } from "react";

export interface ConfirmModalProps {
  open: boolean;
  title: string;
  message?: React.ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  variant?: "danger" | "primary";
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmModal({
  open,
  title,
  message,
  confirmLabel = "Confirm",
  cancelLabel = "Cancel",
  variant = "danger",
  busy = false,
  onConfirm,
  onCancel,
}: ConfirmModalProps) {
  const confirmRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    const t = window.setTimeout(() => confirmRef.current?.focus(), 0);
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        e.preventDefault();
        if (!busy) onCancel();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => {
      window.clearTimeout(t);
      window.removeEventListener("keydown", onKey);
    };
  }, [open, busy, onCancel]);

  if (!open) return null;

  return (
    <div
      className="modal-scrim"
      onClick={() => {
        if (!busy) onCancel();
      }}
      role="presentation"
    >
      <div
        className="modal"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="confirm-modal-title"
      >
        <h3 id="confirm-modal-title">{title}</h3>
        {message && <p className="muted">{message}</p>}
        <div className="form-actions">
          <button type="button" onClick={onCancel} disabled={busy}>
            {cancelLabel}
          </button>
          <button
            ref={confirmRef}
            type="button"
            onClick={onConfirm}
            disabled={busy}
            className={variant}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
