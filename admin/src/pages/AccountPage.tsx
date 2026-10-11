import { unwrap } from "@talanta/shared";
import { useMutation } from "@tanstack/react-query";
import { useState, type CSSProperties, type FormEvent } from "react";
import { ShieldIcon } from "../components/icons";
import { PASSWORD_MAX, PASSWORD_MIN, PasswordStrength } from "../components/PasswordStrength";
import { QrCode } from "../components/QrCode";
import { Alert, Button, Card, PasswordField, TextField } from "../components/ui";
import { api, type Schemas } from "../lib/api";
import { describeError, isCode, splitFieldErrors } from "../lib/errors";
import { formatDate, roleLabel } from "../lib/format";
import { useCurrentUser, useSession } from "../lib/session";

export function AccountPage() {
  const user = useCurrentUser();
  return (
    <div className="page page--narrow">
      <header className="page__header">
        <h1>Your account</h1>
        <p>Your sign-in details for the organiser site.</p>
      </header>

      <Card>
        <div className="card__body">
          <dl className="deflist">
            <dt>Name</dt>
            <dd>{user.name ?? <span className="muted">Not set</span>}</dd>
            <dt>Email</dt>
            <dd>{user.email ?? <span className="muted">Not set</span>}</dd>
            <dt>Role</dt>
            <dd>{roleLabel(user.role)}</dd>
            <dt>Joined</dt>
            <dd>
              <time dateTime={user.created_at}>{formatDate(user.created_at)}</time>
            </dd>
          </dl>
        </div>
      </Card>

      <ChangePasswordCard email={user.email} />
      <TwoStepCard />
    </div>
  );
}

