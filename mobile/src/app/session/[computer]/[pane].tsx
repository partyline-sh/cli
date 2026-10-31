import { useLocalSearchParams } from 'expo-router';
import { SessionScreen } from '@/ui';
export default function SessionRoute() {
  const { computer, pane } = useLocalSearchParams<{ computer: string; pane: string }>();
  return <SessionScreen computer={computer} pane={pane} />;
}
