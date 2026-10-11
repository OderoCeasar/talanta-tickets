import {
  useEffect,
  useId,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  type Ref,
  type SelectHTMLAttributes,
} from "react";
import { AlertIcon, CheckIcon, CopyIcon } from "./icons";

type ButtonVariant = "primary" | "secondary" | "danger" | "danger-solid" | "ghost";

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: "normal" | "small";
  block?: boolean;
  /** Shows a spinner and blocks the button while a request is running. */
  loading?: boolean;
}

export function Button({
  variant = "primary",
  size = "normal",
  block,
  loading,
  disabled,
  children,
  className,
  type = "button",
  ...rest
}: ButtonProps) {
  const classes = ["btn", `btn--${variant}`, size === "small" && "btn--small", block && "btn--block", className]
    .filter(Boolean)
    .join(" ");
  return (
    <button {...rest} type={type} className={classes} disabled={disabled || loading} aria-busy={loading || undefined}>
      {loading && <span className="spinner" aria-hidden="true" />}
      {children}
    </button>
  );
}

export function Spinner({ label = "Loading" }: { label?: string }) {
  return (
    <span role="status" className="row">
      <span className="spinner spinner--large" aria-hidden="true" />
      <span className="visually-hidden">{label}</span>
    </span>
  );
}

interface TextFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "id"> {
  label: string;
  /** Shown under the field in the error colour, and announced to screen readers. */
  error?: string | null;
  hint?: ReactNode;
  /** A button drawn inside the field's right edge (for example Show / Hide). */
  action?: ReactNode;
  inputRef?: Ref<HTMLInputElement>;
}

export function TextField({ label, error, hint, action, inputRef, className, ...input }: TextFieldProps) {
  const id = useId();
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [errorId, hintId].filter(Boolean).join(" ") || undefined;
  const classes = ["input", action && "input--with-action", className].filter(Boolean).join(" ");
  return (
    <div className="field">
      <label className="field__label" htmlFor={id}>
        {label}
      </label>
      <div className="field__control">
        <input
          {...input}
          ref={inputRef}
          id={id}
          className={classes}
          aria-invalid={error ? true : undefined}
          aria-describedby={describedBy}
        />
        {action}
      </div>
      {error && (
        <p className="field__error" id={errorId} role="alert">
          {error}
        </p>
      )}
      {hint && (
        <div className="field__hint" id={hintId}>
          {hint}
        </div>
      )}
    </div>
  );
}

interface SelectFieldProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, "id"> {
  label: string;
  error?: string | null;
  hint?: ReactNode;
}

export function SelectField({ label, error, hint, className, children, ...select }: SelectFieldProps) {
  const id = useId();
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  return (
    <div className="field">
      <label className="field__label" htmlFor={id}>
        {label}
      </label>
      <select
        {...select}
        id={id}
        className={["input", className].filter(Boolean).join(" ")}
        aria-invalid={error ? true : undefined}
        aria-describedby={[errorId, hintId].filter(Boolean).join(" ") || undefined}
      >
        {children}
      </select>
      {error && (
        <p className="field__error" id={errorId} role="alert">
          {error}
        </p>
      )}
      {hint && (
        <div className="field__hint" id={hintId}>
          {hint}
        </div>
      )}
    </div>
  );
}

/** A coloured square that stands for a ticket category. The colour comes from the category's data. */
export function Swatch({ color }: { color: string }) {
  return <span className="swatch" style={{ background: color }} aria-hidden="true" />;
}

/** A password field with a Show / Hide button. The button is reachable by keyboard. */
export function PasswordField(props: Omit<TextFieldProps, "type" | "action">) {
  const [visible, setVisible] = useState(false);
  return (
    <TextField
      {...props}
      type={visible ? "text" : "password"}
      autoCapitalize="none"
      autoCorrect="off"
      spellCheck={false}
      action={
        <button
          type="button"
          className="field__action"
          onClick={() => setVisible((v) => !v)}
          aria-pressed={visible}
          aria-label={visible ? `Hide ${props.label.toLowerCase()}` : `Show ${props.label.toLowerCase()}`}
        >
          {visible ? "Hide" : "Show"}
        </button>
      }
    />
  );
}

type AlertTone = "error" | "warning" | "success" | "info";

export function Alert({ tone, title, children }: { tone: AlertTone; title?: string; children?: ReactNode }) {
  return (
    // Errors interrupt a screen reader; the other tones wait their turn.
    <div className={`alert alert--${tone}`} role={tone === "error" ? "alert" : "status"}>
      {tone === "success" ? <CheckIcon /> : <AlertIcon />}
      <div className="alert__body">
        {title && <p className="alert__title">{title}</p>}
        {children}
      </div>
    </div>
  );
}

