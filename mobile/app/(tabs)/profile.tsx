import { Text, View } from "react-native";

import { screen } from "../../src/theme";

export default function ProfileScreen() {
  return (
    <View style={screen.container}>
      <Text style={screen.title}>Profile</Text>
      <Text style={screen.body}>Sign in with your phone number to see your bookings.</Text>
    </View>
  );
}
