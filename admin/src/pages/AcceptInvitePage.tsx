import { unwrap } from "@talanta/shared";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { AuthLayout } from "../components/AuthLayout";
import { AlertIcon, ClockIcon, LinkOffIcon, UserIcon } from "../components/icons";
import { PASSWORD_MAX, PASSWORD_MIN, PasswordStrength } from "../components/PasswordStrength";
import { Alert, Button, Card, PasswordField, Spinner, StateBlock, TextField } from "../components/ui";
import { api, type Schemas } from "../lib/api";
import { describeError, isCode, splitFieldErrors } from "../lib/errors";
import { formatDateTime, roleLabel } from "../lib/format";
import { useSession } from "../lib/session";

/** Reasons the link cannot be used. Each gets its own screen. */
type DeadLink = "invalid" | "expired" | "already-registered";

function deadLinkFor(error: unknown): DeadLink | null {
  if (isCode(error, "INVITATION_INVALID", "NOT_FOUND")) return "invalid";
  if (isCode(error, "INVITATION_EXPIRED")) return "expired";
  if (isCode(error, "EMAIL_ALREADY_REGISTERED")) return "already-registered";
  return null;
}

export function AcceptInvitePage() {
  const { token = "" } = useParams();

  const preview = useQuery({
    queryKey: ["invitation", token],
    queryFn: () => unwrap(api.GET("/auth/invitations/{token}", { params: { path: { token } } })),
    // The answer to "is this link good?" does not change by asking again.
    retry: false,
    staleTime: Infinity,
  });

  let body: ReactNode;
  if (preview.isPending) {
    body = (
      <Card className="auth__card">
        <div className="state">
          <Spinner label="Checking your invitation" />
          <p>Checking your invitation…</p>
        </div>
      </Card>
    );
  } else if (preview.isError) {
    const dead = deadLinkFor(preview.error);
    body = dead ? (
      <DeadLinkScreen reason={dead} />
    ) : (
      <Card className="auth__card">
        <StateBlock
          icon={<AlertIcon />}
          title="We could not check your invitation"
          action={
            <Button variant="secondary" onClick={() => preview.refetch()} loading={preview.isFetching}>
              Try again
            </Button>
          }
        >
          <p role="alert">{describeError(preview.error)}</p>
        </StateBlock>
      </Card>
    );
  } else {
    body = <AcceptForm token={token} invitation={preview.data} />;
  }

  return <AuthLayout>{body}</AuthLayout>;
}

function DeadLinkScreen({ reason, email }: { reason: DeadLink; email?: string }) {
  if (reason === "expired") {
    return (
      <Card className="auth__card">
        <StateBlock icon={<ClockIcon />} title="This invitation has expired">
          <p>
            Invitation links only work for a few days. Ask the person who invited you to send it again: they can do
            that from the Team page, and you will get a new link.
          </p>
        </StateBlock>
      </Card>
    );
  }
  if (reason === "already-registered") {
    return (
      <Card className="auth__card">
        <StateBlock
          icon={<UserIcon />}
          title="You already have an account"
          action={
            <Link className="btn btn--primary" to="/login">
              Go to sign in
            </Link>
          }
        >
          <p>
            An account for {email ? <strong>{email}</strong> : "this email"} was created after this invitation was sent,
            so the invitation is no longer needed. Sign in with that account instead.
          </p>
        </StateBlock>
      </Card>
    );
  }
  return (
    <Card className="auth__card">
      <StateBlock
        icon={<LinkOffIcon />}
        title="This invitation link does not work"
        action={
          <Link className="btn btn--secondary" to="/login">
            I already have an account
          </Link>
        }
      >
        <div className="stack" style={{ textAlign: "left" }}>
          <p>This usually means one of three things:</p>
          <ul style={{ margin: 0, paddingLeft: "1.125rem" }}>
            <li>
              <strong>It has already been used.</strong> If you set a password before, sign in instead.
            </li>
            <li>
              <strong>It was withdrawn or replaced.</strong> If you were sent a newer link, use that one.
            </li>
            <li>
              <strong>Part of the link is missing.</strong> Copy the whole link from the message and try again.
            </li>
          </ul>
          <p>If none of that helps, ask the person who invited you to send a new invitation.</p>
        </div>
      </StateBlock>
    </Card>
  );
}

