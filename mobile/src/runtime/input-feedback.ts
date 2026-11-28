export interface InputFeedback { inputDelivery: 'delivered' | 'agent_working' | 'in_composer'; retryInputId?: string }
export function readInputFeedback(value: unknown): InputFeedback | undefined {
  if (!value || typeof value !== 'object') return;
  const data = value as Record<string, unknown>;
  if (!['delivered', 'agent_working', 'in_composer'].includes(String(data.inputDelivery))) return;
  return { inputDelivery: data.inputDelivery as InputFeedback['inputDelivery'], retryInputId: typeof data.retryInputId === 'string' && data.retryInputId.length <= 128 ? data.retryInputId : undefined };
}
