import type { TerminalDimensions } from './terminal-resize';
import type { Attachment } from './uploads';
/** Public UI contract. Pairing secrets and WebSockets never cross this boundary. */
export type ComputerId = string;
export type PaneId = string;
export type ConnectionState = 'connecting' | 'connected' | 'reconnecting' | 'offline' | 'revoked';
export type SessionState = 'waiting' | 'working' | 'idle';
export type Tab = 'sessions' | 'loops' | 'chat' | 'computers';
export type SessionView = 'messages' | 'terminal';
export type InputKey = 'Escape' | 'Enter' | 'Tab' | 'BTab' | 'BSpace' | 'Up' | 'Down' | 'Left' | 'Right' | 'PageUp' | 'PageDown' | 'C-c' | 'C-d' | 'C-r' | 'C-l';

export type RelayHosting = 'self-hosted' | 'cloudflare';
export interface Computer {
  id: ComputerId;
  name: string;
  relay: string;
  relayHosting?: RelayHosting;
  connection: ConnectionState;
  lastReceivedAt?: number;
  error?: string;
  capabilities: readonly string[];
  pairedAt: number;
}
export interface Session {
  id: string;
  computerId: ComputerId;
  pane: PaneId;
  title: string;
  engine: string;
  state: SessionState;
  project?: string;
  lastLine?: string;
  updatedAt?: number;
}
export interface Tool {
  name: string;
  target?: string;
  status?: 'running' | 'ok' | 'error';
}
export interface TranscriptEntry {
  id: string;
  role: 'user' | 'assistant';
  text: string;
  tools: readonly Tool[];
  truncated?: boolean;
}
export interface PaneSnapshot {
  computerId: ComputerId;
  pane: PaneId;
  terminal?: { ansi: string; columns: number; rows: number; receivedAt: number };
  transcript?: { entries: readonly TranscriptEntry[]; revision: number; receivedAt: number };
  transcriptSupported: boolean;
  stale: boolean;
}
export interface LoopItem {
  id: string;
  computerId: ComputerId;
  section: string;
  title: string;
  state: string;
  repo?: string;
  detail?: string;
  round?: number;
  batch?: string;
  openP1?: number;
  dispute?: string;
  pr?: number;
  ci?: string;
  builder?: string;
  chips?: readonly { name: string; mark: string }[];
  receivedAt?: number;
  updatedAt?: number;
}
export type LoopAction = 'approve' | 'reject' | 'fix' | 'dismiss' | 'stop' | 'delete' | 'resume' | 'reverse';
export interface LoopDetail {
  id: string; title: string; state: string; revision: number;
  brief: string; history: string; actions: LoopAction[];
  deleteTarget: string; pr: string;
  dispute: { id?: string; claim?: string; dispute?: string; file?: string; line?: number };
}
export interface LoopConfirmation {
  token: string; code: string; revision: number; action: LoopAction; detail: LoopDetail;
}
export interface ChatMessage {
  id: string;
  computerId: ComputerId;
  name: string;
  text: string;
  at: string;
  own: boolean;
}
export interface Delivery {
  id: string;
  computerId: ComputerId;
  pane: PaneId | 'chat';
  status: 'waiting' | 'confirmed' | 'refused' | 'unknown' | 'not-sent';
  reason?: string;
  data?: unknown;
}
export type PairingState =
  | { status: 'idle' }
  | { status: 'scanning' }
  | { status: 'connecting' }
  | { status: 'awaiting-approval'; code: string; fingerprint: string }
  | { status: 'paired'; computerId: ComputerId }
  | { status: 'error'; message: string; code?: 'camera-denied' | 'pairing-failed' };
export interface Selection { computerId: ComputerId; pane: PaneId }
export interface PartylineState {
  mode: 'fixture' | 'live';
  loading: boolean;
  computers: readonly Computer[];
  sessions: readonly Session[];
  panes: Readonly<Record<string, PaneSnapshot>>;
  loops: readonly LoopItem[];
  chat: readonly ChatMessage[];
  deliveries: readonly Delivery[];
  drafts: Readonly<Record<string, string>>;
  pairing: PairingState;
  tab: Tab;
  selected?: Selection;
  sessionView: SessionView;
}
export interface PartylineActions {
  selectTab(tab: Tab): void;
  selectSession(selection?: Selection): void;
  selectView(view: SessionView): void;
  setDraft(computerId: ComputerId, pane: PaneId | 'chat', text: string): void;
  startScan(): void;
  pair(codeOrLink: string): Promise<void>;
  cancelPair(): void;
  forgetComputer(computerId: ComputerId): Promise<void>;
  reconnect(computerId: ComputerId): void;
  sendText(computerId: ComputerId, pane: PaneId, text: string): Promise<Delivery>;
  paste(computerId: ComputerId, pane: PaneId, text: string): Promise<Delivery>;
  sendKey(computerId: ComputerId, pane: PaneId, key: InputKey): Promise<Delivery>;
  sendChat(computerId: ComputerId, text: string): Promise<Delivery>;
  openShell(computerId: ComputerId): Promise<Delivery>;
  readLoop(computerId: ComputerId, id: string): Promise<LoopDetail>;
  prepareLoopAction(computerId: ComputerId, id: string, action: LoopAction): Promise<LoopConfirmation>;
  confirmLoopAction(computerId: ComputerId, id: string, confirmation: LoopConfirmation, code: string, note: string): Promise<Delivery>;
  resizeTerminal(computerId: ComputerId, pane: PaneId, mode: 'phone' | 'desktop', viewport?: TerminalDimensions): Promise<void>;
  uploadAttachment(computerId: ComputerId, pane: PaneId, file: Attachment, progress: (fraction: number) => void, signal: AbortSignal): Promise<string>;
  /** UI must show the duplicate-execution warning before setting acknowledgeRisk. */
  retryDelivery(id: string, acknowledgeRisk: boolean): Promise<Delivery>;
  /** UI must warn about duplicate execution before acknowledging unknown delivery. */
  acknowledgeDelivery(id: string): void;
  /** No structured prompt source exists yet. Do not render answer buttons. */
  answer: undefined;
}
export type PartylineController = PartylineState & PartylineActions;
/** Composite keys prevent pane ids on different computers from colliding. */
export const paneKey = (computerId: ComputerId, pane: PaneId): string => `${computerId}:${pane}`;
