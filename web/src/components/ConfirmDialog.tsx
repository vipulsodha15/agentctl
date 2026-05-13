import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";

type Variant = "danger" | "primary" | "accent";

interface ConfirmOptions {
  title: string;
  message?: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  variant?: Variant;
}

interface NotifyOptions {
  title: string;
  message?: ReactNode;
  dismissLabel?: string;
  variant?: "error" | "info";
}

interface DialogState {
  kind: "confirm" | "notify";
  title: string;
  message?: ReactNode;
  confirmLabel: string;
  cancelLabel?: string;
  variant: Variant;
  resolve: (value: boolean) => void;
}

interface ConfirmContextValue {
  confirm: (opts: ConfirmOptions) => Promise<boolean>;
  notify: (opts: NotifyOptions) => Promise<void>;
}

const ConfirmContext = createContext<ConfirmContextValue | null>(null);

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [dialog, setDialog] = useState<DialogState | null>(null);
  const confirmBtnRef = useRef<HTMLButtonElement>(null);

  const confirm = useCallback(
    (opts: ConfirmOptions) =>
      new Promise<boolean>((resolve) => {
        setDialog({
          kind: "confirm",
          title: opts.title,
          message: opts.message,
          confirmLabel: opts.confirmLabel ?? "Confirm",
          cancelLabel: opts.cancelLabel ?? "Cancel",
          variant: opts.variant ?? "primary",
          resolve,
        });
      }),
    [],
  );

  const notify = useCallback(
    (opts: NotifyOptions) =>
      new Promise<void>((resolve) => {
        setDialog({
          kind: "notify",
          title: opts.title,
          message: opts.message,
          confirmLabel: opts.dismissLabel ?? "OK",
          variant: opts.variant === "error" ? "danger" : "primary",
          resolve: () => resolve(),
        });
      }),
    [],
  );

  const close = useCallback(
    (value: boolean) => {
      if (!dialog) return;
      dialog.resolve(value);
      setDialog(null);
    },
    [dialog],
  );

  // Focus the primary action and wire up Escape/Enter shortcuts when open.
  useEffect(() => {
    if (!dialog) return;
    confirmBtnRef.current?.focus();
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        e.preventDefault();
        close(false);
      } else if (e.key === "Enter") {
        e.preventDefault();
        close(true);
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [dialog, close]);

  const buttonClass =
    dialog?.variant === "danger"
      ? "danger"
      : dialog?.variant === "accent"
        ? "accent"
        : "primary";

  return (
    <ConfirmContext.Provider value={{ confirm, notify }}>
      {children}
      {dialog && (
        <div
          className="modal-scrim"
          onClick={() => close(false)}
          role="presentation"
        >
          <div
            className="modal"
            onClick={(e) => e.stopPropagation()}
            role="alertdialog"
            aria-modal="true"
            aria-labelledby="confirm-dialog-title"
            aria-describedby={dialog.message ? "confirm-dialog-body" : undefined}
          >
            <h3 id="confirm-dialog-title">{dialog.title}</h3>
            {dialog.message != null && dialog.message !== "" && (
              <p id="confirm-dialog-body" className="muted">
                {dialog.message}
              </p>
            )}
            <div className="form-actions">
              {dialog.kind === "confirm" && (
                <button type="button" onClick={() => close(false)}>
                  {dialog.cancelLabel}
                </button>
              )}
              <button
                ref={confirmBtnRef}
                type="button"
                className={buttonClass}
                onClick={() => close(true)}
              >
                {dialog.confirmLabel}
              </button>
            </div>
          </div>
        </div>
      )}
    </ConfirmContext.Provider>
  );
}

export function useConfirm() {
  const ctx = useContext(ConfirmContext);
  if (!ctx) {
    throw new Error("useConfirm must be used within <ConfirmProvider>");
  }
  return ctx.confirm;
}

export function useNotify() {
  const ctx = useContext(ConfirmContext);
  if (!ctx) {
    throw new Error("useNotify must be used within <ConfirmProvider>");
  }
  return ctx.notify;
}
