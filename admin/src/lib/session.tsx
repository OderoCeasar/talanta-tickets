import { unwrap, type AuthSession } from "@talanta/shared";
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { api, client, queryClient, subscribeSignedOut, tokenStore, type User } from "./api";

/** Why the user is looking at the sign-in page, when it was not their choice. */
export type SignedOutNotice = "expired" | "wrong-role";

export type SessionState =
  | { status: "restoring" }
  /** There is a stored session but the server could not be asked about it. */
  | { status: "unreachable" }
  /** `byChoice` is true when the user pressed Sign out, as opposed to being signed out. */
  | { status: "signedOut"; notice: SignedOutNotice | null; byChoice?: boolean }
  | { status: "signedIn"; user: User };

interface SessionContextValue {
  state: SessionState;
  /** Starts a session from a sign-in answer. False when the account may not use this site. */
  signIn(session: AuthSession): boolean;
  signOut(): Promise<void>;
  /** After an endpoint answers with the changed user (for example turning on two-step sign-in). */
  setUser(user: User): void;
  retryRestore(): void;
  clearNotice(): void;
}

const SessionContext = createContext<SessionContextValue | null>(null);

/** This site is for the people who run events. Fans and gate staff use the app. */
export function isStaff(role: User["role"]): boolean {
  return role === "ORGANISER" || role === "ADMIN";
}

function revokeOnServer(refreshToken: string): Promise<void> {
  // Best effort: the session is already gone from this browser whatever the server says.
  return api
    .POST("/auth/logout", { body: { refresh_token: refreshToken } })
    .then(() => undefined)
    .catch(() => undefined);
}

type Restored = { kind: "none" } | { kind: "user"; user: User } | { kind: "unreachable" };

// One restore per page load, shared by every caller. React's StrictMode runs effects twice
// in development, and two refreshes with the same rotating token would look like theft to
// the server.
let restoring: Promise<Restored> | null = null;

function restoreSession(): Promise<Restored> {
  restoring ??= (async (): Promise<Restored> => {
    try {
      const session = await client.refreshSession();
      if (!session) return { kind: "none" };
      const user = await unwrap(api.GET("/me"));
      return { kind: "user", user };
    } catch {
      return { kind: "unreachable" };
    }
  })();
  return restoring;
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<SessionState>({ status: "restoring" });

  const endLocally = useCallback((notice: SignedOutNotice | null, byChoice = false) => {
    tokenStore.clearTokens();
    queryClient.clear();
    setState({ status: "signedOut", notice, byChoice });
  }, []);

  const turnAway = useCallback(
    (refreshToken: string | null) => {
      if (refreshToken) void revokeOnServer(refreshToken);
      endLocally("wrong-role");
    },
    [endLocally],
  );

  const applyRestored = useCallback(
    (restored: Restored) => {
      if (restored.kind === "unreachable") setState({ status: "unreachable" });
      else if (restored.kind === "none") {
        // Keep a notice the client's sign-out callback may have set a moment ago.
        setState((current) => (current.status === "signedOut" ? current : { status: "signedOut", notice: null }));
      } else if (!isStaff(restored.user.role)) turnAway(tokenStore.getRefreshToken());
      else setState({ status: "signedIn", user: restored.user });
    },
    [turnAway],
  );

  useEffect(() => {
    let cancelled = false;
    void restoreSession().then((restored) => {
      if (!cancelled) applyRestored(restored);
    });
    return () => {
      cancelled = true;
    };
  }, [applyRestored]);

  // The API client calls this after the server has refused the refresh token.
  useEffect(
    () =>
      subscribeSignedOut(() => {
        queryClient.clear();
        setState({ status: "signedOut", notice: "expired" });
      }),
    [],
  );

  const value = useMemo<SessionContextValue>(
    () => ({
      state,
      signIn(session) {
        if (!isStaff(session.user.role)) {
          turnAway(session.refresh_token);
          return false;
        }
        // Signing in over another session (an invitation opened while signed in): end the old one.
        const previous = tokenStore.getRefreshToken();
        if (previous && previous !== session.refresh_token) void revokeOnServer(previous);
        tokenStore.setTokens({ accessToken: session.access_token, refreshToken: session.refresh_token });
        queryClient.clear();
        setState({ status: "signedIn", user: session.user });
        return true;
      },
      async signOut() {
        const refreshToken = tokenStore.getRefreshToken();
        // Clear this browser first, so a failed network call cannot leave the user signed in.
        endLocally(null, true);
        if (refreshToken) await revokeOnServer(refreshToken);
      },
      setUser(user) {
        setState((current) => (current.status === "signedIn" ? { status: "signedIn", user } : current));
      },
      retryRestore() {
        restoring = null;
        setState({ status: "restoring" });
        void restoreSession().then(applyRestored);
      },
      clearNotice() {
        setState((current) =>
          current.status === "signedOut" && current.notice ? { status: "signedOut", notice: null } : current,
        );
      },
    }),
    [state, turnAway, endLocally, applyRestored],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionContextValue {
  const value = useContext(SessionContext);
  if (!value) throw new Error("useSession must be used inside <SessionProvider>");
  return value;
}

/** For pages that only render when signed in (everything inside the app shell). */
export function useCurrentUser(): User {
  const { state } = useSession();
  if (state.status !== "signedIn") throw new Error("useCurrentUser needs a signed-in session");
  return state.user;
}
