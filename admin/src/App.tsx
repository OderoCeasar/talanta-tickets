import { getHealth } from "@talanta/shared";
import { useEffect, useState } from "react";

type ApiState = "checking" | "ok" | "degraded" | "unreachable";

export function App() {
  const [api, setApi] = useState<ApiState>("checking");

  useEffect(() => {
    getHealth("")
      .then((health) => setApi(health.status))
      .catch(() => setApi("unreachable"));
  }, []);

  return (
    <>
      <header className="topbar">Talanta Tickets Admin</header>
      <main className="page">
        <h1>Organiser console</h1>
        <p>Events, stadium layout, pricing and sales.</p>
        <p>
          API status: <strong>{api}</strong>
        </p>
      </main>
    </>
  );
}
