import { Text, View } from "react-native";

import { screen } from "../../src/theme";

export default function TicketsScreen() {
  return (
    <View style={screen.container}>
      <Text style={screen.title}>My tickets</Text>
      <Text style={screen.body}>Tickets you have bought will be kept here.</Text>
    </View>
  );
}
