import { useLocalSearchParams } from "expo-router";
import { StreamTerminalScreen } from "../../../ui/StreamTerminalScreen";
export default function TerminalRoute() {
  const { computer, session } = useLocalSearchParams<{
    computer: string;
    session: string;
  }>();
  return (
    <StreamTerminalScreen
      key={`${computer}:${session}`}
      computer={computer}
      session={session}
    />
  );
}
