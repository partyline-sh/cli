import "./platform";
import {
  createContext,
  useContext,
  useEffect,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { AppState, Platform } from "react-native";
import { useFixturePartyline } from "./fixture-controller";
import { PartylineClient } from "./client";
import { deviceStorage } from "./storage";
import type { PartylineController } from "./types";
export * from "./types";
const Context = createContext<PartylineController | undefined>(undefined);
function FixtureProvider({ children }: { children: ReactNode }) {
  return (
    <Context.Provider value={useFixturePartyline()}>
      {children}
    </Context.Provider>
  );
}
function LiveProvider({ children }: { children: ReactNode }) {
  const [client] = useState(() => new PartylineClient(deviceStorage));
  const state = useSyncExternalStore(
    client.subscribe,
    client.snapshot,
    client.snapshot,
  );
  useEffect(() => {
    client.foreground(AppState.currentState === "active");
    void client.start();
    const subscription = AppState.addEventListener("change", (state) =>
      client.foreground(state === "active"),
    );
    return () => {
      subscription.remove();
      client.dispose();
    };
  }, [client]);
  return (
    <Context.Provider value={{ ...state, ...client.actions }}>
      {children}
    </Context.Provider>
  );
}
export function PartylineProvider({
  children,
  mode = Platform.OS === "web" ? "fixture" : "live",
}: {
  children: ReactNode;
  mode?: "fixture" | "live";
}) {
  return mode === "fixture" ? (
    <FixtureProvider>{children}</FixtureProvider>
  ) : (
    <LiveProvider>{children}</LiveProvider>
  );
}
export function usePartyline(): PartylineController {
  const controller = useContext(Context);
  if (!controller)
    throw new Error("usePartyline must be inside PartylineProvider");
  return controller;
}
