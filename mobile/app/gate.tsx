import { Redirect } from "expo-router";
import { StyleSheet, Text, View } from "react-native";

import { formatPhone } from "../src/lib/phone";
import { useSession } from "../src/session";
import { palette, space, type } from "../src/theme";
import { Button, Card, Icon, Screen } from "../src/ui";

/** Placeholder for gate staff until scanning is built (milestone M10). */
export default function GateScreen() {
  const { status, user, signOut } = useSession();
  if (status === "signedOut") return <Redirect href="/events" />;

  return (
    <Screen>
      <Card>
        <View style={styles.icon}>
          <Icon name="gate" size={30} color={palette.champagne} />
        </View>
        <Text style={type.h2}>Gate mode is coming</Text>
        <Text style={type.body}>
          You are signed in as gate staff{user?.phone ? ` (${formatPhone(user.phone)})` : ""}. Ticket scanning will be in this app before match day.
        </Text>
        <Text style={type.muted}>To buy tickets for yourself, sign out and sign in with your own number.</Text>
      </Card>
      <Button title="Sign out" variant="secondary" onPress={() => void signOut()} testID="gate-sign-out" />
    </Screen>
  );
}

const styles = StyleSheet.create({
  icon: { width: 56, height: 56, borderRadius: 14, backgroundColor: palette.emeraldInk, alignItems: "center", justifyContent: "center", marginBottom: space.sm },
});
