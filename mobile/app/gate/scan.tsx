import { CameraView, useCameraPermissions } from "expo-camera";
import * as Haptics from "expo-haptics";
import { useKeepAwake } from "expo-keep-awake";
import { Redirect, router, useFocusEffect } from "expo-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { Keyboard, Linking, Pressable, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { useGate } from "../../src/features/gate/GateProvider";
import type { ScanOutcome } from "../../src/features/gate/scanner";
import { describeVerdict } from "../../src/features/gate/verdict";
import { formatCount } from "../../src/lib/money";
import { formatClock } from "../../src/lib/time";
import { font, palette, radius, space, type } from "../../src/theme";
import { Button, Field, Icon, Loading, Screen } from "../../src/ui";

/** How long a result stays up before the scanner takes the next ticket by itself. */
const ADMIT_MS = 1800;
const REFUSE_MS = 4500;
/** The camera reports the same code many times a second; one already handled is ignored this long. */
const SAME_CODE_MS = 4000;

type Shown = { kind: "result"; outcome: ScanOutcome } | { kind: "failed" } | null;

/** Two clearly different patterns, so the steward knows without looking. */
function buzz(admitted: boolean) {
  if (admitted) {
    void Haptics.notificationAsync(Haptics.NotificationFeedbackType.Success).catch(() => undefined);
    return;
  }
  void Haptics.notificationAsync(Haptics.NotificationFeedbackType.Error).catch(() => undefined);
  setTimeout(() => void Haptics.impactAsync(Haptics.ImpactFeedbackStyle.Heavy).catch(() => undefined), 350);
}