export function Card({ children, className }: { children: ReactNode; className?: string }) {
  return <section className={["card", className].filter(Boolean).join(" ")}>{children}</section>;
}

/** The middle of a card or page when there is nothing to list, or the list failed to load. */
export function StateBlock({
  icon,
  title,
  children,
  action,
}: {
  icon?: ReactNode;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="state">
      {icon && <div className="state__art">{icon}</div>}
      <p className="state__title">{title}</p>
      {children && <div>{children}</div>}
      {action}
    </div>
  );
}

export function LoadFailed({ message, onRetry, retrying }: { message: string; onRetry: () => void; retrying?: boolean }) {
  return (
    <StateBlock
      icon={<AlertIcon />}
      title="This did not load"
      action={
        <Button variant="secondary" size="small" onClick={onRetry} loading={retrying}>
          Try again
        </Button>
      }
    >
      <p role="alert">{message}</p>
    </StateBlock>
  );
}

export function SkeletonRows({ rows = 3, label }: { rows?: number; label: string }) {
  return (
    <div role="status" aria-label={label}>
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className="skeleton-row" style={{ width: `${82 - i * 14}%` }} />
      ))}
    </div>
  );
}

/** A read-only value with a Copy button. Falls back to selecting the text when the clipboard is blocked. */
export function CopyBox({ label, value }: { label: string; value: string }) {
  const id = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [status, setStatus] = useState<"idle" | "copied" | "manual">("idle");

  useEffect(() => setStatus("idle"), [value]);

  useEffect(() => {
    if (status !== "copied") return;
    const timer = window.setTimeout(() => setStatus("idle"), 2500);
    return () => window.clearTimeout(timer);
  }, [status]);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setStatus("copied");
    } catch {
      inputRef.current?.focus();
      inputRef.current?.select();
      setStatus("manual");
    }
  }

  return (
    <div className="field">
      <label className="field__label" htmlFor={id}>
        {label}
      </label>
      <div className="copybox">
        <input
          ref={inputRef}
          id={id}
          className="input"
          value={value}
          readOnly
          onFocus={(event) => event.currentTarget.select()}
        />
        <Button variant={status === "copied" ? "secondary" : "primary"} onClick={copy}>
          {status === "copied" ? <CheckIcon /> : <CopyIcon />}
          {status === "copied" ? "Copied" : "Copy"}
        </Button>
      </div>
      <p className="field__hint" role="status">
        {status === "copied" && "Copied. Paste it into a message to them."}
        {status === "manual" && "Your browser blocked copying. The text is selected: press Ctrl+C (or ⌘C) to copy it."}
      </p>
    </div>
  );
}

interface ConfirmDialogProps {
  open: boolean;
  title: string;
  children: ReactNode;
  confirmLabel: string;
  cancelLabel?: string;
  danger?: boolean;
  busy?: boolean;
  error?: string | null;
  onConfirm: () => void;
  onCancel: () => void;
}

/**
 * A yes/no question in a native <dialog>: the browser traps focus inside it, closes it on
 * Escape and gives focus back to the button that opened it.
 */
export function ConfirmDialog({
  open,
  title,
  children,
  confirmLabel,
  cancelLabel = "Cancel",
  danger,
  busy,
  error,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);

  return (
    <dialog
      ref={ref}
      className="dialog"
      aria-labelledby={titleId}
      onCancel={(event) => {
        event.preventDefault();
        if (!busy) onCancel();
      }}
    >
      <div className="dialog__body">
        <h2 id={titleId}>{title}</h2>
        <div className="muted">{children}</div>
        {error && <Alert tone="error">{error}</Alert>}
      </div>
      <div className="dialog__actions">
        {/* Cancel comes first so it has focus when the dialog opens: Enter never destroys by accident. */}
        <Button variant="secondary" onClick={onCancel} disabled={busy}>
          {cancelLabel}
        </Button>
        <Button variant={danger ? "danger-solid" : "primary"} onClick={onConfirm} loading={busy}>
          {confirmLabel}
        </Button>
      </div>
    </dialog>
  );
}

/** Counts a wait down to zero, one second at a time. `until` is a timestamp in milliseconds. */
export function useSecondsLeft(until: number | null): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (until === null) return;
    setNow(Date.now());
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [until]);
  return until === null ? 0 : Math.max(0, Math.ceil((until - now) / 1000));
}
