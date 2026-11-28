import type { ReactNode } from 'react';
import NativeSvg, { Path as NativePath, Rect as NativeRect } from 'react-native-svg';

/** A 24-point line icon, as the prototype draws them: stroked, no fill, 2-point lines. */
export default function Svg({ children, size = 24 }: { children: ReactNode; size?: number }) {
  return <NativeSvg width={size} height={size} viewBox="0 0 24 24" fill="none">{children}</NativeSvg>;
}
export function Path(props: { d: string; stroke: string }) { return <NativePath {...props} strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" fill="none" />; }
export function Rect(props: { x: string; y: string; width: string; height: string; rx: string; stroke: string }) { return <NativeRect {...props} strokeWidth={2} fill="none" />; }
