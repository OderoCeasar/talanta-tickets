import type { ReactNode } from "react";
import {
  ActivityIndicator,
  Modal,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
  type RefreshControlProps,
  type StyleProp,
  type TextInputProps,
  type ViewStyle,
} from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { errorMessage } from "../lib/errors";
import { font, palette, radius, shadow, space, type } from "../theme";
import { Icon, type IconName } from "./Icon";

export { Icon } from "./Icon";
export type { IconName } from "./Icon";

// ---------------------------------------------------------------------------- layout

interface ScreenProps {
  children: ReactNode;
  /** Wraps the content in a ScrollView. Use false for screens that own a list or a canvas. */
  scroll?: boolean;
  padded?: boolean;
  /** Pinned under the content, above the system navigation bar. */
  footer?: ReactNode;
  refreshControl?: React.ReactElement<RefreshControlProps>;
  style?: StyleProp<ViewStyle>;
}

export function Screen({ children, scroll = true, padded = true, footer, refreshControl, style }: ScreenProps) {
  const insets = useSafeAreaInsets();
  const pad = padded ? styles.padded : null;
  // With a footer the footer takes the bottom inset; without one the content does.
  const bottom = footer ? 0 : insets.bottom;
  return (
    <View style={[styles.screen, style]}>
      {scroll ? (
        <ScrollView
          contentContainerStyle={[pad, { paddingBottom: (padded ? space.xl : 0) + bottom }]}
          keyboardShouldPersistTaps="handled"
          refreshControl={refreshControl}
        >
          {children}
        </ScrollView>
      ) : (
        <View style={[styles.fill, pad, { paddingBottom: bottom }]}>{children}</View>
      )}
      {footer ? <View style={[styles.footer, { paddingBottom: Math.max(insets.bottom, space.md) }]}>{footer}</View> : null}
    </View>
  );
}

export function Card({ children, style, onPress, accessibilityLabel }: { children: ReactNode; style?: StyleProp<ViewStyle>; onPress?: () => void; accessibilityLabel?: string }) {
  if (onPress) {
    return (
      <Pressable
        onPress={onPress}
        accessibilityRole="button"
        accessibilityLabel={accessibilityLabel}
        android_ripple={{ color: palette.ink100 }}
        style={({ pressed }) => [styles.card, pressed && styles.cardPressed, style]}
      >
        {children}
      </Pressable>
    );
  }
  return <View style={[styles.card, style]}>{children}</View>;
}

export function Row({ children, style }: { children: ReactNode; style?: StyleProp<ViewStyle> }) {
  return <View style={[styles.row, style]}>{children}</View>;
}

// ---------------------------------------------------------------------------- buttons

interface ButtonProps {
  title: string;
  onPress: () => void;
  /** `light` is the main button on an Emerald Ink panel. */
  variant?: "primary" | "secondary" | "ghost" | "danger" | "light";
  disabled?: boolean;
  loading?: boolean;
  icon?: IconName;
  testID?: string;
  style?: StyleProp<ViewStyle>;
}

export function Button({ title, onPress, variant = "primary", disabled, loading, icon, testID, style }: ButtonProps) {
  const off = disabled || loading;
  const fg = variant === "primary" ? palette.champagne : variant === "danger" ? palette.error : palette.emeraldInk;
  const ripple = variant === "primary" ? palette.inkSoft : variant === "light" ? palette.champagne300 : palette.ink100;
  return (
    <Pressable
      testID={testID}
      onPress={onPress}
      disabled={off}
      accessibilityRole="button"
      accessibilityState={{ disabled: !!off, busy: !!loading }}
      android_ripple={{ color: ripple }}
      style={({ pressed }) => [
        styles.button,
        variant === "primary" && styles.buttonPrimary,
        variant === "light" && styles.buttonLight,
        variant === "secondary" && styles.buttonSecondary,
        variant === "danger" && styles.buttonDanger,
        off && styles.buttonOff,
        pressed && !off && styles.buttonPressed,
        style,
      ]}
    >
      {loading ? <ActivityIndicator color={fg} /> : icon ? <Icon name={icon} color={fg} size={18} /> : null}
      <Text style={[styles.buttonText, { color: fg }]}>{title}</Text>
    </Pressable>
  );
}

// ---------------------------------------------------------------------------- flow

const STEPS = ["Seats", "Pay", "Tickets"] as const;

/**
 * Where the fan is in a purchase: choose seats, pay, get tickets. Shown at the top of each
 * buying screen so nobody wonders how many more steps there are.
 */
