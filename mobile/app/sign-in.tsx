import { ApiError, fieldErrors, unwrap } from "@talanta/shared";
import { useMutation } from "@tanstack/react-query";
import { router } from "expo-router";
import { useEffect, useRef, useState } from "react";
import { KeyboardAvoidingView, Platform, Pressable, StyleSheet, Text, TextInput, View } from "react-native";

import { api } from "../src/api/client";
import { errorMessage } from "../src/lib/errors";
import { formatPhone, normalisePhone } from "../src/lib/phone";
import { useNow } from "../src/lib/useNow";
import { useSession } from "../src/session";
import { font, palette, radius, space, type } from "../src/theme";
import { Banner, Button, Field, Icon, Screen } from "../src/ui";

interface CodeStep {
  phone: string;
  /** Phone clock time after which another code may be requested. */
  resendAt: number;
  /** Present only when the server is not in production; shown so the app works without SMS. */
  devCode: string | null;
}

export default function SignInScreen() {
  const { signIn } = useSession();
  const [phoneInput, setPhoneInput] = useState("");
  const [phoneError, setPhoneError] = useState<string | null>(null);
  const [step, setStep] = useState<CodeStep | null>(null);
  const [code, setCode] = useState("");
  const now = useNow(1000, step !== null);
  const submittedCode = useRef<string | null>(null);

  const request = useMutation({
    mutationFn: (phone: string) => unwrap(api.POST("/auth/otp/request", { body: { phone } })).then((answer) => ({ phone, answer })),
    onSuccess: ({ phone, answer }) => {
      setCode("");
      submittedCode.current = null;
      verify.reset();
      setStep({ phone, resendAt: Date.now() + answer.resend_after_seconds * 1000, devCode: answer.dev_code ?? null });
    },
    onError: (error) => {
      const fields = fieldErrors(error);
      if (fields.phone) setPhoneError(fields.phone);
      // A 429 says how long to wait: start the resend countdown from it.
      if (error instanceof ApiError && error.status === 429 && error.retryAfterSeconds !== null) {
        const wait = error.retryAfterSeconds;
        setStep((s) => (s ? { ...s, resendAt: Date.now() + wait * 1000 } : s));
      }
    },
  });

  const verify = useMutation({
    mutationFn: (input: { phone: string; code: string }) => unwrap(api.POST("/auth/otp/verify", { body: input })),
    onSuccess: async (session) => {
      await signIn(session);
      // Back to whatever asked for sign-in (the seat map's Continue, My tickets, Profile).
      if (router.canGoBack()) router.back();
      else router.replace("/events");
    },
  });

  const sendCode = () => {
    const result = normalisePhone(phoneInput);
    if (!result.ok) {
      setPhoneError(result.message);
      return;
    }
    setPhoneError(null);
    request.mutate(result.e164);
  };

  // Six digits typed: submit without making the fan find the button. Once per code.
  useEffect(() => {
    if (step && code.length === 6 && submittedCode.current !== code && !verify.isPending) {
      submittedCode.current = code;
      verify.mutate({ phone: step.phone, code });
    }
  }, [code, step, verify]);

  const resendIn = step ? Math.max(0, Math.ceil((step.resendAt - now) / 1000)) : 0;

  return (
    <KeyboardAvoidingView style={styles.fill} behavior={Platform.OS === "ios" ? "padding" : undefined}>
      <Screen>
        <View style={styles.intro}>
          <View style={styles.badge}>
            <Icon name={step === null ? "phone" : "lock"} size={28} color={palette.champagne} />
          </View>
          <Text style={type.h1} accessibilityRole="header">{step === null ? "Sign in with your phone" : "Enter the code"}</Text>
          <Text style={type.muted}>
            {step === null ? "We will text you a 6-digit code. No password needed." : `We sent a 6-digit code to ${formatPhone(step.phone)}.`}
          </Text>
        </View>

        {step === null ? (
          <>
            <Field
              label="Phone number"
              testID="phone-input"
              value={phoneInput}
              onChangeText={(t) => {
                setPhoneInput(t);
                setPhoneError(null);
              }}
              error={phoneError}
              hint="For example 0712 345 678"
              placeholder="0712 345 678"
              keyboardType="phone-pad"
              autoComplete="tel"
              textContentType="telephoneNumber"
              autoFocus
              returnKeyType="send"
              onSubmitEditing={sendCode}
            />
            {request.isError && !phoneError ? <Banner tone="error">{errorMessage(request.error)}</Banner> : null}
            <Button title="Send code" onPress={sendCode} loading={request.isPending} testID="send-code" />
          </>
        ) : (
          <>
            {step.devCode ? (
              <View style={styles.dev} testID="dev-code">
                <Text style={styles.devLabel}>DEVELOPMENT ONLY</Text>
                <Text style={type.body}>
                  This server is not in production, so it tells the app the code: <Text style={styles.devCode}>{step.devCode}</Text>
                </Text>
                <Pressable onPress={() => setCode(step.devCode ?? "")} accessibilityRole="button" hitSlop={8} testID="use-dev-code" style={styles.devButton}>
                  <Text style={[type.label, styles.link]}>Use this code</Text>
                </Pressable>
              </View>
            ) : null}
            <CodeBoxes code={code} onChange={setCode} error={verify.isError ? errorMessage(verify.error) : null} busy={verify.isPending} />
            <Button
              title="Sign in"
              onPress={() => verify.mutate({ phone: step.phone, code })}
              disabled={code.length !== 6}
              loading={verify.isPending}
              testID="verify-code"
            />
            {request.isError ? <Banner tone="error">{errorMessage(request.error)}</Banner> : null}
            <View style={styles.links}>
              <Button
                title={resendIn > 0 ? `New code in ${resendIn}s` : "Send a new code"}
                variant="ghost"
                disabled={resendIn > 0}
                loading={request.isPending}
                onPress={() => request.mutate(step.phone)}
                testID="resend-code"
                style={styles.link1}
              />
              <Button title="Change number" variant="ghost" onPress={() => setStep(null)} style={styles.link1} />
            </View>
          </>
        )}
        <View style={styles.footnote}>
          <Icon name="shield" size={16} color={palette.grey600} />
          <Text style={[type.small, styles.fill]}>You can browse matches without signing in. You only need it to hold seats and to see your tickets.</Text>
        </View>
      </Screen>
    </KeyboardAvoidingView>
  );
}

