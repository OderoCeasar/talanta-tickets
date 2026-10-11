import { useEffect, useState } from "react";

/** The phone's clock, re-read every `intervalMs` while `running`. For countdowns and clocks. */
export function useNow(intervalMs = 1000, running = true): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!running) return;
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs, running]);
  return now;
}