export function FlowSteps({ step }: { step: 1 | 2 | 3 }) {
  return (
    <View style={styles.steps} accessible accessibilityLabel={`Step ${step} of ${STEPS.length}: ${STEPS[step - 1]}`}>
      {STEPS.map((name, i) => {
        const n = i + 1;
        const done = n < step;
        const current = n === step;
        return (
          <View key={name} style={styles.stepItem}>
            {/* Half a line each side of the circle, touching it, never under it. */}
            <View style={styles.stepTrack}>
              <View style={[styles.stepLine, n <= step && styles.stepLineOn, i === 0 && styles.stepLineNone]} />
              <View style={[styles.stepDot, (done || current) && styles.stepDotOn]}>
                {done ? <Icon name="check" size={12} color={palette.champagne} /> : <Text style={[styles.stepNumber, current && styles.stepNumberOn]} maxFontSizeMultiplier={1.2}>{n}</Text>}
              </View>
              <View style={[styles.stepLine, n < step && styles.stepLineOn, i === STEPS.length - 1 && styles.stepLineNone]} />
            </View>
            <Text style={[styles.stepName, (done || current) && styles.stepNameOn]} maxFontSizeMultiplier={1.3}>{name}</Text>
          </View>
        );
      })}
    </View>
  );
}

// ---------------------------------------------------------------------------- lists

/** One tappable line in a settings-style list: icon, title, optional detail, chevron. */
export function ListRow({ icon, title, detail, onPress, tone = "default", testID, last }: { icon: IconName; title: string; detail?: string; onPress: () => void; tone?: "default" | "danger"; testID?: string; last?: boolean }) {
  const fg = tone === "danger" ? palette.error : palette.emeraldInk;
  return (
    <Pressable
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={detail ? `${title}. ${detail}` : title}
      android_ripple={{ color: palette.ink100 }}
      testID={testID}
      style={({ pressed }) => [styles.listRow, !last && styles.listRowBorder, pressed && styles.cardPressed]}
    >
      <View style={[styles.listIcon, tone === "danger" && { backgroundColor: palette.errorBg }]}>
        <Icon name={icon} size={18} color={fg} />
      </View>
      <View style={styles.fill}>
        <Text style={[type.label, { color: tone === "danger" ? palette.error : palette.grey900 }]}>{title}</Text>
        {detail ? <Text style={type.small}>{detail}</Text> : null}
      </View>
      {tone === "danger" ? null : <Icon name="chevron" size={18} color={palette.grey400} />}
    </Pressable>
  );
}

/** A small heading above a group of cards, with an optional action on the right. */
export function SectionTitle({ title, action, onAction }: { title: string; action?: string; onAction?: () => void }) {
  return (
    <View style={styles.sectionTitle}>
      <Text style={[type.h3, styles.fill]} accessibilityRole="header">{title}</Text>
      {action && onAction ? (
        <Pressable onPress={onAction} accessibilityRole="button" hitSlop={12}>
          <Text style={[type.label, { color: palette.emeraldInk }]}>{action}</Text>
        </Pressable>
      ) : null}
    </View>
  );
}

// ---------------------------------------------------------------------------- messages

type Tone = "info" | "warning" | "error" | "success";

const TONES: Record<Tone, { bg: string; border: string; fg: string; icon: IconName }> = {
  info: { bg: palette.champagne50, border: palette.champagne300, fg: palette.grey900, icon: "info" },
  warning: { bg: palette.warningBg, border: palette.warningBorder, fg: palette.warning, icon: "warning" },
  error: { bg: palette.errorBg, border: palette.errorBorder, fg: palette.error, icon: "warning" },
  success: { bg: palette.ink100, border: palette.ink200, fg: palette.emeraldInk, icon: "check" },
};

export function Banner({ tone = "info", title, children, action, onAction, testID }: { tone?: Tone; title?: string; children?: ReactNode; action?: string; onAction?: () => void; testID?: string }) {
  const t = TONES[tone];
  return (
    <View testID={testID} accessibilityRole={tone === "error" ? "alert" : undefined} style={[styles.banner, { backgroundColor: t.bg, borderColor: t.border }]}>
      <View style={styles.bannerIcon}>
        <Icon name={t.icon} color={t.fg} size={18} />
      </View>
      <View style={styles.fill}>
        {title ? <Text style={[type.label, { color: t.fg }]}>{title}</Text> : null}
        {typeof children === "string" ? <Text style={[type.muted, { color: tone === "info" ? palette.grey700 : t.fg }]}>{children}</Text> : children}
        {action && onAction ? (
          <Pressable onPress={onAction} accessibilityRole="button" hitSlop={10} style={styles.bannerAction}>
            <Text style={[type.label, styles.link, { color: t.fg }]}>{action}</Text>
          </Pressable>
        ) : null}
      </View>
    </View>
  );
}

