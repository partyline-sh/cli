import { Tabs } from 'expo-router';
import { KeyboardAvoidingView, Platform } from 'react-native';
import { TabBar } from '@/ui';
export default function Layout() {
  // Keep navigation above the iOS keyboard too. Avoiding only the Chat body left
  // the tab bar underneath it, trapping the person in the text field.
  return <KeyboardAvoidingView style={{ flex: 1 }} behavior="padding" enabled={Platform.OS === 'ios'}>
    <Tabs screenOptions={{ headerShown: false }} tabBar={props => <TabBar {...props} />}><Tabs.Screen name="index" options={{ title: 'Sessions' }} /><Tabs.Screen name="loops" options={{ title: 'Loops' }} /><Tabs.Screen name="chat" options={{ title: 'Chat' }} /><Tabs.Screen name="computers" options={{ title: 'Computers' }} /></Tabs>
  </KeyboardAvoidingView>;
}
