import { ApiError, fieldErrors, unwrap } from "@talanta/shared";
import { useMutation } from "@tanstack/react-query";
import { router } from "expo-router";
import { useEffect, useRef, useState } from "react";
import { KeyboardAvoidingView, Platform, Pressable, StyleSheet, Text, View } from "react-native";

import { api } from "../src/api/client";
import { errorMessage } from "../src/lib/errors";
import { formatPhone, normalisePhone } from "../src/lib/phone";
import { useNow } from "../src/lib/useNow";
import { useSession } from "../src/session";
import { palette, radius, space, type } from "../src/theme";
import { Banner, Button, Card, Field, Screen } from "../src/ui";

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
        {step === null ? (
          <Card>
            <Text style={type.h2}>Sign in with your phone</Text>
            <Text style={type.muted}>We will text you a 6-digit code. No password needed.</Text>
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
              keyboardType="phone-pad"
              autoComplete="tel"
              textContentType="telephoneNumber"
              autoFocus
              returnKeyType="send"
              onSubmitEditing={sendCode}
            />
            {request.isError && !phoneError ? <Banner tone="error">{errorMessage(request.error)}</Banner> : null}
            <Button title="Send code" onPress={sendCode} loading={request.isPending} testID="send-code" />
          </Card>
        ) : (
          <Card>
            <Text style={type.h2}>Enter the code</Text>
            <Text style={type.muted}>We sent a 6-digit code to {formatPhone(step.phone)}.</Text>
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
            <Field
              label="Code"
              testID="code-input"
              value={code}
              onChangeText={(t) => setCode(t.replace(/[^0-9]/g, "").slice(0, 6))}
              keyboardType="number-pad"
              autoComplete="sms-otp"
              textContentType="oneTimeCode"
              maxLength={6}
              autoFocus
              style={styles.codeInput}
              error={verify.isError ? errorMessage(verify.error) : null}
            />
            <Button
              title="Sign in"
              onPress={() => verify.mutate({ phone: step.phone, code })}
              disabled={code.length !== 6}
              loading={verify.isPending}
              testID="verify-code"
            />
            {request.isError ? <Banner tone="error">{errorMessage(request.error)}</Banner> : null}
            <Button
              title={resendIn > 0 ? `Send a new code in ${resendIn}s` : "Send a new code"}
              variant="ghost"
              disabled={resendIn > 0}
              loading={request.isPending}
              onPress={() => request.mutate(step.phone)}
              testID="resend-code"
            />
            <Button title="Use a different number" variant="ghost" onPress={() => setStep(null)} />
          </Card>
        )}
        <Text style={[type.small, styles.footnote]}>Signing in is only needed to hold seats and to see your tickets.</Text>
      </Screen>
    </KeyboardAvoidingView>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  link: { textDecorationLine: "underline", color: palette.warning },
  dev: { backgroundColor: palette.warningBg, borderColor: palette.warningBorder, borderWidth: 1, borderStyle: "dashed", borderRadius: radius.md, padding: space.md, gap: 4 },
  devLabel: { ...type.overline, color: palette.warning },
  devCode: { ...type.mono, letterSpacing: 2 },
  devButton: { alignSelf: "flex-start", minHeight: 36, justifyContent: "center" },
  codeInput: { ...type.mono, fontSize: 26, letterSpacing: 8, textAlign: "center" },
  footnote: { textAlign: "center" },
});
