// Gate mode's state for the screens: which event and gate this phone is set to, what it
// holds, and the background work (uploading scans, keeping the ticket list fresh).
//
// The ticket list and the scans live in SQLite, not in memory and not in a query cache: they
// are the phone's own records and must be there after a crash with no network.

import * as Crypto from "expo-crypto";
import * as Network from "expo-network";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import { errorMessage } from "../../lib/errors";
import { useAppActive } from "../../lib/useAppActive";
import { fetchManifest, openGateStore, sendScanBatch } from "./device";
import { ManifestError, syncManifest } from "./manifest";
import { createScanner, type ScanOutcome, type Scanner } from "./scanner";
import type { GateStore, ManifestMeta, ScanSummary } from "./store";
import { decodePublicKey, warmUpVerifier } from "./token";
import { describeStop, recheckAdmitted, uploadPending, type UploadDeps, type UploadReport } from "./upload";
import type { GateDevice } from "./verdict";

const SESSION_KEY = "session";
const DEVICE_KEY = "device_id";
/** How often, while gate mode is open, waiting scans are sent again and the ticket list is refreshed. */
const TICK_MS = 20_000;
const MANIFEST_REFRESH_MS = 60_000;

export type Activity = { kind: "manifest"; done: number; total: number | null } | { kind: "upload" } | { kind: "closing" } | null;

export interface EndCheck {
  /** Scans still waiting to upload (they stay on the phone either way). */
  pending: number;
  /** Whether the server could be asked again about the scans it had answered ADMITTED. */
  rechecked: boolean;
  /** How many results the server changed during this check. */
  changed: number;
}

interface GateValue {
  /** `opening` until the database is open and the saved session is read. */
  status: "opening" | "ready" | "failed";
  openError: string | null;
  retryOpen: () => void;
  store: GateStore | null;
  device: GateDevice | null;
  manifest: ManifestMeta | null;
  pending: number;
  summary: ScanSummary;
  activity: Activity;
  /** Why the last ticket-list sync failed, in plain words. */
  syncError: string | null;
  /** Why scans are still waiting, in plain words. */
  uploadNote: string | null;
  online: boolean | null;
  /** Goes up whenever scans change, so lists know to read again. */
  revision: number;
  /** Downloads the event's tickets and sets this phone to the gate. Throws with the reason if it cannot. */
  startSession: (device: GateDevice) => Promise<void>;
  /** Uploads waiting scans, then refreshes the ticket list. */
  syncNow: () => Promise<void>;
  scan: (text: string) => Promise<ScanOutcome>;
  /** Uploads what it can and asks the server once more about admitted scans. Does not close anything. */
  checkBeforeEnd: () => Promise<EndCheck>;
  endSession: () => Promise<void>;
}

const GateContext = createContext<GateValue | null>(null);
const NO_SCANS: ScanSummary = { total: 0, admitted: 0, changed: 0 };

/** Plain words for a ticket-list download that failed. */
export function syncFailure(error: unknown): string {
  if (error instanceof ManifestError) return `${error.message} Tell your supervisor.`;
  return errorMessage(error);
}

