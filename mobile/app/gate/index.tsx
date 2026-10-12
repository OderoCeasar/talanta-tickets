import { useQuery } from "@tanstack/react-query";
import { router } from "expo-router";
import { useState } from "react";
import { Alert, Pressable, RefreshControl, StyleSheet, Text, View } from "react-native";

import type { Schemas } from "../../src/api/client";
import { fetchGateEvents } from "../../src/features/gate/device";
import { syncFailure, useGate, type Activity } from "../../src/features/gate/GateProvider";
import { CLOCK_WARNING_MS } from "../../src/features/gate/manifest";
import { formatCount } from "../../src/lib/money";
import { formatPhone } from "../../src/lib/phone";
import { formatDateTime } from "../../src/lib/time";
import { useSession } from "../../src/session";
import { font, palette, radius, space, type } from "../../src/theme";
import { Banner, Button, Card, EmptyState, ErrorState, Icon, Loading, Screen } from "../../src/ui";

type GateEvent = Schemas["GateEventListItem"];

const plural = (n: number, one: string, many: string) => `${formatCount(n)} ${n === 1 ? one : many}`;

function activityText(activity: Activity): string | null {
  if (!activity) return null;
  if (activity.kind === "upload") return "Uploading scans…";
  if (activity.kind === "closing") return "Checking scans with the server…";
  if (activity.total === null) return "Downloading the ticket list…";
  return `Saving tickets on this phone: ${formatCount(activity.done)} of ${formatCount(activity.total)}`;
}

/** "3 minutes fast" / "2 minutes slow", for the clock warning. */
function skewText(ms: number): string {
  const minutes = Math.max(1, Math.round(Math.abs(ms) / 60_000));
  return `${minutes} minute${minutes === 1 ? "" : "s"} ${ms > 0 ? "fast" : "slow"}`;
}

export default function GateHome() {
  const gate = useGate();
  const { user, signOut } = useSession();
  const [signingOut, setSigningOut] = useState(false);

  const confirmSignOut = () => {
    const waiting = gate.pending > 0 ? `${plural(gate.pending, "scan has", "scans have")} not been uploaded yet. They stay on this phone and upload when a gate steward signs in again.\n\n` : "";
    Alert.alert("Sign out?", `${waiting}You will need a new code to sign in again.`, [
      { text: "Cancel", style: "cancel" },
      {
        text: "Sign out",
        style: "destructive",
        onPress: () => {
          setSigningOut(true);
          void signOut().finally(() => setSigningOut(false));
        },
      },
    ]);
  };

  const account = (
    <View style={styles.account}>
      <Text style={type.small}>Signed in as gate staff{user?.phone ? ` · ${formatPhone(user.phone)}` : ""}</Text>
      <Button title="Sign out" variant="ghost" onPress={confirmSignOut} loading={signingOut} testID="gate-sign-out" />
    </View>
  );

  if (gate.status === "opening") {
    return (
      <Screen scroll={false}>
        <Loading label="Opening gate mode…" />
      </Screen>
    );
  }
  if (gate.status === "failed") {
    return (
      <Screen>
        <Banner tone="error" title="Gate mode could not start" action="Try again" onAction={gate.retryOpen}>
          {gate.openError}
        </Banner>
        {account}
      </Screen>
    );
  }

  return gate.device ? <Session account={account} /> : <Setup account={account} />;
}

// ---------------------------------------------------------------------------- choosing

