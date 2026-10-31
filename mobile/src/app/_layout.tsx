import { Stack } from 'expo-router';
import { StatusBar } from 'expo-status-bar';
import { PartylineProvider } from '../runtime';
import { storePreview } from '../preview/config';
import { PreviewNavigation } from '../preview/PreviewNavigation';
import { StartupNavigation } from '../ui/StartupNavigation';

export default function Layout() {
  return <PartylineProvider mode={storePreview ? 'fixture' : undefined}><StatusBar style="light" /><Stack screenOptions={{ headerShown: false, contentStyle: { backgroundColor: '#1b1a19' } }}><Stack.Screen name="pair" options={{ presentation: 'fullScreenModal' }} /></Stack>{storePreview ? <PreviewNavigation /> : <StartupNavigation />}</PartylineProvider>;
}