function AcceptForm({ token, invitation }: { token: string; invitation: Schemas["InvitationPreview"] }) {
  const { signIn } = useSession();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  /** Local problems are held back until the first attempt, so nobody is told off mid-typing. */
  const [attempted, setAttempted] = useState(false);

  const accept = useMutation({
    mutationFn: (body: Schemas["AcceptInvitation"]) =>
      unwrap(api.POST("/auth/invitations/{token}/accept", { params: { path: { token } }, body })),
    onSuccess: (session) => {
      // The new account is an organiser or admin, so this lands on the home page. If the
      // server ever answered with another role, signIn refuses and /login explains why.
      navigate(signIn(session) ? "/" : "/login", { replace: true });
    },
  });

  const local = {
    name: name.trim() ? null : "Enter your name.",
    password:
      password.length < PASSWORD_MIN
        ? `Use at least ${PASSWORD_MIN} characters.`
        : password.length > PASSWORD_MAX
          ? `Use at most ${PASSWORD_MAX} characters.`
          : null,
    confirm: confirm !== password ? "The two passwords are not the same." : null,
  };

  function submit(event: FormEvent) {
    event.preventDefault();
    if (accept.isPending) return;
    setAttempted(true);
    if (local.name || local.password || local.confirm) return;
    accept.mutate({ name: name.trim(), password });
  }

  const dead = deadLinkFor(accept.error);
  if (dead) return <DeadLinkScreen reason={dead} email={invitation.email} />;

  const { fields, other } = splitFieldErrors(accept.error, ["name", "password"] as const);
  const formError = accept.error && !fields.name && !fields.password ? describeError(accept.error) : null;
  const clearServerError = () => {
    if (accept.isError) accept.reset();
  };
  // Say the passwords differ once the second one is as long as the first, not on every key.
  const showConfirmError = attempted || (confirm.length > 0 && confirm.length >= password.length);
  const confirmError = showConfirmError ? local.confirm : null;

  return (
    <Card className="auth__card">
      <div>
        <h1>Set up your account</h1>
        <p className="auth__lede">You have been invited to help run events at Talanta Stadium.</p>
      </div>

      <div className="invite-summary">
        <span>
          You've been invited as{" "}
          <span className="chip chip--plain chip--role">{roleLabel(invitation.role)}</span>
        </span>
        <span className="invite-summary__email">{invitation.email}</span>
      </div>

      {formError && (
        <Alert tone="error">
          {formError}
          {other.map((message) => (
            <p key={message}>{message}</p>
          ))}
        </Alert>
      )}

      <form className="stack" onSubmit={submit} noValidate>
        <TextField
          label="Your full name"
          name="name"
          autoComplete="name"
          maxLength={100}
          required
          autoFocus
          value={name}
          onChange={(event) => {
            setName(event.target.value);
            clearServerError();
          }}
          error={fields.name ?? (attempted ? local.name : null)}
          hint="Shown to the rest of the team."
        />
        {/* Lets a password manager file the new password under the right email. */}
        <input type="email" name="email" autoComplete="username" value={invitation.email} readOnly hidden />
        <PasswordField
          label="Choose a password"
          name="new-password"
          autoComplete="new-password"
          maxLength={PASSWORD_MAX}
          required
          value={password}
          onChange={(event) => {
            setPassword(event.target.value);
            clearServerError();
          }}
          error={fields.password ?? (attempted ? local.password : null)}
          hint={<PasswordStrength password={password} />}
        />
        <PasswordField
          label="Type the password again"
          name="confirm-password"
          autoComplete="new-password"
          maxLength={PASSWORD_MAX}
          required
          value={confirm}
          onChange={(event) => setConfirm(event.target.value)}
          error={confirmError}
          hint={confirm.length > 0 && !local.confirm ? "The passwords match." : undefined}
        />
        <Button type="submit" block loading={accept.isPending}>
          {accept.isPending ? "Creating your account…" : "Create account and sign in"}
        </Button>
      </form>

      <p className="small muted">
        This invitation expires on {formatDateTime(invitation.expires_at)} (Nairobi time) and can be used once.
      </p>
    </Card>
  );
}
