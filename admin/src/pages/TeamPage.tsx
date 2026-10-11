import { unwrap } from "@talanta/shared";
import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState, type CSSProperties, type FormEvent } from "react";
import { MailIcon, TeamIcon } from "../components/icons";
import { Alert, Button, Card, ConfirmDialog, CopyBox, LoadFailed, SkeletonRows, StateBlock, TextField } from "../components/ui";
import { api, type Schemas, type StaffRole } from "../lib/api";
import { describeError, isCode, splitFieldErrors } from "../lib/errors";
import { formatDate, formatDateTime, roleLabel } from "../lib/format";
import { useCurrentUser } from "../lib/session";

type Invitation = Schemas["Invitation"];

const PAGE_SIZE = 50;
const USERS_KEY = ["admin", "users"] as const;
const INVITATIONS_KEY = ["admin", "invitations"] as const;

const ROLE_CHOICES: { value: StaffRole; title: string; text: string }[] = [
  { value: "ORGANISER", title: "Organiser", text: "Runs events, layout, prices and sales." },
  { value: "ADMIN", title: "Admin", text: "Everything an organiser can do, and can invite people." },
];

const STATUS_LABELS: Record<Invitation["status"], string> = {
  PENDING: "Pending",
  ACCEPTED: "Accepted",
  EXPIRED: "Expired",
  REVOKED: "Revoked",
};

/** A link the admin can copy right now. The server shows each link only once. */
interface FreshLink {
  invitationId: number;
  email: string;
  url: string | null;
  expiresAt: string;
  resent: boolean;
}

export function TeamPage() {
  const [freshLink, setFreshLink] = useState<FreshLink | null>(null);
  const linkRef = useRef<HTMLDivElement>(null);

  // The link panel appears above the lists; bring it into view and tell screen readers.
  useEffect(() => {
    if (freshLink) linkRef.current?.focus();
  }, [freshLink]);

  function showLink(invitation: Invitation, resent: boolean) {
    setFreshLink({
      invitationId: invitation.id,
      email: invitation.email,
      url: invitation.invite_url,
      expiresAt: invitation.expires_at,
      resent,
    });
  }

  return (
    <div className="page">
      <header className="page__header">
        <h1>Team</h1>
        <p>Invite the people who run events with you. Each person sets their own password from a one-time link.</p>
      </header>

      <InviteForm onInvited={(invitation) => showLink(invitation, false)} />

      {freshLink && (
        <div ref={linkRef} tabIndex={-1} className="linkpanel stack" style={{ outline: "none" }}>
          <LinkPanel link={freshLink} onDismiss={() => setFreshLink(null)} />
        </div>
      )}

      <InvitationsCard
        onResent={(invitation) => showLink(invitation, true)}
        onRevoked={(id) => setFreshLink((current) => (current?.invitationId === id ? null : current))}
      />
      <StaffCard />
    </div>
  );
}

function LinkPanel({ link, onDismiss }: { link: FreshLink; onDismiss: () => void }) {
  return (
    <>
      <div className="row" style={{ justifyContent: "space-between" }}>
        <h2>{link.resent ? `New link for ${link.email}` : `Invitation ready for ${link.email}`}</h2>
        <Button variant="ghost" size="small" onClick={onDismiss}>
          Done
        </Button>
      </div>
      {link.url ? (
        <>
          <CopyBox label="Invitation link" value={link.url} />
          <ul className="small muted" style={{ margin: 0, paddingLeft: "1.125rem" }}>
            <li>
              The link works once and expires on <strong>{formatDateTime(link.expiresAt)}</strong> (Nairobi time).
            </li>
            {link.resent && <li>The link you sent before no longer works.</li>}
            <li>It has also been emailed to them, if email is set up on this site. If not, send them this link yourself.</li>
            <li>
              <strong>Copy it now.</strong> For safety it cannot be shown again; if it is lost, use Resend to make a new
              one.
            </li>
          </ul>
        </>
      ) : (
        <Alert tone="warning">
          The invitation was saved, but the server did not send back a link. Use Resend in the list below to get one.
        </Alert>
      )}
    </>
  );
}

