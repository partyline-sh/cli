import { useLocalSearchParams } from "expo-router";
import { LoopDetailsScreen } from "@/ui/LoopDetailsScreen";
export default function LoopRoute() {
  const { computer, id } = useLocalSearchParams<{
    computer: string;
    id: string;
  }>();
  return <LoopDetailsScreen computer={computer} id={id} />;
}
