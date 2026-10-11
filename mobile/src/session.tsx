import type { AuthSession } from "@talanta/shared";
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { api, queryClient, subscribeSignedOut, type Schemas } from "./api/client";
import { keys, useMe } from "./api/queries";
import { clearUserStorage, readStoredUser, writeStoredUser } from "./api/storage";
import { hasRefreshToken, tokenStore } from "./api/tokens";
import { useBookingFlow } from "./store/bookingFlow";

type Status = "loading" | "signedOut" | "signedIn";

interface SessionValue {
  status: Status;
  /** From `GET /me` (TanStack Query); the copy saved on the phone until that answers. */
  user: Schemas["User"] | null;
  signIn: (session: AuthSession) => Promise<void>;
  signOut: () => Promise<void>;
}

const SessionContext = createContext<SessionValue | null>(null);

/** Forgets everything that belonged to the signed-in person. */
async function forgetUser(): Promise<void> {
  await tokenStore.clearTokens();
  await clearUserStorage();
  queryClient.clear();
  useBookingFlow.getState().reset();
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<Status>("loading");

  useEffect(() => {
    let cancelled = false;
    (async () => {
      // A stored refresh token means "signed in", even with no network: tickets must open
      // offline. If the server later refuses the token, the client calls onSignedOut.
      const signedIn = await hasRefreshToken();
      if (signedIn) {
        const stored = await readStoredUser();
        // Marked stale so GET /me still runs and corrects it.
        if (stored) queryClient.setQueryData(keys.me, stored, { updatedAt: 0 });
      }
      if (!cancelled) setStatus(signedIn ? "signedIn" : "signedOut");
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(
    () =>
      subscribeSignedOut(() => {
        void forgetUser().then(() => setStatus("signedOut"));
      }),
    [],
  );

  const me = useMe(status === "signedIn");

  const signIn = useCallback(async (session: AuthSession) => {
    await tokenStore.setTokens({ accessToken: session.access_token, refreshToken: session.refresh_token });
    await writeStoredUser(session.user);
    queryClient.setQueryData(keys.me, session.user);
    // Seat maps differ once signed in (the fan's own held seats come back as "M").
    void queryClient.invalidateQueries({ queryKey: ["events"] });
    setStatus("signedIn");
  }, []);

  const signOut = useCallback(async () => {
    const refreshToken = await tokenStore.getRefreshToken();
    if (refreshToken) {
      // Best effort: the session ends on this phone even if the server cannot be told.
      await api.POST("/auth/logout", { body: { refresh_token: refreshToken } }).catch(() => undefined);
    }
    await forgetUser();
    setStatus("signedOut");
  }, []);

  const user = status === "signedIn" ? (me.data ?? null) : null;
  const value = useMemo(() => ({ status, user, signIn, signOut }), [status, user, signIn, signOut]);
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionValue {
  const value = useContext(SessionContext);
  if (!value) throw new Error("useSession must be used inside SessionProvider");
  return value;
}