const CODE_LENGTH = 6;

/**
 * The code as six boxes. One real (invisible) input sits on top, so the keyboard, paste and the
 * phone's SMS autofill all behave as they would on a plain field.
 */
function CodeBoxes({ code, onChange, error, busy }: { code: string; onChange: (code: string) => void; error: string | null; busy: boolean }) {
  const [focused, setFocused] = useState(true);
  return (
    <View style={styles.codeField}>
      <View style={styles.boxes}>
        {Array.from({ length: CODE_LENGTH }, (_, i) => {
          const active = focused && !busy && (i === code.length || (i === CODE_LENGTH - 1 && code.length === CODE_LENGTH));
          return (
            <View key={i} style={[styles.box, code[i] ? styles.boxFilled : null, active && styles.boxActive, error ? styles.boxError : null]}>
              <Text style={styles.boxDigit} maxFontSizeMultiplier={1.3}>{code[i] ?? ""}</Text>
            </View>
          );
        })}
        <TextInput
          accessibilityLabel="Code"
          accessibilityHint="Six digits from the text message"
          testID="code-input"
          value={code}
          onChangeText={(t) => onChange(t.replace(/[^0-9]/g, "").slice(0, CODE_LENGTH))}
          onFocus={() => setFocused(true)}
          onBlur={() => setFocused(false)}
          keyboardType="number-pad"
          autoComplete="sms-otp"
          textContentType="oneTimeCode"
          maxLength={CODE_LENGTH}
          autoFocus
          caretHidden
          style={styles.hiddenInput}
        />
      </View>
      {error ? <Text style={[type.small, { color: palette.error }]} accessibilityRole="alert">{error}</Text> : null}
    </View>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  link: { textDecorationLine: "underline", color: palette.warning },
  dev: { backgroundColor: palette.warningBg, borderColor: palette.warningBorder, borderWidth: 1, borderStyle: "dashed", borderRadius: radius.md, padding: space.md, gap: 4 },
  devLabel: { ...type.overline, color: palette.warning },
  devCode: { ...type.mono, letterSpacing: 2 },
  devButton: { alignSelf: "flex-start", minHeight: 36, justifyContent: "center" },
  intro: { gap: space.sm, marginTop: space.sm },
  badge: { width: 56, height: 56, borderRadius: 28, backgroundColor: palette.emeraldInk, alignItems: "center", justifyContent: "center", marginBottom: space.xs },
  codeField: { gap: space.sm },
  boxes: { flexDirection: "row", gap: space.sm },
  box: { flex: 1, height: 58, borderRadius: radius.md, borderWidth: 1.5, borderColor: palette.grey300, backgroundColor: palette.white, alignItems: "center", justifyContent: "center" },
  boxFilled: { borderColor: palette.ink400 },
  boxActive: { borderColor: palette.emeraldInk, borderWidth: 2 },
  boxError: { borderColor: palette.error },
  boxDigit: { fontFamily: font.headingBold, fontSize: 24, color: palette.grey900 },
  // Covers the boxes so a tap anywhere on them opens the keyboard; its own text is invisible.
  hiddenInput: { position: "absolute", left: 0, right: 0, top: 0, bottom: 0, color: "transparent", opacity: 0.02, fontSize: 1 },
  links: { flexDirection: "row", justifyContent: "space-between", gap: space.sm },
  link1: { flex: 1, minHeight: 44, paddingHorizontal: space.sm },
  footnote: { flexDirection: "row", gap: space.sm, alignItems: "flex-start", marginTop: space.md },
});
