import { useSyncExternalStore } from "react";

const MOBILE_BREAKPOINT = 768;
const query = () => window.matchMedia(`(max-width: ${MOBILE_BREAKPOINT - 1}px)`);

function subscribe(onChange: () => void) {
  const mql = query();
  mql.addEventListener("change", onChange);
  return () => mql.removeEventListener("change", onChange);
}

export function useIsMobile() {
  return useSyncExternalStore(subscribe, () => query().matches, () => false);
}