function Setup({ account }: { account: React.ReactNode }) {
  const gate = useGate();
  const events = useQuery({ queryKey: ["gate", "events"], queryFn: fetchGateEvents, staleTime: 0 });
  const [chosen, setChosen] = useState<GateEvent | null>(null);
  const [group, setGroup] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const busy = gate.activity?.kind === "manifest";

  const start = async () => {
    if (!chosen || !group) return;
    setFailure(null);
    try {
      await gate.startSession({ eventId: chosen.event.id, gateGroup: group });
    } catch (error) {
      setFailure(syncFailure(error));
    }
  };

  const waiting =
    gate.pending > 0 ? (
      <Banner tone="warning" title={`${plural(gate.pending, "scan is", "scans are")} waiting to upload`} testID="gate-waiting">
        {gate.uploadNote ?? "They upload by themselves when this phone has a connection."}
      </Banner>
    ) : null;

  if (chosen) {
    return (
      <Screen>
        {waiting}
        <Card>
          <Text style={type.overline}>Event</Text>
          <Text style={type.h2}>{chosen.event.title}</Text>
          <Text style={type.muted}>
            {formatDateTime(chosen.event.kickoff_at)} · {chosen.event.venue_name}
          </Text>
          <Text style={type.small}>{plural(chosen.ticket_count, "ticket", "tickets")} to download</Text>
          {busy ? null : <Button title="Choose another event" variant="ghost" onPress={() => (setChosen(null), setGroup(null), setFailure(null))} />}
        </Card>

        <Card>
          <Text style={type.h3}>Which gate are you at?</Text>
          <Text style={type.muted}>This phone will only let in tickets for that gate.</Text>
          {chosen.gate_groups.length === 0 ? (
            <Banner tone="warning">This event has no gates set up. Tell your supervisor.</Banner>
          ) : (
            <View style={styles.groups} accessibilityRole="radiogroup">
              {chosen.gate_groups.map((g) => {
                const on = g === group;
                return (
                  <Pressable
                    key={g}
                    testID={`gate-group-${g}`}
                    disabled={busy}
                    onPress={() => setGroup(g)}
                    accessibilityRole="radio"
                    accessibilityState={{ selected: on, disabled: busy }}
                    accessibilityLabel={`Gate ${g}`}
                    style={[styles.group, on && styles.groupOn]}
                  >
                    <Text style={[styles.groupText, on && styles.groupTextOn]}>{g}</Text>
                  </Pressable>
                );
              })}
            </View>
          )}
        </Card>

        {failure ? (
          <Banner tone="error" title="The ticket list did not download" testID="gate-download-error">
            {failure}
          </Banner>
        ) : null}
        {busy ? <Banner tone="info">{activityText(gate.activity)}</Banner> : null}
        <Button title={busy ? "Downloading…" : "Download tickets and start"} onPress={() => void start()} disabled={!group} loading={busy} testID="gate-download" />
        <Text style={[type.small, styles.center]}>Do this while you have a connection. After that the phone checks tickets with no network.</Text>
        {account}
      </Screen>
    );
  }

  return (
    <Screen refreshControl={<RefreshControl refreshing={events.isRefetching} onRefresh={() => void events.refetch()} tintColor={palette.emeraldInk} />}>
      {waiting}
      <View>
        <Text style={type.h2}>Choose the event</Text>
        <Text style={type.muted}>Pick the match you are working at.</Text>
      </View>
      {events.isPending ? (
        <Loading label="Loading events…" />
      ) : events.isError ? (
        <ErrorState error={events.error} onRetry={() => void events.refetch()} title="The events did not load" />
      ) : events.data.items.length === 0 ? (
        <EmptyState icon="calendar" title="No events to scan for" body="There is no published event today or ahead. Ask your supervisor, then pull down to check again." action="Check again" onAction={() => void events.refetch()} />
      ) : (
        events.data.items.map((item) => (
          <Card key={item.event.id} onPress={() => (setChosen(item), setGroup(null))} accessibilityLabel={`${item.event.title}, ${formatDateTime(item.event.kickoff_at)}`}>
            <View style={styles.eventRow} testID={`gate-event-${item.event.id}`}>
              <View style={styles.fill}>
                <Text style={type.h3}>{item.event.title}</Text>
                <Text style={type.muted}>{formatDateTime(item.event.kickoff_at)}</Text>
                <Text style={type.small}>
                  {item.event.venue_name} · {plural(item.ticket_count, "ticket", "tickets")}
                </Text>
              </View>
              <Icon name="chevron" color={palette.grey600} />
            </View>
          </Card>
        ))
      )}
      {account}
    </Screen>
  );
}

// ---------------------------------------------------------------------------- ready to scan