export function Chip({ label, tone = "neutral" }: { label: string; tone?: "neutral" | "ink" | "warning" | "error" | "muted" }) {
  const look = {
    neutral: { bg: palette.champagne, fg: palette.emeraldInk },
    ink: { bg: palette.emeraldInk, fg: palette.champagne },
    warning: { bg: palette.warningBg, fg: palette.warning },
    error: { bg: palette.errorBg, fg: palette.error },
    muted: { bg: palette.grey200, fg: palette.grey700 },
  }[tone];
  return (
    <View style={[styles.chip, { backgroundColor: look.bg }]}>
      <Text style={[styles.chipText, { color: look.fg }]}>{label}</Text>
    </View>
  );
}

export function Loading({ label = "Loading…" }: { label?: string }) {
  return (
    <View style={styles.state} accessibilityRole="progressbar" accessibilityLabel={label}>
      <ActivityIndicator size="large" color={palette.emeraldInk} />
      <Text style={type.muted}>{label}</Text>
    </View>
  );
}

export function ErrorState({ error, onRetry, title = "That did not load" }: { error: unknown; onRetry?: () => void; title?: string }) {
  return (
    <View style={styles.state}>
      <View style={styles.stateIcon}>
        <Icon name="warning" color={palette.error} size={26} />
      </View>
      <Text style={[type.h3, styles.center]}>{title}</Text>
      <Text style={[type.muted, styles.center]}>{errorMessage(error)}</Text>
      {onRetry ? <Button title="Try again" variant="secondary" onPress={onRetry} testID="retry" /> : null}
    </View>
  );
}

export function EmptyState({ icon = "info", title, body, action, onAction }: { icon?: IconName; title: string; body?: string; action?: string; onAction?: () => void }) {
  return (
    <View style={styles.state}>
      <View style={[styles.stateIcon, { backgroundColor: palette.champagne }]}>
        <Icon name={icon} size={26} />
      </View>
      <Text style={[type.h3, styles.center]}>{title}</Text>
      {body ? <Text style={[type.muted, styles.center]}>{body}</Text> : null}
      {action && onAction ? <Button title={action} onPress={onAction} /> : null}
    </View>
  );
}

// ---------------------------------------------------------------------------- inputs

interface FieldProps extends TextInputProps {
  label: string;
  error?: string | null;
  hint?: string;
  prefix?: string;
}

export function Field({ label, error, hint, prefix, style, ...input }: FieldProps) {
  return (
    <View style={styles.field}>
      <Text style={type.label}>{label}</Text>
      <View style={[styles.inputWrap, error ? styles.inputError : null]}>
        {prefix ? <Text style={styles.inputPrefix}>{prefix}</Text> : null}
        <TextInput
          accessibilityLabel={label}
          placeholderTextColor={palette.grey400}
          selectionColor={palette.emeraldInk}
          style={[styles.input, style]}
          {...input}
        />
      </View>
      {error ? <Text style={[type.small, { color: palette.error }]} accessibilityRole="alert">{error}</Text> : hint ? <Text style={type.small}>{hint}</Text> : null}
    </View>
  );
}

// ---------------------------------------------------------------------------- sheet