export function GateProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<GateValue["status"]>("opening");
  const [openError, setOpenError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [store, setStore] = useState<GateStore | null>(null);
  const [device, setDevice] = useState<GateDevice | null>(null);
  const [manifest, setManifest] = useState<ManifestMeta | null>(null);
  const [pending, setPending] = useState(0);
  const [summary, setSummary] = useState<ScanSummary>(NO_SCANS);
  const [activity, setActivity] = useState<Activity>(null);
  const [syncError, setSyncError] = useState<string | null>(null);
  const [uploadNote, setUploadNote] = useState<string | null>(null);
  const [revision, setRevision] = useState(0);
  const network = Network.useNetworkState();
  const online = network.isConnected === undefined ? null : network.isConnected && network.isInternetReachable !== false;
  const active = useAppActive();

  const deviceId = useRef<string | null>(null);
  const scanner = useRef<Scanner | null>(null);
  const warmed = useRef(false);
  const uploading = useRef<Promise<UploadReport | null> | null>(null);
  const syncing = useRef<Promise<void> | null>(null);
  const uploadNotBefore = useRef(0);
  const manifestSyncedAt = useRef(0);
  const deviceRef = useRef<GateDevice | null>(null);
  deviceRef.current = device;

  const refreshCounts = useCallback(async (s: GateStore, d: GateDevice | null) => {
    const [waiting, sum] = await Promise.all([s.countPending(), d ? s.scanSummary(d.eventId) : Promise.resolve(NO_SCANS)]);
    setPending(waiting);
    setSummary(sum);
    setRevision((r) => r + 1);
  }, []);

  /** Reads the manifest the phone holds for the device's event and gets ready to scan with its keys. */
  const load = useCallback(async (s: GateStore, d: GateDevice | null) => {
    scanner.current = null;
    if (!d) {
      setManifest(null);
      return;
    }
    const [meta, keys] = await Promise.all([s.getManifestMeta(d.eventId), s.getKeys(d.eventId)]);
    setManifest(meta);
    if (!meta) return;
    const byId = new Map<string, Uint8Array>();
    for (const k of keys) {
      const bytes = decodePublicKey(k.publicKey);
      if (bytes) byId.set(k.keyId, bytes);
    }
    scanner.current = createScanner({ store: s, keys: byId, now: () => new Date(), newId: () => Crypto.randomUUID() }, d);
    // Once per app start, after the screen has drawn: the first signature check is the slow one.
    if (!warmed.current) {
      warmed.current = true;
      setTimeout(() => {
        const started = Date.now();
        const ok = warmUpVerifier();
        if (__DEV__) console.log(`[gate] signature check warmed up in ${Date.now() - started} ms (${ok ? "ok" : "FAILED"})`);
      }, 300);
    }
  }, []);

  useEffect(() => {
    let cancelled = false;
    setStatus("opening");
    (async () => {
      try {
        const s = await openGateStore();
        let id = await s.getSetting(DEVICE_KEY);
        if (!id) {
          // Stable per install: made once, kept with the scans it is sent with.
          id = `gate-${Crypto.randomUUID()}`;
          await s.setSetting(DEVICE_KEY, id);
        }
        deviceId.current = id;
        const saved = await s.getSetting(SESSION_KEY);
        const d = saved ? (JSON.parse(saved) as GateDevice) : null;
        await load(s, d);
        await refreshCounts(s, d);
        if (cancelled) return;
        setStore(s);
        setDevice(d);
        setStatus("ready");
      } catch (error) {
        if (cancelled) return;
        console.warn("Gate storage did not open.", error);
        setOpenError("This phone's gate storage could not be opened. Free some space and try again.");
        setStatus("failed");
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [attempt, load, refreshCounts]);

  const uploadDeps = useCallback((s: GateStore): UploadDeps => ({ store: s, send: sendScanBatch, deviceId: deviceId.current ?? "gate-unknown", now: () => new Date() }), []);

  /** One upload run at a time. `force` ignores a Retry-After still running (the steward pressed Sync). */
  const upload = useCallback(
    (force: boolean): Promise<UploadReport | null> => {
      if (!store) return Promise.resolve(null);
      if (uploading.current) return uploading.current;
      if (!force && Date.now() < uploadNotBefore.current) return Promise.resolve(null);
      const run = (async () => {
        if ((await store.countPending()) === 0) {
          setUploadNote(null);
          return null;
        }
        setActivity((a) => a ?? { kind: "upload" });
        try {
          const report = await uploadPending(uploadDeps(store));
          const wait = report.stopped?.kind === "retry" ? report.stopped.retryAfterSeconds : null;
          uploadNotBefore.current = wait ? Date.now() + wait * 1000 : 0;
          setUploadNote(describeStop(report.stopped));
          return report;
        } catch (error) {
          // The phone's own storage failed mid-run. Nothing left the queue that was not answered.
          console.warn("Uploading scans failed.", error);
          setUploadNote("The scans could not be uploaded just now. They are kept on this phone.");
          return null;
        } finally {
          setActivity((a) => (a?.kind === "upload" ? null : a));
          await refreshCounts(store, deviceRef.current).catch(() => undefined);
        }
      })();
      uploading.current = run;
      void run.finally(() => {
        uploading.current = null;
      });
      return run;
    },
    [store, uploadDeps, refreshCounts],
  );

  /** Brings the ticket list up to date. Throws; callers decide whether to show it. */
  const refreshManifest = useCallback(
    (d: GateDevice, showProgress: boolean): Promise<void> => {
      if (!store) return Promise.resolve();
      if (syncing.current) return syncing.current;
      const run = (async () => {
        if (showProgress) setActivity({ kind: "manifest", done: 0, total: null });
        try {
          // Timed in development: download and parse, then the write (see mobile/README.md).
          const started = Date.now();
          let fetched = started;
          const timedFetch: typeof fetchManifest = async (eventId, since) => {
            const answer = await fetchManifest(eventId, since);
            fetched = Date.now();
            return answer;
          };
          const result = await syncManifest(store, timedFetch, d.eventId, showProgress ? (done, total) => setActivity({ kind: "manifest", done, total }) : undefined);
          if (__DEV__) console.log(`[gate] manifest ${result.full ? "full" : "changes"}: ${result.changes} tuples, download+parse ${fetched - started} ms, save ${Date.now() - fetched} ms, ${result.count} tickets held`);
          manifestSyncedAt.current = Date.now();
          setSyncError(null);
        } finally {
          if (showProgress) setActivity(null);
          // Also after a failure: a new key set or count must never be half-loaded in memory.
          if (deviceRef.current?.eventId === d.eventId) await load(store, deviceRef.current);
        }
      })();
      syncing.current = run;
      void run.catch(() => undefined).finally(() => {
        syncing.current = null;
      });
      return run;
    },
    [store, load],
  );

  const startSession = useCallback(
    async (d: GateDevice) => {
      if (!store) throw new Error("Gate storage is not open.");
      setSyncError(null);
      try {
        await refreshManifest(d, true);
      } catch (error) {
        // No network, but this phone already holds the event's tickets from an earlier
        // session: scanning can start with them. With nothing on the phone it cannot.
        if (!(await store.getManifestMeta(d.eventId))) throw error;
        setSyncError(syncFailure(error));
      }
      // Saved only once the tickets are on the phone: a session without a manifest cannot scan.
      await store.setSetting(SESSION_KEY, JSON.stringify(d));
      deviceRef.current = d;
      setDevice(d);
      await load(store, d);
      await refreshCounts(store, d);
    },
    [store, refreshManifest, load, refreshCounts],
  );

  const syncNow = useCallback(async () => {
    if (!store || !device) return;
    await upload(true);
    try {
      await refreshManifest(device, true);
    } catch (error) {
      setSyncError(syncFailure(error));
    }
    await refreshCounts(store, device);
  }, [store, device, upload, refreshManifest, refreshCounts]);

  // Background work while gate mode is open and the app is in front: on arrival, when the
  // network comes back, and every 20 seconds. Each run is cheap when there is nothing to do.
  const tick = useCallback(async () => {
    if (!store) return;
    await upload(false);
    const d = deviceRef.current;
    if (d && Date.now() - manifestSyncedAt.current > MANIFEST_REFRESH_MS) {
      // Quiet: with no network this simply fails and the phone scans on with what it has. The
      // time is stamped only on success, so the first run after the network returns syncs.
      await refreshManifest(d, false).catch(() => undefined);
    }
  }, [store, upload, refreshManifest]);

  useEffect(() => {
    if (status !== "ready" || !active) return;
    if (online !== false) void tick();
    const timer = setInterval(() => void tick(), TICK_MS);
    return () => clearInterval(timer);
  }, [status, active, online, tick]);

  const scan = useCallback(
    async (text: string): Promise<ScanOutcome> => {
      if (!store || !scanner.current) throw new Error("This phone has no ticket list yet.");
      const started = Date.now();
      const outcome = await scanner.current.scan(text);
      if (__DEV__) console.log(`[gate] scan ${outcome.result}/${outcome.reason}: checked and saved in ${Date.now() - started} ms`);
      // The scan is on disk. Counting and uploading come after and never hold the result up.
      void refreshCounts(store, deviceRef.current).catch(() => undefined);
      if (outcome.queued) void upload(false);
      return outcome;
    },
    [store, refreshCounts, upload],
  );

  const checkBeforeEnd = useCallback(async (): Promise<EndCheck> => {
    if (!store || !device) return { pending: 0, rechecked: true, changed: 0 };
    setActivity({ kind: "closing" });
    try {
      const first = await upload(true);
      let changed = first?.overridden ?? 0;
      let rechecked = false;
      try {
        // The only way to learn that an admission was later given to another phone's earlier scan.
        const again = await recheckAdmitted(uploadDeps(store), device.eventId);
        rechecked = again.stopped === null;
        changed += again.overridden;
      } catch (error) {
        console.warn("Rechecking admitted scans failed.", error);
      }
      await refreshCounts(store, device);
      return { pending: await store.countPending(), rechecked, changed };
    } finally {
      setActivity(null);
    }
  }, [store, device, upload, uploadDeps, refreshCounts]);

  const endSession = useCallback(async () => {
    if (!store) return;
    // Only the choice of event and gate goes. Tickets and scans stay on the phone.
    await store.setSetting(SESSION_KEY, null);
    deviceRef.current = null;
    setDevice(null);
    setSyncError(null);
    await load(store, null);
    await refreshCounts(store, null);
  }, [store, load, refreshCounts]);

  const retryOpen = useCallback(() => setAttempt((n) => n + 1), []);

  const value = useMemo<GateValue>(
    () => ({ status, openError, retryOpen, store, device, manifest, pending, summary, activity, syncError, uploadNote, online, revision, startSession, syncNow, scan, checkBeforeEnd, endSession }),
    [status, openError, retryOpen, store, device, manifest, pending, summary, activity, syncError, uploadNote, online, revision, startSession, syncNow, scan, checkBeforeEnd, endSession],
  );
  return <GateContext.Provider value={value}>{children}</GateContext.Provider>;
}

export function useGate(): GateValue {
  const value = useContext(GateContext);
  if (!value) throw new Error("useGate must be used inside GateProvider");
  return value;
}