function InviteForm({ onInvited }: { onInvited: (invitation: Invitation) => void }) {
  const queryClient = useQueryClient();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<StaffRole>("ORGANISER");
  const [localError, setLocalError] = useState<string | null>(null);

  const invite = useMutation({
    mutationFn: (body: Schemas["CreateInvitation"]) => unwrap(api.POST("/admin/invitations", { body })),
    onSuccess: (invitation) => {
      setEmail("");
      setRole("ORGANISER");
      onInvited(invitation);
      return queryClient.invalidateQueries({ queryKey: INVITATIONS_KEY });
    },
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    if (invite.isPending) return;
    const trimmed = email.trim();
    // A light check so an obvious slip is caught without a round trip; the server decides.
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed)) {
      setLocalError("Enter the person's full email address, for example name@example.com.");
      return;
    }
    setLocalError(null);
    invite.mutate({ email: trimmed, role });
  }

  const { fields, other } = splitFieldErrors(invite.error, ["email", "role"] as const);
  // Both of these are about the address that was typed, so they belong next to it.
  const emailConflict = isCode(invite.error, "EMAIL_ALREADY_REGISTERED")
    ? "Someone with this email already has an account. They can sign in with it; there is no need to invite them."
    : isCode(invite.error, "INVITATION_ALREADY_PENDING")
      ? "This email already has an invitation that has not been used. Find it in the list below and use Resend for a new link, or Revoke it first."
      : null;
  const emailError = localError ?? fields.email ?? emailConflict;
  const formError =
    invite.error && !emailConflict && !fields.email && !fields.role ? describeError(invite.error) : null;

  return (
    <Card>
      <div className="card__header">
        <h2>Invite someone</h2>
      </div>
      <form className="card__body invite-form" onSubmit={submit} noValidate>
        <TextField
          label="Their email"
          name="email"
          type="email"
          inputMode="email"
          autoComplete="off"
          autoCapitalize="none"
          spellCheck={false}
          placeholder="name@example.com"
          maxLength={254}
          required
          value={email}
          onChange={(event) => {
            setEmail(event.target.value);
            setLocalError(null);
            if (invite.isError) invite.reset();
          }}
          error={emailError}
        />
        <div className="field">
          <fieldset className="choice-group" aria-describedby={fields.role ? "invite-role-error" : undefined}>
            <legend>What they can do</legend>
            {ROLE_CHOICES.map((choice) => (
              <label key={choice.value} className="choice">
                <input
                  type="radio"
                  name="role"
                  value={choice.value}
                  checked={role === choice.value}
                  onChange={() => setRole(choice.value)}
                />
                <span>
                  <span className="choice__title">{choice.title}</span>
                  <span className="choice__text">{choice.text}</span>
                </span>
              </label>
            ))}
          </fieldset>
          {fields.role && (
            <p className="field__error" id="invite-role-error" role="alert">
              {fields.role}
            </p>
          )}
        </div>
        <div className="invite-form__submit stack" style={{ "--gap": "0.75rem" } as CSSProperties}>
          {formError && (
            <Alert tone="error">
              {formError}
              {other.map((message) => (
                <p key={message}>{message}</p>
              ))}
            </Alert>
          )}
          <div className="form-actions">
            <Button type="submit" loading={invite.isPending} disabled={!email.trim()}>
              <MailIcon />
              {invite.isPending ? "Creating invitation…" : "Create invitation"}
            </Button>
            <span className="small muted">You will get a link to send them.</span>
          </div>
        </div>
      </form>
    </Card>
  );
}

