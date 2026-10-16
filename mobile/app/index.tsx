import { Redirect } from "expo-router";
import { useEffect, useState } from "react";

import { readWelcomeSeen } from "../src/api/storage";
import { startRoute } from "../src/lib/startRoute";
import { useSession } from "../src/session";
import { Loading, Screen } from "../src/ui";

export default function Index() {
  const { status, user } = useSession();
  const [welcomeSeen, setWelcomeSeen] = useState<boolean | null>(null);
  useEffect(() => {
    void readWelcomeSeen().then(setWelcomeSeen);
  }, []);

  // Gate staff use the same app but never see the fan screens.
  const route = startRoute({ status, role: user?.role, welcomeSeen });
  if (route) return <Redirect href={route} />;
  return (
    <Screen scroll={false}>
      <Loading label="Opening Talanta Tickets…" />
    </Screen>
  );
}
