import { unwrap } from "@talanta/shared";
import { useMutation } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { Navigate, useLocation, useNavigate } from "react-router-dom";
import { AuthLayout } from "../components/AuthLayout";
import { Alert, Button, Card, PasswordField, Spinner, TextField, useSecondsLeft } from "../components/ui";
import { api, type Schemas } from "../lib/api";
import { describeError, isCode, splitFieldErrors } from "../lib/errors";
import { formatWait } from "../lib/format";
import { useSession, type SignedOutNotice } from "../lib/session";

const NOTICES: Record<SignedOutNotice, { title: string; text: string }> = {
  "wrong-role": {
    title: "This site is for organisers",
    text: "That account is for buying tickets or scanning at the gate, so it cannot be used here. You have been signed out. To get access, ask a site admin to invite you.",
  },
  expired: {
    title: "You were signed out",
    text: "Your session ended. Sign in again to carry on where you left off.",
  },
};

/** Where to go after signing in: the page that sent the user here, when there was one. */
function useReturnPath(): string {
  const location = useLocation();
  const from = (location.state as { from?: unknown } | null)?.from;
  // Only paths inside this site, never a full URL.
  if (typeof from === "string" && from.startsWith("/") && !from.startsWith("//") && !from.startsWith("/login")) {
    return from;
  }
  return "/";
}

export function LoginPage() {
  const { state, signIn, clearNotice } = useSession();
  const navigate = useNavigate();
  const returnPath = useReturnPath();

  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  /** Set once the password was right and the account also needs a code. */
  const [mfaToken, setMfaToken] = useState<string | null>(null);
  const [restartReason, setRestartReason] = useState<string | null>(null);
  const [blockedUntil, setBlockedUntil] = useState<number | null>(null);
  const secondsLeft = useSecondsLeft(blockedUntil);

  function noteRateLimit(error: unknown) {
    if (isCode(error, "RATE_LIMITED") && error.retryAfterSeconds !== null) {
      setBlockedUntil(Date.now() + error.retryAfterSeconds * 1000);
    }
  }

  function finish(session: Schemas["AuthSession"]) {
    // signIn refuses accounts that are not organisers or admins and leaves a notice behind.
    if (signIn(session)) navigate(returnPath, { replace: true });
    else {
      setMfaToken(null);
      setPassword("");
    }
  }

  const login = useMutation({
    mutationFn: (body: Schemas["PasswordLogin"]) => unwrap(api.POST("/auth/login", { body })),
    onSuccess: (result) => {
      if (result.mfa_required) setMfaToken(result.mfa_token);
      else finish(result);
    },
    onError: noteRateLimit,
  });

  function submitPassword(event: FormEvent) {
    event.preventDefault();
    if (login.isPending || secondsLeft > 0) return;
    clearNotice();
    setRestartReason(null);
    login.mutate({ email: email.trim(), password });
  }

  if (state.status === "signedIn") return <Navigate to={returnPath} replace />;
  if (state.status === "restoring") {
    return (
      <div className="fullscreen">
        <Spinner label="Checking whether you are signed in" />
      </div>
    );
  }

  if (mfaToken) {
    return (
      <AuthLayout>
        <CodeStep
          mfaToken={mfaToken}
          email={email.trim()}
          onSignedIn={finish}
          onStartAgain={(reason) => {
            setMfaToken(null);
            setPassword("");
            login.reset();
            setRestartReason(reason);
          }}
        />
      </AuthLayout>
    );
  }

  const notice = state.status === "signedOut" && state.notice ? NOTICES[state.notice] : null;
  const { fields, other } = splitFieldErrors(login.error, ["email", "password"] as const);
  const fieldProblem = isCode(login.error, "VALIDATION_FAILED") && Object.keys(fields).length > 0;
  const waiting = secondsLeft > 0;
  const formError =
    login.error && !fieldProblem && !(waiting && isCode(login.error, "RATE_LIMITED"))
      ? describeError(login.error, {
          INVALID_CREDENTIALS: "That email or password is not right. Check both and try again.",
        })
      : null;

  return (
    <AuthLayout>
      <Card className="auth__card">
        <div>
          <h1>Sign in</h1>
          <p className="auth__lede">Use the email and password for your organiser account.</p>
        </div>

        {state.status === "unreachable" && (
          <Alert tone="warning" title="We could not check your session">
            The server did not answer. You can sign in again once it is back.
          </Alert>
        )}
        {notice && (
          <Alert tone="warning" title={notice.title}>
            {notice.text}
          </Alert>
        )}
        {restartReason && <Alert tone="warning">{restartReason}</Alert>}
        {waiting && (
          <Alert tone="warning" title="Too many attempts">
            For safety, sign-in is paused. You can try again in {formatWait(secondsLeft)}.
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

        <form className="stack" onSubmit={submitPassword} noValidate>
          <TextField
            label="Email"
            name="email"
            type="email"
            autoComplete="username"
            inputMode="email"
            autoCapitalize="none"
            spellCheck={false}
            required
            autoFocus
            value={email}
            onChange={(event) => {
              setEmail(event.target.value);
              if (login.isError) login.reset();
            }}
            error={fields.email}
          />
          <PasswordField
            label="Password"
            name="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(event) => {
              setPassword(event.target.value);
              if (login.isError) login.reset();
            }}
            error={fields.password}
          />
          <Button type="submit" block loading={login.isPending} disabled={waiting || !email.trim() || !password}>
            {login.isPending ? "Signing in…" : waiting ? `Try again in ${formatWait(secondsLeft)}` : "Sign in"}
          </Button>
        </form>

        <p className="small muted">
          No account yet? Accounts are by invitation: ask a site admin to send you a link. Forgotten your password? A
          site admin can help you get back in.
        </p>
      </Card>
    </AuthLayout>
  );
}