function InvitationsCard({
  onResent,
  onRevoked,
}: {
  onResent: (invitation: Invitation) => void;
  onRevoked: (invitationId: number) => void;
}) {
  const queryClient = useQueryClient();
  const [toRevoke, setToRevoke] = useState<Invitation | null>(null);

  const invitations = useInfiniteQuery({
    queryKey: INVITATIONS_KEY,
    queryFn: ({ pageParam }) =>
      unwrap(api.GET("/admin/invitations", { params: { query: { limit: PAGE_SIZE, cursor: pageParam } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

  const resend = useMutation({
    mutationFn: (invitation: Invitation) =>
      unwrap(
        api.POST("/admin/invitations/{invitationId}/resend", { params: { path: { invitationId: invitation.id } } }),
      ),
    onSuccess: (invitation) => {
      onResent(invitation);
    },
    // Refetch either way: a failure usually means the list on screen is out of date.
    onSettled: () => queryClient.invalidateQueries({ queryKey: INVITATIONS_KEY }),
  });

  const revoke = useMutation({
    mutationFn: (invitation: Invitation) =>
      unwrap(api.DELETE("/admin/invitations/{invitationId}", { params: { path: { invitationId: invitation.id } } })),
    onSuccess: (_nothing, invitation) => {
      setToRevoke(null);
      onRevoked(invitation.id);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: INVITATIONS_KEY }),
  });

  const rows = invitations.data?.pages.flatMap((page) => page.items) ?? [];

  const resendError = resend.error
    ? describeError(resend.error, {
        INVITATION_NOT_PENDING: "This invitation has already been accepted or revoked, so it cannot be sent again.",
        EMAIL_ALREADY_REGISTERED: "Someone with this email already has an account, so the invitation is not needed.",
        NOT_FOUND: "This invitation no longer exists.",
      })
    : null;
  const revokeError = revoke.error
    ? describeError(revoke.error, {
        INVITATION_NOT_PENDING: "This invitation has already been accepted, so the account exists and it cannot be revoked.",
        NOT_FOUND: "This invitation no longer exists.",
      })
    : null;

  return (
    <Card>
      <div className="card__header">
        <h2>Invitations</h2>
        <span className="small muted">Times are Nairobi time</span>
      </div>

      {resendError && (
        <div className="card__body" style={{ paddingBottom: 0 }}>
          <Alert tone="error" title={`Could not resend to ${resend.variables?.email ?? "that address"}`}>
            {resendError}
          </Alert>
        </div>
      )}

      <div className="card__body card__body--flush">
        {invitations.isPending ? (
          <SkeletonRows label="Loading invitations" />
        ) : invitations.isError ? (
          <LoadFailed
            message={describeError(invitations.error)}
            onRetry={() => invitations.refetch()}
            retrying={invitations.isFetching}
          />
        ) : rows.length === 0 ? (
          <StateBlock icon={<MailIcon />} title="No invitations yet">
            <p>When you invite someone, their invitation shows here until they set a password.</p>
          </StateBlock>
        ) : (
          <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">Email</th>
                <th scope="col">Role</th>
                <th scope="col">Status</th>
                <th scope="col">Expiry</th>
                <th scope="col">
                  <span className="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((invitation) => {
                const resending = resend.isPending && resend.variables?.id === invitation.id;
                const canResend = invitation.status === "PENDING" || invitation.status === "EXPIRED";
                const canRevoke = invitation.status === "PENDING";
                return (
                  <tr key={invitation.id}>
                    <td className="table__primary" data-label="Email">
                      {invitation.email}
                      <span className="table__secondary">
                        Invited by {invitation.invited_by.name ?? invitation.invited_by.email ?? "a former admin"}
                      </span>
                    </td>
                    <td data-label="Role">{roleLabel(invitation.role)}</td>
                    <td data-label="Status">
                      <span className={`chip chip--${invitation.status.toLowerCase()}`}>
                        {STATUS_LABELS[invitation.status]}
                      </span>
                    </td>
                    <td className="table__wrap" data-label="Expiry">
                      <ExpiryCell invitation={invitation} />
                    </td>
                    <td className="table__actions-cell" data-label="Actions">
                      {(canResend || canRevoke) && (
                        <div className="table__actions">
                          {canResend && (
                            <Button
                              variant="secondary"
                              size="small"
                              loading={resending}
                              disabled={resend.isPending}
                              onClick={() => resend.mutate(invitation)}
                              aria-label={`Resend invitation to ${invitation.email}`}
                            >
                              Resend
                            </Button>
                          )}
                          {canRevoke && (
                            <Button
                              variant="danger"
                              size="small"
                              onClick={() => {
                                revoke.reset();
                                setToRevoke(invitation);
                              }}
                              aria-label={`Revoke invitation to ${invitation.email}`}
                            >
                              Revoke
                            </Button>
                          )}
                        </div>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          </div>
        )}
      </div>

      {invitations.hasNextPage && (
        <div className="card__footer">
          <Button
            variant="secondary"
            size="small"
            loading={invitations.isFetchingNextPage}
            onClick={() => invitations.fetchNextPage()}
          >
            Show more
          </Button>
        </div>
      )}

      <ConfirmDialog
        open={toRevoke !== null}
        title="Revoke this invitation?"
        confirmLabel={revoke.isPending ? "Revoking…" : "Revoke invitation"}
        cancelLabel="Keep it"
        danger
        busy={revoke.isPending}
        error={revokeError}
        onConfirm={() => toRevoke && revoke.mutate(toRevoke)}
        onCancel={() => setToRevoke(null)}
      >
        <p>
          The link sent to <strong>{toRevoke?.email}</strong> will stop working straight away, and they will not be able
          to create an account with it. You can invite them again later.
        </p>
      </ConfirmDialog>
    </Card>
  );
}

function ExpiryCell({ invitation }: { invitation: Invitation }) {
  if (invitation.status === "ACCEPTED" && invitation.accepted_at) {
    return (
      <span>
        Accepted <time dateTime={invitation.accepted_at}>{formatDateTime(invitation.accepted_at)}</time>
      </span>
    );
  }
  const prefix = invitation.status === "PENDING" ? "Expires" : invitation.status === "EXPIRED" ? "Expired" : "Was due";
  return (
    <span className={invitation.status === "PENDING" ? undefined : "muted"}>
      {prefix} <time dateTime={invitation.expires_at}>{formatDateTime(invitation.expires_at)}</time>
    </span>
  );
}

function StaffCard() {
  const me = useCurrentUser();
  const users = useInfiniteQuery({
    queryKey: USERS_KEY,
    queryFn: ({ pageParam }) =>
      unwrap(api.GET("/admin/users", { params: { query: { limit: PAGE_SIZE, cursor: pageParam } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

  const rows = users.data?.pages.flatMap((page) => page.items) ?? [];

  return (
    <Card>
      <div className="card__header">
        <h2>People with access</h2>
        {users.isSuccess && (
          <span className="small muted">
            {rows.length}
            {users.hasNextPage ? "+" : ""} {rows.length === 1 ? "person" : "people"}
          </span>
        )}
      </div>
      <div className="card__body card__body--flush">
        {users.isPending ? (
          <SkeletonRows label="Loading people" />
        ) : users.isError ? (
          <LoadFailed message={describeError(users.error)} onRetry={() => users.refetch()} retrying={users.isFetching} />
        ) : rows.length === 0 ? (
          <StateBlock icon={<TeamIcon />} title="Nobody here yet">
            <p>People appear here once they accept an invitation.</p>
          </StateBlock>
        ) : (
          <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Email</th>
                <th scope="col">Role</th>
                <th scope="col">Two-step sign-in</th>
                <th scope="col">Joined</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((user) => (
                <tr key={user.id}>
                  <td className="table__primary" data-label="Name">
                    {user.name ?? <span className="muted">No name yet</span>}
                    {user.id === me.id && (
                      <>
                        {" "}
                        <span className="chip chip--plain chip--role">You</span>
                      </>
                    )}
                  </td>
                  <td className="table__wrap" data-label="Email">
                    {user.email}
                  </td>
                  <td data-label="Role">{roleLabel(user.role)}</td>
                  <td data-label="Two-step sign-in">{user.totp_enabled ? "On" : <span className="muted">Off</span>}</td>
                  <td data-label="Joined">
                    <time dateTime={user.created_at}>{formatDate(user.created_at)}</time>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        )}
      </div>
      {users.hasNextPage && (
        <div className="card__footer">
          <Button variant="secondary" size="small" loading={users.isFetchingNextPage} onClick={() => users.fetchNextPage()}>
            Show more
          </Button>
        </div>
      )}
    </Card>
  );
}