export function Sheet({ visible, onClose, title, children }: { visible: boolean; onClose: () => void; title: string; children: ReactNode }) {
  const insets = useSafeAreaInsets();
  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onClose} statusBarTranslucent>
      <View style={styles.sheetRoot}>
        <Pressable style={styles.sheetBackdrop} onPress={onClose} accessibilityLabel="Close" />
        <View style={[styles.sheet, { paddingBottom: Math.max(insets.bottom, space.lg) }]}>
          <View style={styles.sheetHandle} />
          <Text style={type.h2} accessibilityRole="header">{title}</Text>
          <ScrollView contentContainerStyle={styles.sheetBody} keyboardShouldPersistTaps="handled">{children}</ScrollView>
        </View>
      </View>
    </Modal>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  center: { textAlign: "center" },
  link: { textDecorationLine: "underline" },
  screen: { flex: 1, backgroundColor: palette.champagne50 },
  padded: { padding: space.lg, gap: space.lg },
  footer: {
    paddingHorizontal: space.lg,
    paddingTop: space.md,
    gap: space.sm,
    backgroundColor: palette.white,
    borderTopWidth: StyleSheet.hairlineWidth,
    borderTopColor: palette.grey300,
  },
  card: {
    backgroundColor: palette.white,
    borderRadius: radius.lg,
    padding: space.lg,
    gap: space.sm,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: palette.grey200,
    ...shadow.card,
  },
  cardPressed: { backgroundColor: palette.ink50 },
  row: { flexDirection: "row", alignItems: "center", gap: space.sm, flexWrap: "wrap" },
  button: {
    minHeight: 52,
    borderRadius: radius.md,
    paddingHorizontal: space.lg,
    paddingVertical: space.md,
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "center",
    gap: space.sm,
    overflow: "hidden",
  },
  buttonPrimary: { backgroundColor: palette.emeraldInk },
  buttonLight: { backgroundColor: palette.champagne },
  buttonSecondary: { backgroundColor: palette.white, borderWidth: 1.5, borderColor: palette.emeraldInk },
  buttonDanger: { backgroundColor: palette.white, borderWidth: 1.5, borderColor: palette.errorBorder },
  buttonOff: { opacity: 0.5 },
  buttonPressed: { opacity: 0.88 },
  buttonText: { ...type.button, flexShrink: 1 },
  banner: { flexDirection: "row", gap: space.sm, padding: space.md, borderRadius: radius.md, borderWidth: 1 },
  bannerIcon: { paddingTop: 2 },
  bannerAction: { marginTop: space.xs, alignSelf: "flex-start", minHeight: 32, justifyContent: "center" },
  chip: { paddingHorizontal: 10, paddingVertical: 3, borderRadius: radius.pill, alignSelf: "flex-start" },
  chipText: { ...type.chip },
  state: { flex: 1, minHeight: 260, alignItems: "center", justifyContent: "center", gap: space.md, padding: space.xl },
  stateIcon: { width: 56, height: 56, borderRadius: 28, alignItems: "center", justifyContent: "center", backgroundColor: palette.errorBg },
  field: { gap: 6 },
  inputWrap: {
    flexDirection: "row",
    alignItems: "center",
    backgroundColor: palette.white,
    borderWidth: 1.5,
    borderColor: palette.grey300,
    borderRadius: radius.md,
    paddingHorizontal: space.md,
  },
  inputError: { borderColor: palette.error },
  inputPrefix: { ...type.input, color: palette.grey600, marginRight: space.sm },
  input: { ...type.input, flex: 1, minHeight: 52, paddingVertical: space.sm },
  sheetRoot: { flex: 1, justifyContent: "flex-end" },
  sheetBackdrop: { position: "absolute", left: 0, right: 0, top: 0, bottom: 0, backgroundColor: "rgba(4, 30, 23, 0.55)" },
  sheet: {
    backgroundColor: palette.white,
    borderTopLeftRadius: radius.lg,
    borderTopRightRadius: radius.lg,
    paddingHorizontal: space.lg,
    paddingTop: space.sm,
    gap: space.md,
    maxHeight: "85%",
  },
  steps: { flexDirection: "row", alignItems: "flex-start" },
  stepItem: { flex: 1, alignItems: "stretch", gap: 4 },
  stepTrack: { flexDirection: "row", alignItems: "center" },
  stepLine: { flex: 1, height: 2, backgroundColor: palette.grey300 },
  stepLineNone: { backgroundColor: "transparent" },
  stepLineOn: { backgroundColor: palette.emeraldInk },
  stepDot: { width: 24, height: 24, borderRadius: 12, alignItems: "center", justifyContent: "center", backgroundColor: palette.white, borderWidth: 2, borderColor: palette.grey300 },
  stepDotOn: { backgroundColor: palette.emeraldInk, borderColor: palette.emeraldInk },
  stepNumber: { fontFamily: font.bodySemiBold, fontSize: 12, color: palette.grey600 },
  stepNumberOn: { color: palette.champagne },
  stepName: { fontFamily: font.bodyMedium, fontSize: 12, color: palette.grey600, textAlign: "center" },
  stepNameOn: { fontFamily: font.bodySemiBold, color: palette.emeraldInk },
  listRow: { flexDirection: "row", alignItems: "center", gap: space.md, paddingVertical: space.md, paddingHorizontal: space.lg, minHeight: 60 },
  listRowBorder: { borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: palette.grey200 },
  listIcon: { width: 36, height: 36, borderRadius: 18, alignItems: "center", justifyContent: "center", backgroundColor: palette.champagne },
  sectionTitle: { flexDirection: "row", alignItems: "center", gap: space.sm, marginTop: space.xs },
  sheetHandle: { alignSelf: "center", width: 44, height: 5, borderRadius: 3, backgroundColor: palette.grey300 },
  sheetBody: { gap: space.md, paddingBottom: space.sm },
});