export default function ScanScreen() {
  useKeepAwake();
  const gate = useGate();
  const insets = useSafeAreaInsets();
  const [permission, requestPermission] = useCameraPermissions();
  const [shown, setShown] = useState<Shown>(null);
  const [typing, setTyping] = useState(false);
  const [typed, setTyped] = useState("");
  const [focused, setFocused] = useState(true);

  const busy = useRef(false);
  const last = useRef<{ text: string; at: number } | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const { scan } = gate;

  // The camera is released while another screen is on top.
  useFocusEffect(
    useCallback(() => {
      setFocused(true);
      return () => setFocused(false);
    }, []),
  );
  useEffect(() => () => void (timer.current && clearTimeout(timer.current)), []);

  const dismiss = useCallback(() => {
    if (timer.current) clearTimeout(timer.current);
    timer.current = null;
    // The code that was just handled is probably still in front of the camera.
    if (last.current) last.current.at = Date.now();
    busy.current = false;
    setShown(null);
  }, []);

  const handle = useCallback(
    async (text: string, fromCamera: boolean) => {
      if (busy.current) return;
      if (fromCamera && last.current && last.current.text === text && Date.now() - last.current.at < SAME_CODE_MS) return;
      busy.current = true;
      last.current = { text, at: Date.now() };
      try {
        // Resolves only after the scan is written to the phone's storage.
        const outcome = await scan(text);
        buzz(outcome.result === "ADMITTED");
        setShown({ kind: "result", outcome });
        timer.current = setTimeout(dismiss, outcome.result === "ADMITTED" ? ADMIT_MS : REFUSE_MS);
      } catch (error) {
        // The scan could not be saved, so there is no result: neither green nor red.
        console.warn("A scan could not be saved.", error);
        buzz(false);
        setShown({ kind: "failed" });
      }
    },
    [scan, dismiss],
  );

  if (gate.status !== "ready") {
    return (
      <Screen scroll={false}>
        <Loading label="Opening gate mode…" />
      </Screen>
    );
  }
  if (!gate.device || !gate.manifest) return <Redirect href="/gate" />;

  const submitTyped = () => {
    // A pasted code often brings a space or a line break with it; the code itself has none.
    const text = typed.replace(/\s+/g, "");
    if (!text) return;
    Keyboard.dismiss();
    setTyping(false);
    setTyped("");
    void handle(text, false);
  };

  const cameraOn = focused && permission?.granted === true && shown === null && !typing;

  return (
    <View style={styles.root}>
      {permission?.granted ? (
        <>
          {/* Unmounted behind a result or the keyboard, so nothing is read while one is up. */}
          {focused ? (
            <CameraView
              style={StyleSheet.absoluteFill}
              facing="back"
              active={cameraOn}
              barcodeScannerSettings={{ barcodeTypes: ["qr"] }}
              onBarcodeScanned={cameraOn ? (event) => void handle(event.data, true) : undefined}
            />
          ) : null}
          <View style={styles.frameWrap} pointerEvents="none">
            <View style={styles.frame} />
            <Text style={styles.hint}>Hold the ticket's QR code inside the square</Text>
          </View>
        </>
      ) : (
        <View style={styles.noCamera}>
          {permission === null ? (
            <Loading label="Checking the camera…" />
          ) : (
            <>
              <Icon name="lock" size={34} color={palette.champagne} />
              <Text style={styles.noCameraTitle}>The camera is off for this app</Text>
              <Text style={styles.noCameraBody}>
                {permission.canAskAgain ? "Allow the camera to scan ticket codes." : "Turn the camera on for Talanta Tickets in the phone's settings."} You can also type a ticket's code.
              </Text>
              <Button
                title={permission.canAskAgain ? "Allow camera" : "Open settings"}
                onPress={() => void (permission.canAskAgain ? requestPermission() : Linking.openSettings())}
                variant="secondary"
                style={styles.noCameraButton}
                testID="scan-allow-camera"
              />
            </>
          )}
        </View>
      )}

      <View style={[styles.bar, { paddingBottom: Math.max(insets.bottom, space.md) }]}>
        <View style={styles.barInfo}>
          <Text style={styles.barGate} testID="scan-gate">{gate.device.gateGroup}</Text>
          <Text style={styles.barSmall} numberOfLines={2}>
            {gate.online === false ? "No connection · " : ""}
            {gate.pending === 0 ? "All scans uploaded" : `${formatCount(gate.pending)} waiting to upload`}
          </Text>
        </View>
        <Button title="Type a code" variant="secondary" onPress={() => setTyping(true)} testID="scan-type" />
      </View>

      {/* At the top of the screen, not in a bottom sheet: the keyboard must never cover the code or the button. */}
      {typing ? (
        <View style={[styles.typePanel, { paddingTop: space.lg }]} testID="scan-type-panel">
          <Text style={type.h2} accessibilityRole="header">Type or paste a ticket code</Text>
          <Text style={type.muted}>For when the camera will not read a code. The code starts with TLT1.</Text>
          <Field
            label="Ticket code"
            testID="scan-code-input"
            value={typed}
            onChangeText={setTyped}
            placeholder="TLT1.…"
            autoFocus
            autoCapitalize="none"
            autoCorrect={false}
            autoComplete="off"
            spellCheck={false}
            returnKeyType="done"
            onSubmitEditing={submitTyped}
            style={styles.codeInput}
          />
          <View style={styles.typeButtons}>
            <Button title="Cancel" variant="secondary" onPress={() => (setTyping(false), setTyped(""))} style={styles.typeButton} testID="scan-code-cancel" />
            <Button title="Check ticket" onPress={submitTyped} disabled={typed.trim() === ""} style={styles.typeButton} testID="scan-code-submit" />
          </View>
        </View>
      ) : null}

      {shown ? <Result shown={shown} onDismiss={dismiss} /> : null}
    </View>
  );
}