function Session({ account }: { account: React.ReactNode }) {
  const gate = useGate();
  const { device, manifest, activity } = gate;
  const [ending, setEnding] = useState(false);
  if (!device) return null;
  const busy = activity !== null;

  const end = async () => {
    setEnding(true);
    try {
      // Upload what is waiting and ask once more about the scans the server had accepted:
      // nothing tells this phone later that one of them was given to another phone's scan.
      const check = await gate.checkBeforeEnd();
      const lines: string[] = [];
      if (check.pending > 0) lines.push(`${plural(check.pending, "scan has", "scans have")} NOT been uploaded. They stay on this phone and upload when it is online in gate mode.`);
      if (!check.rechecked) lines.push("The server could not be asked for the final results. Open History later to see them.");
      if (check.changed > 0) lines.push(`${plural(check.changed, "result was", "results were")} changed by the server. See "Changed after sync" in History.`);
      if (lines.length === 0) lines.push("Every scan is uploaded and confirmed.");
      Alert.alert("End this session?", `${lines.join("\n\n")}\n\nThe phone will forget which event and gate it is set to.`, [
        { text: "Keep scanning", style: "cancel" },
        { text: "End session", style: "destructive", onPress: () => void gate.endSession() },
      ]);
    } finally {
      setEnding(false);
    }
  };

  const skew = manifest?.clockSkewMs ?? null;

  return (
    <Screen
      footer={<Button title="Start scanning" icon="ticket" onPress={() => router.push("/gate/scan")} disabled={!manifest} testID="gate-start-scanning" />}
      refreshControl={<RefreshControl refreshing={false} onRefresh={() => void gate.syncNow()} tintColor={palette.emeraldInk} />}
    >
      <View style={styles.hero}>
        <Text style={styles.heroOver}>THIS PHONE IS AT GATE</Text>
        <Text style={styles.heroGate} testID="gate-current-group">{device.gateGroup}</Text>
        <Text style={styles.heroEvent}>{manifest?.event.title ?? `Event ${device.eventId}`}</Text>
        {manifest ? <Text style={styles.heroWhen}>{formatDateTime(manifest.event.kickoff_at)}</Text> : null}
      </View>

      {gate.online === false ? (
        <Banner tone="info" title="No connection" testID="gate-offline">
          Scanning still works. Scans are kept on this phone and upload when the connection returns.
        </Banner>
      ) : null}

      {skew !== null && Math.abs(skew) > CLOCK_WARNING_MS ? (
        <Banner tone="warning" title={`This phone's clock is ${skewText(skew)}`} testID="gate-clock-warning">
          Scan times decide which phone scanned a ticket first. Set the date and time to automatic in the phone's settings, then sync again.
        </Banner>
      ) : null}

      <Card>
        <Text style={type.overline}>Ticket list on this phone</Text>
        {manifest ? (
          <>
            <Text style={type.price} testID="gate-ticket-count">{plural(manifest.ticketCount, "ticket", "tickets")}</Text>
            <Text style={type.muted} testID="gate-updated">Last updated {formatDateTime(manifest.updatedAt)} (Nairobi time)</Text>
          </>
        ) : (
          <Banner tone="error">The ticket list is not on this phone. Sync while you have a connection.</Banner>
        )}
        {gate.syncError ? (
          <Banner tone="warning" title="The ticket list could not be updated" testID="gate-sync-error">
            {gate.syncError} The phone is using the list it already has.
          </Banner>
        ) : null}
        {busy ? <Text style={type.small} testID="gate-activity">{activityText(activity)}</Text> : null}
        <Button title="Sync now" variant="secondary" onPress={() => void gate.syncNow()} loading={busy} disabled={ending} testID="gate-sync" />
      </Card>

      <Card onPress={() => router.push("/gate/history")} accessibilityLabel="Scans on this phone. Opens the history.">
        <View style={styles.eventRow}>
          <View style={styles.fill}>
            <Text style={type.overline}>Scans</Text>
            <Text style={type.price} testID="gate-pending">{gate.pending === 0 ? "All scans uploaded" : `${formatCount(gate.pending)} waiting to upload`}</Text>
            <Text style={type.muted}>
              {plural(gate.summary.total, "scan", "scans")} for this event · {formatCount(gate.summary.admitted)} let in
            </Text>
            {gate.pending > 0 && gate.uploadNote ? <Text style={type.small} testID="gate-upload-note">{gate.uploadNote}</Text> : null}
            {gate.summary.changed > 0 ? (
              <Text style={styles.changed} testID="gate-changed">
                {plural(gate.summary.changed, "result", "results")} changed after sync. Tap to see.
              </Text>
            ) : null}
          </View>
          <Icon name="chevron" color={palette.grey600} />
        </View>
      </Card>

      <Button title="End session" variant="danger" onPress={() => void end()} loading={ending} disabled={busy && !ending} testID="gate-end" />
      {account}
    </Screen>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1, gap: 2 },
  center: { textAlign: "center" },
  account: { alignItems: "center", gap: space.xs, marginTop: space.sm },
  eventRow: { flexDirection: "row", alignItems: "center", gap: space.md },
  groups: { flexDirection: "row", flexWrap: "wrap", gap: space.sm },
  group: { minHeight: 52, minWidth: 104, paddingHorizontal: space.lg, borderRadius: radius.md, borderWidth: 1.5, borderColor: palette.ink200, backgroundColor: palette.white, alignItems: "center", justifyContent: "center" },
  groupOn: { backgroundColor: palette.emeraldInk, borderColor: palette.emeraldInk },
  groupText: { fontFamily: font.headingBold, fontSize: 16, color: palette.emeraldInk },
  groupTextOn: { color: palette.champagne },
  hero: { backgroundColor: palette.emeraldInk, borderRadius: radius.lg, padding: space.xl, gap: 2 },
  heroOver: { fontFamily: font.bodySemiBold, fontSize: 12, letterSpacing: 1, color: palette.champagne300 },
  heroGate: { fontFamily: font.headingExtraBold, fontSize: 38, lineHeight: 46, color: palette.champagne },
  heroEvent: { ...type.h3, color: palette.white, marginTop: space.sm },
  heroWhen: { ...type.muted, color: palette.champagne300 },
  changed: { ...type.label, color: palette.warning, marginTop: space.xs },
});