function ChangePasswordCard({ email }: { email: string | null }) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [attempted, setAttempted] = useState(false);

  const change = useMutation({
    mutationFn: (body: Schemas["ChangePassword"]) => unwrap(api.POST("/me/password", { body })),
    onSuccess: () => {
      setCurrent("");
      setNext("");
      setConfirm("");
      setAttempted(false);
    },
  });

  const local = {
    current: current ? null : "Enter the password you use now.",
    next:
      next.length < PASSWORD_MIN
        ? `Use at least ${PASSWORD_MIN} characters.`
        : next === current
          ? "Choose a password that is different from the one you use now."
          : null,
    confirm: confirm !== next ? "The two passwords are not the same." : null,
  };

  function submit(event: FormEvent) {
    event.preventDefault();
    if (change.isPending) return;
    setAttempted(true);
    if (local.current || local.next || local.confirm) return;
    change.mutate({ current_password: current, new_password: next });
  }

  const { fields, other } = splitFieldErrors(change.error, ["current_password", "new_password"] as const);
  const wrongCurrent = isCode(change.error, "PASSWORD_INCORRECT")
    ? "That is not your current password. Check it and try again."
    : null;
  const formError =
    change.error && !wrongCurrent && !fields.current_password && !fields.new_password
      ? describeError(change.error)
      : null;
  // Typing again clears the last answer, both the error and the "changed" note.
  const clearResult = () => {
    if (!change.isIdle && !change.isPending) change.reset();
  };
  const showConfirmError = attempted || (confirm.length > 0 && confirm.length >= next.length);

  return (
    <Card>
      <div className="card__header">
        <h2>Change password</h2>
      </div>
      <form className="card__body stack" onSubmit={submit} noValidate>
        {change.isSuccess && (
          <Alert tone="success" title="Password changed">
            Use the new password next time you sign in. Any other device that was signed in to this account has been
            signed out.
          </Alert>
        )}
        {formError && (
          <Alert tone="error">
            {formError}
            {other.map((message) => (
              <p key={message}>{message}</p>
            ))}
          </Alert>
        )}
        <input type="email" name="email" autoComplete="username" value={email ?? ""} readOnly hidden />
        <PasswordField
          label="Current password"
          name="current-password"
          autoComplete="current-password"
          required
          value={current}
          onChange={(event) => {
            setCurrent(event.target.value);
            clearResult();
          }}
          error={wrongCurrent ?? fields.current_password ?? (attempted ? local.current : null)}
        />
        <PasswordField
          label="New password"
          name="new-password"
          autoComplete="new-password"
          maxLength={PASSWORD_MAX}
          required
          value={next}
          onChange={(event) => {
            setNext(event.target.value);
            clearResult();
          }}
          error={fields.new_password ?? (attempted ? local.next : null)}
          hint={<PasswordStrength password={next} />}
        />
        <PasswordField
          label="Type the new password again"
          name="confirm-password"
          autoComplete="new-password"
          maxLength={PASSWORD_MAX}
          required
          value={confirm}
          onChange={(event) => setConfirm(event.target.value)}
          error={showConfirmError ? local.confirm : null}
        />
        <div className="form-actions">
          <Button type="submit" loading={change.isPending}>
            {change.isPending ? "Changing…" : "Change password"}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function TwoStepCard() {
  const user = useCurrentUser();
  return (
    <Card>
      <div className="card__header">
        <h2>Two-step sign-in</h2>
        <span className={`chip ${user.totp_enabled ? "chip--accepted" : "chip--expired"}`}>
          {user.totp_enabled ? "On" : "Off"}
        </span>
      </div>
      <div className="card__body stack">
        <p className="muted">
          Optional. With it on, signing in also asks for a 6-digit code from an authenticator app on your phone, so a
          stolen password is not enough to get in.
        </p>
        {user.totp_enabled ? <TurnOffTwoStep /> : <TurnOnTwoStep />}
      </div>
    </Card>
  );
}

function TurnOnTwoStep() {
  const { setUser } = useSession();
  const [code, setCode] = useState("");

  const setup = useMutation({
    mutationFn: () => unwrap(api.POST("/me/totp/setup")),
    onSuccess: () => setCode(""),
  });

  const enable = useMutation({
    mutationFn: (body: Schemas["TotpCode"]) => unwrap(api.POST("/me/totp/enable", { body })),
    onSuccess: (user) => setUser(user),
    onError: (error) => {
      // The secret on screen is no longer the one the server holds: start over.
      if (isCode(error, "TOTP_SETUP_REQUIRED")) setup.reset();
    },
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    if (enable.isPending || code.length !== 6) return;
    enable.mutate({ code });
  }

  if (!setup.data) {
    return (
      <>
        {setup.error && (
          <Alert tone="error">
            {describeError(setup.error, {
              TOTP_ALREADY_ENABLED: "Two-step sign-in is already on for this account. Reload the page to see it.",
            })}
          </Alert>
        )}
        {isCode(enable.error, "TOTP_SETUP_REQUIRED") && (
          <Alert tone="warning">That setup ran out. Start again to get a new code to scan.</Alert>
        )}
        <div className="form-actions">
          <Button variant="secondary" onClick={() => setup.mutate()} loading={setup.isPending}>
            <ShieldIcon />
            {setup.isPending ? "Getting ready…" : "Set up two-step sign-in"}
          </Button>
        </div>
      </>
    );
  }

  const { fields } = splitFieldErrors(enable.error, ["code"] as const);
  const codeError = isCode(enable.error, "TOTP_CODE_INCORRECT")
    ? "That code does not match. Wait for the next code in your app and try again."
    : fields.code;
  const formError = enable.error && !codeError ? describeError(enable.error) : null;

  return (
    <div className="stack">
      <div className="totp-setup">
        <QrCode value={setup.data.otpauth_url} label="QR code to scan with your authenticator app" />
        <ol className="stack small" style={{ margin: 0, paddingLeft: "1.25rem", "--gap": "0.625rem" } as CSSProperties}>
          <li>
            Open an authenticator app on your phone (Google Authenticator, Microsoft Authenticator, Authy or similar) and
            scan this code.
          </li>
          <li>
            Cannot scan? Type this key into the app instead:
            <br />
            <code className="secret">{setup.data.secret.replace(/(.{4})/g, "$1 ").trim()}</code>
          </li>
          <li>Type the 6-digit code the app shows below. Two-step sign-in turns on only once that code is right.</li>
        </ol>
      </div>

      {formError && <Alert tone="error">{formError}</Alert>}

      <form className="stack" onSubmit={submit} noValidate style={{ maxWidth: "20rem" }}>
        <TextField
          label="6-digit code from the app"
          name="code"
          className="input--code"
          inputMode="numeric"
          autoComplete="one-time-code"
          maxLength={6}
          required
          value={code}
          onChange={(event) => {
            setCode(event.target.value.replace(/\D/g, "").slice(0, 6));
            if (enable.isError) enable.reset();
          }}
          error={codeError}
        />
        <div className="form-actions">
          <Button type="submit" loading={enable.isPending} disabled={code.length !== 6}>
            {enable.isPending ? "Checking…" : "Turn on"}
          </Button>
          <Button
            variant="ghost"
            onClick={() => {
              setup.reset();
              enable.reset();
            }}
            disabled={enable.isPending}
          >
            Cancel
          </Button>
        </div>
      </form>
    </div>
  );
}

function TurnOffTwoStep() {
  const { setUser } = useSession();
  const [open, setOpen] = useState(false);
  const [password, setPassword] = useState("");

  const disable = useMutation({
    mutationFn: (body: Schemas["PasswordConfirm"]) => unwrap(api.POST("/me/totp/disable", { body })),
    onSuccess: (user) => setUser(user),
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    if (disable.isPending || !password) return;
    disable.mutate({ password });
  }

  if (!open) {
    return (
      <div className="form-actions">
        <Button variant="secondary" onClick={() => setOpen(true)}>
          Turn off two-step sign-in
        </Button>
      </div>
    );
  }

  const { fields } = splitFieldErrors(disable.error, ["password"] as const);
  const passwordError = isCode(disable.error, "PASSWORD_INCORRECT")
    ? "That is not your password. Check it and try again."
    : fields.password;
  const formError = disable.error && !passwordError ? describeError(disable.error) : null;

  return (
    <form className="stack" onSubmit={submit} noValidate style={{ maxWidth: "24rem" }}>
      <Alert tone="warning">
        After this, your password alone will be enough to sign in. To use a different phone, turn it off and set it up
        again.
      </Alert>
      {formError && <Alert tone="error">{formError}</Alert>}
      <PasswordField
        label="Your password, to confirm"
        name="current-password"
        autoComplete="current-password"
        required
        autoFocus
        value={password}
        onChange={(event) => {
          setPassword(event.target.value);
          if (disable.isError) disable.reset();
        }}
        error={passwordError}
      />
      <div className="form-actions">
        <Button type="submit" variant="danger" loading={disable.isPending} disabled={!password}>
          {disable.isPending ? "Turning off…" : "Turn off"}
        </Button>
        <Button variant="ghost" onClick={() => setOpen(false)} disabled={disable.isPending}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