function Result({ shown, onDismiss }: { shown: Exclude<Shown, null>; onDismiss: () => void }) {
  const insets = useSafeAreaInsets();
  if (shown.kind === "failed") {
    return (
      <Pressable style={[styles.result, styles.resultFailed, { paddingTop: insets.top + space.xl, paddingBottom: insets.bottom + space.xl }]} onPress={onDismiss} accessibilityRole="button" testID="scan-result-failed">
        <Icon name="warning" size={72} color={palette.white} />
        <Text style={styles.resultTitle} accessibilityRole="alert">Scan again</Text>
        <Text style={styles.resultDetail}>This scan could not be saved on the phone, so it was not counted. Do not let anyone in on it. Scan the ticket again.</Text>
        <Text style={styles.resultTap}>Tap to continue</Text>
      </Pressable>
    );
  }

  const { outcome } = shown;
  const admitted = outcome.result === "ADMITTED";
  const words = describeVerdict(outcome.result, outcome.reason, outcome.ticketGateGroup);
  return (
    <Pressable
      style={[styles.result, admitted ? styles.resultAdmit : styles.resultRefuse, { paddingTop: insets.top + space.xl, paddingBottom: insets.bottom + space.xl }]}
      onPress={onDismiss}
      accessibilityRole="button"
      accessibilityLabel={`${words.title}. ${words.detail} Tap to scan the next ticket.`}
      testID={admitted ? "scan-result-admitted" : "scan-result-refused"}
    >
      {/* Not colour alone: a tick or a cross, and the words. */}
      <Icon name={admitted ? "check" : "close"} size={96} color={palette.white} />
      <Text style={styles.resultTitle} accessibilityRole="alert" testID="scan-result-title">{words.title}</Text>
      <Text style={styles.resultDetail} testID="scan-result-detail">{words.detail}</Text>
      <View style={styles.resultFacts}>
        {admitted && outcome.ticketGateGroup ? <Text style={styles.resultFact}>Gate {outcome.ticketGateGroup}</Text> : null}
        {outcome.ticketId ? <Text style={styles.resultFact}>Ticket {outcome.ticketId.slice(0, 8)}</Text> : null}
        <Text style={styles.resultFact}>{formatClock(outcome.scannedAt)}</Text>
      </View>
      <Text style={styles.resultTap}>Tap to scan the next ticket</Text>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: palette.inkDeep },
  frameWrap: { position: "absolute", left: 0, right: 0, top: 0, bottom: 0, alignItems: "center", justifyContent: "center", gap: space.lg, paddingBottom: 96 },
  frame: { width: "72%", aspectRatio: 1, borderRadius: radius.lg, borderWidth: 3, borderColor: palette.champagne },
  hint: { ...type.label, color: palette.white, textAlign: "center", paddingHorizontal: space.xl, textShadowColor: "#000000", textShadowRadius: 6 },
  noCamera: { flex: 1, alignItems: "center", justifyContent: "center", padding: space.xl, gap: space.md },
  noCameraTitle: { ...type.h2, color: palette.white, textAlign: "center" },
  noCameraBody: { ...type.body, color: palette.champagne300, textAlign: "center" },
  noCameraButton: { alignSelf: "stretch" },
  bar: { position: "absolute", left: 0, right: 0, bottom: 0, flexDirection: "row", alignItems: "center", gap: space.md, paddingHorizontal: space.lg, paddingTop: space.md, backgroundColor: palette.emeraldInk },
  barInfo: { flex: 1 },
  barGate: { fontFamily: font.headingExtraBold, fontSize: 22, lineHeight: 28, color: palette.champagne },
  barSmall: { ...type.small, color: palette.champagne300 },
  typePanel: { position: "absolute", left: 0, right: 0, top: 0, backgroundColor: palette.white, padding: space.lg, gap: space.md, borderBottomLeftRadius: radius.lg, borderBottomRightRadius: radius.lg },
  typeButtons: { flexDirection: "row", gap: space.md },
  typeButton: { flex: 1 },
  codeInput: { fontFamily: "monospace", fontSize: 14 },
  result: { position: "absolute", left: 0, right: 0, top: 0, bottom: 0, alignItems: "center", justifyContent: "center", paddingHorizontal: space.xl, gap: space.md },
  resultAdmit: { backgroundColor: palette.admit },
  resultRefuse: { backgroundColor: palette.refuse },
  resultFailed: { backgroundColor: palette.grey900 },
  resultTitle: { fontFamily: font.headingExtraBold, fontSize: 40, lineHeight: 48, color: palette.white, textAlign: "center" },
  resultDetail: { fontFamily: font.bodyMedium, fontSize: 20, lineHeight: 28, color: palette.white, textAlign: "center" },
  resultFacts: { flexDirection: "row", flexWrap: "wrap", justifyContent: "center", gap: space.sm, marginTop: space.sm },
  resultFact: { fontFamily: font.bodySemiBold, fontSize: 15, color: palette.white, backgroundColor: "rgba(0,0,0,0.22)", paddingHorizontal: 12, paddingVertical: 6, borderRadius: radius.pill, overflow: "hidden" },
  resultTap: { ...type.small, color: palette.white, opacity: 0.85, marginTop: space.lg },
});
