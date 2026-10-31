import { Alert, Pressable, Text, View } from 'react-native';
import { useState } from 'react';
import { usePartyline, type Delivery } from '../runtime';
import { c } from './theme';

export function ControlDelivery({ delivery }: { delivery?: Delivery }) {
  const app = usePartyline();
  const [error, setError] = useState('');
  if (!delivery) return null;
  const retry = () => { void app.retryDelivery(delivery.id, true).catch(e => setError(String(e))); };
  return <View style={{ gap: 10 }} accessibilityLiveRegion="polite">
    <Text style={{ color: delivery.status === 'confirmed' ? c.ok : c.warn }}>
      {delivery.status === 'waiting' ? 'Waiting for computer confirmation…' : delivery.status === 'unknown' ? 'Delivery unknown. Check the computer before trying again.' : delivery.status === 'confirmed' ? 'Confirmed by computer.' : delivery.status === 'not-sent' ? 'Not sent.' : 'Refused.'}{delivery.reason ? ` ${delivery.reason}` : ''}
    </Text>
    {error ? <Text style={{ color: c.warn }}>{error}</Text> : null}
    {delivery.status === 'unknown' || delivery.status === 'not-sent' ? <Pressable accessibilityRole="button" onPress={() => Alert.alert('Retry this action?', 'It may already have run. If the computer restarted, retrying could repeat it. Loop decisions also require their original live challenge.', [{ text: 'Cancel', style: 'cancel' }, { text: 'Retry', style: 'destructive', onPress: retry }])}><Text style={{ color: c.accent }}>Retry the same request</Text></Pressable> : null}
    {delivery.status === 'unknown' ? <Pressable accessibilityRole="button" onPress={() => Alert.alert('Allow another action?', 'This action may already have run. Check the session or loop before continuing; a new action may repeat it.', [{ text: 'Cancel', style: 'cancel' }, { text: 'I checked; continue', style: 'destructive', onPress: () => app.acknowledgeDelivery(delivery.id) }])}><Text style={{ color: c.accent }}>I checked the computer…</Text></Pressable> : null}
  </View>;
}
