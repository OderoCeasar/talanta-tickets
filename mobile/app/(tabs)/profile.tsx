import { fieldErrors } from "@talanta/shared";
import { router } from "expo-router";
import { useEffect, useState } from "react";
import { Alert, StyleSheet, Text, View } from "react-native";

import { API_ORIGIN } from "../../src/api/config";
import { useUpdateName } from "../../src/api/queries";
import { errorMessage } from "../../src/lib/errors";
import { formatPhone } from "../../src/lib/phone";
import { useSession } from "../../src/session";
import { palette, space, type } from "../../src/theme";
import { Banner, Button, Card, EmptyState, Field, Icon, Loading, Screen } from "../../src/ui";

export default function ProfileScreen() {
  const { status, user, signOut } = useSession();
  const update = useUpdateName();
  const [name, setName] = useState("");
  const [signingOut, setSigningOut] = useState(false);

  const savedName = user?.name ?? "";
  useEffect(() => setName(savedName), [savedName]);

  if (status === "loading") return <Loading />;
  if (status === "signedOut") {
    return (
      <Screen scroll={false}>
        <EmptyState icon="user" title="You are not signed in" body="Sign in with your phone number to hold seats, pay and see your tickets." action="Sign in" onAction={() => router.push("/sign-in")} />
      </Screen>
    );
  }
  if (!user) return <Loading label="Loading your profile…" />;

  const trimmed = name.trim();
  const dirty = trimmed !== savedName;
  const nameError = update.isError ? (fieldErrors(update.error).name ?? errorMessage(update.error)) : null;

  const confirmSignOut = () =>
    Alert.alert("Sign out?", "Your tickets stay in your account. You will need a new code to sign in again.", [
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

  return (
    <Screen>
      <Card>
        <View style={styles.identity}>
          <View style={styles.avatar}>
            <Icon name="user" size={26} color={palette.champagne} />
          </View>
          <View style={styles.fill}>
            <Text style={type.h2}>{savedName || "Talanta fan"}</Text>
            <Text style={type.muted} testID="profile-phone">{formatPhone(user.phone)}</Text>
          </View>
        </View>
      </Card>

      <Card>
        <Text style={type.h3}>Your name</Text>
        <Text style={type.muted}>It is printed on tickets you buy from now on.</Text>
        <Field
          label="Name"
          testID="name-input"
          value={name}
          onChangeText={(t) => {
            setName(t);
            if (update.isError || update.isSuccess) update.reset();
          }}
          placeholder="For example Achieng Odhiambo"
          autoCapitalize="words"
          autoComplete="name"
          maxLength={100}
          error={nameError}
        />
        {update.isSuccess && !dirty ? <Banner tone="success">Saved.</Banner> : null}
        <Button title="Save name" onPress={() => update.mutate(trimmed)} disabled={!dirty || trimmed.length === 0} loading={update.isPending} testID="save-name" />
      </Card>

      <Button title="Sign out" variant="danger" onPress={confirmSignOut} loading={signingOut} testID="sign-out" />
      {__DEV__ ? <Text style={[type.small, styles.dev]}>Development build · API {API_ORIGIN}</Text> : null}
    </Screen>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  identity: { flexDirection: "row", alignItems: "center", gap: space.md },
  avatar: { width: 52, height: 52, borderRadius: 26, backgroundColor: palette.emeraldInk, alignItems: "center", justifyContent: "center" },
  dev: { textAlign: "center" },
});
