import { getHealth } from "@talanta/shared";
import { useEffect, useState } from "react";
import { Text, View } from "react-native";

import { API_URL } from "../../src/api/config";
import { screen } from "../../src/theme";

type ApiState = "checking" | "ok" | "degraded" | "unreachable";

export default function EventsScreen() {
  const [api, setApi] = useState<ApiState>("checking");

  useEffect(() => {
    getHealth(API_URL)
      .then((health) => setApi(health.status))
      .catch(() => setApi("unreachable"));
  }, []);

  return (
    <View style={screen.container}>
      <Text style={screen.title}>Upcoming events</Text>
      <Text style={screen.body}>Matches and events at Talanta Stadium will be listed here.</Text>
      <Text style={screen.body}>API status: {api}</Text>
    </View>
  );
}
