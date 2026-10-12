import { useEffect, useState } from "react";
import { FlatList, Pressable, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { useGate } from "../../src/features/gate/GateProvider";
import type { ScanFilter, StoredScan } from "../../src/features/gate/store";
import { describeVerdict } from "../../src/features/gate/verdict";
import { formatCount } from "../../src/lib/money";
import { formatClock, formatDate, formatTime } from "../../src/lib/time";
import { font, palette, radius, space, type } from "../../src/theme";
import { Banner, Button, Chip, EmptyState, Loading, Screen } from "../../src/ui";

/** The newest scans are shown; a steward at one gate makes a few thousand in a match. */
const LIMIT = 500;

const FILTERS: { key: ScanFilter; label: string }[] = [
  { key: "all", label: "All" },
  { key: "waiting", label: "Waiting to upload" },
  { key: "changed", label: "Changed after sync" },
];

function uploadText(scan: StoredScan): string {
  if (scan.upload === "PENDING") return "Waiting to upload";
  if (scan.upload === "LOCAL") return "Not a ticket code · kept on this phone only";
  return scan.uploadedAt ? `Uploaded ${formatTime(scan.uploadedAt)}` : "Uploaded";
}

/** What the server decided instead, for the supervisor. */
function changedText(scan: StoredScan): string {
  const server = scan.serverResult ?? "INVALID";
  const words = describeVerdict(server, "", null);
  if (scan.result === "ADMITTED" && server === "DUPLICATE") {
    return `Server: another phone scanned this ticket first${scan.firstAdmittedAt ? `, at ${formatClock(scan.firstAdmittedAt)}` : ""}. Two people may be inside on one ticket. Tell your supervisor.`;
  }
  if (scan.result === "ADMITTED") return `Server: ${words.title.toLowerCase()}. ${words.detail} This phone let them in. Tell your supervisor.`;
  return `Server: ${words.title.toLowerCase()}. ${words.detail}`;
}

function Row({ scan }: { scan: StoredScan }) {
  const words = describeVerdict(scan.result, scan.reason, null);
  const admitted = scan.result === "ADMITTED";
  return (
    <View style={[styles.row, scan.overridden && styles.rowChanged]} testID={`history-row-${scan.seq}`}>
      <View style={styles.rowHead}>
        <View style={[styles.dot, { backgroundColor: admitted ? palette.admit : palette.refuse }]} />
        <Text style={[type.label, styles.fill]}>{admitted ? "Let in" : words.title}</Text>
        <Text style={styles.time}>{formatClock(scan.scannedAt)}</Text>
      </View>
      <Text style={type.small}>
        {scan.ticketId ? `Ticket ${scan.ticketId.slice(0, 8)} · ` : ""}
        {formatDate(scan.scannedAt)} · gate {scan.gateGroup}
      </Text>
      {admitted ? null : <Text style={type.small}>{words.detail}</Text>}
      <View style={styles.rowFoot}>
        <Chip label={uploadText(scan)} tone={scan.upload === "PENDING" ? "warning" : scan.upload === "LOCAL" ? "muted" : "ink"} />
      </View>
      {scan.overridden ? <Text style={styles.changed}>{changedText(scan)}</Text> : null}
    </View>
  );
}

export default function HistoryScreen() {
  const gate = useGate();
  const insets = useSafeAreaInsets();
  const { store, device, revision } = gate;
  const [filter, setFilter] = useState<ScanFilter>("all");
  const [scans, setScans] = useState<StoredScan[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);

  // Read again whenever a scan is made or an upload is answered (`revision`).
  useEffect(() => {
    if (!store || !device) return;
    let cancelled = false;
    store
      .listScans(device.eventId, LIMIT, filter)
      .then((rows) => {
        if (cancelled) return;
        setScans(rows);
        setFailed(false);
      })
      .catch(() => !cancelled && setFailed(true));
    return () => {
      cancelled = true;
    };
  }, [store, device, filter, revision, attempt]);

  if (gate.status === "ready" && !device) {
    return (
      <Screen scroll={false}>
        <EmptyState icon="gate" title="No session" body="Choose an event and a gate first. Scans made on this phone are listed here." />
      </Screen>
    );
  }

  const empty =
    filter === "waiting"
      ? { title: "Nothing is waiting", body: "Every scan on this phone has been uploaded." }
      : filter === "changed"
        ? { title: "No results were changed", body: "The server agreed with every scan this phone has uploaded." }
        : { title: "No scans yet", body: "Tickets scanned on this phone for this event are listed here." };

  return (
    <View style={styles.root}>
      <View style={styles.filters} accessibilityRole="tablist">
        {FILTERS.map((f) => {
          const on = f.key === filter;
          const count = f.key === "all" ? gate.summary.total : f.key === "changed" ? gate.summary.changed : null;
          return (
            <Pressable key={f.key} onPress={() => setFilter(f.key)} accessibilityRole="tab" accessibilityState={{ selected: on }} style={[styles.filter, on && styles.filterOn]} testID={`history-filter-${f.key}`}>
              <Text style={[styles.filterText, on && styles.filterTextOn]}>
                {f.label}
                {count !== null ? ` (${formatCount(count)})` : ""}
              </Text>
            </Pressable>
          );
        })}
      </View>

      {failed ? (
        <View style={styles.pad}>
          <Banner tone="error" title="The scans could not be read" action="Try again" onAction={() => setAttempt((n) => n + 1)}>
            The phone's storage did not answer. The scans themselves are still saved.
          </Banner>
        </View>
      ) : scans === null ? (
        <Loading label="Reading scans…" />
      ) : (
        <FlatList
          data={scans}
          keyExtractor={(s) => String(s.seq)}
          renderItem={({ item }) => <Row scan={item} />}
          contentContainerStyle={[styles.list, { paddingBottom: insets.bottom + space.xl }]}
          ListHeaderComponent={
            <View style={styles.header}>
              {gate.pending > 0 ? (
                <Banner tone="warning" title={`${formatCount(gate.pending)} waiting to upload`} testID="history-waiting">
                  {gate.uploadNote ?? "They upload by themselves when this phone has a connection."}
                </Banner>
              ) : null}
              {gate.pending > 0 || filter !== "all" ? null : scans.length > 0 ? <Banner tone="success">Every scan on this phone has been uploaded.</Banner> : null}
              {gate.pending > 0 ? <Button title="Upload now" variant="secondary" onPress={() => void gate.syncNow()} loading={gate.activity !== null} testID="history-upload" /> : null}
              {scans.length === LIMIT ? <Text style={type.small}>Showing the newest {formatCount(LIMIT)} scans.</Text> : null}
            </View>
          }
          ListEmptyComponent={<EmptyState icon="ticket" title={empty.title} body={empty.body} />}
        />
      )}
    </View>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: palette.champagne50 },
  fill: { flex: 1 },
  pad: { padding: space.lg },
  filters: { flexDirection: "row", flexWrap: "wrap", gap: space.sm, paddingHorizontal: space.lg, paddingTop: space.md, paddingBottom: space.sm },
  filter: { minHeight: 40, justifyContent: "center", paddingHorizontal: space.md, borderRadius: radius.pill, borderWidth: 1.5, borderColor: palette.ink200, backgroundColor: palette.white },
  filterOn: { backgroundColor: palette.emeraldInk, borderColor: palette.emeraldInk },
  filterText: { fontFamily: font.bodySemiBold, fontSize: 14, color: palette.emeraldInk },
  filterTextOn: { color: palette.champagne },
  list: { paddingHorizontal: space.lg, gap: space.sm, flexGrow: 1 },
  header: { gap: space.sm, paddingBottom: space.xs },
  row: { backgroundColor: palette.white, borderRadius: radius.md, borderWidth: 1, borderColor: palette.grey200, padding: space.md, gap: 4 },
  rowChanged: { borderColor: palette.warningBorder, backgroundColor: palette.warningBg },
  rowHead: { flexDirection: "row", alignItems: "center", gap: space.sm },
  rowFoot: { flexDirection: "row", marginTop: 2 },
  dot: { width: 12, height: 12, borderRadius: 6 },
  time: { fontFamily: font.bodySemiBold, fontSize: 14, fontVariant: ["tabular-nums"], color: palette.grey700 },
  changed: { ...type.label, color: palette.warning, marginTop: 4 },
});