function CodeStep({
  mfaToken,
  email,
  onSignedIn,
  onStartAgain,
}: {
  mfaToken: string;
  email: string;
  onSignedIn: (session: Schemas["AuthSession"]) => void;
  onStartAgain: (reason: string | null) => void;
}) {
  const [code, setCode] = useState("");
  const [blockedUntil, setBlockedUntil] = useState<number | null>(null);
  const secondsLeft = useSecondsLeft(blockedUntil);
  const inputRef = useRef<HTMLInputElement>(null);

  const verify = useMutation({
    mutationFn: (body: Schemas["TotpVerify"]) => unwrap(api.POST("/auth/totp/verify", { body })),
    onSuccess: onSignedIn,
    onError: (error) => {
      if (isCode(error, "MFA_TOKEN_INVALID")) {
        // The half-finished sign-in is dead (too slow, or too many wrong codes).
        onStartAgain("That took too long, or there were too many wrong codes. Enter your password again.");
        return;
      }
      if (isCode(error, "RATE_LIMITED") && error.retryAfterSeconds !== null) {
        setBlockedUntil(Date.now() + error.retryAfterSeconds * 1000);
      }
      setCode("");
      inputRef.current?.focus();
    },
  });

  useEffect(() => inputRef.current?.focus(), []);

  const { fields } = splitFieldErrors(verify.error, ["code"] as const);
  const waiting = secondsLeft > 0;
  const formError =
    verify.error && !fields.code && !isCode(verify.error, "MFA_TOKEN_INVALID")
      ? describeError(verify.error, {
          TOTP_INVALID: "That code is not right. Wait for the next code in your app and try again.",
        })
      : null;

  function submit(event: FormEvent) {
    event.preventDefault();
    if (verify.isPending || waiting || code.length !== 6) return;
    verify.mutate({ mfa_token: mfaToken, code });
  }

  return (
    <Card className="auth__card">
      <div>
        <h1>Enter your code</h1>
        <p className="auth__lede">
          Open your authenticator app and type the 6-digit code for Talanta Tickets{email ? ` (${email})` : ""}.
        </p>
      </div>

      {formError && <Alert tone="error">{formError}</Alert>}

      <form className="stack" onSubmit={submit} noValidate>
        <TextField
          label="6-digit code"
          name="code"
          className="input--code"
          inputMode="numeric"
          autoComplete="one-time-code"
          pattern="[0-9]{6}"
          maxLength={6}
          required
          value={code}
          onChange={(event) => {
            setCode(event.target.value.replace(/\D/g, "").slice(0, 6));
            if (verify.isError) verify.reset();
          }}
          error={fields.code}
          hint="The code changes every 30 seconds."
          inputRef={inputRef}
        />
        <Button type="submit" block loading={verify.isPending} disabled={waiting || code.length !== 6}>
          {verify.isPending ? "Checking…" : waiting ? `Try again in ${formatWait(secondsLeft)}` : "Confirm and sign in"}
        </Button>
      </form>

      <p className="small muted">
        Not you, or no phone to hand?{" "}
        <button type="button" className="link-button" onClick={() => onStartAgain(null)}>
          Go back to sign in
        </button>
      </p>
    </Card>
  );
}
