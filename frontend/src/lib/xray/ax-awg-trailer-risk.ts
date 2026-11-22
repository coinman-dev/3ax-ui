// 3AX-UI: random trailers relax the handshake size checks, so a receiver reads a data packet's
// type at each handshake's S offset; off S4 that is random bytes (amnezia-vpn/amneziawg-go#183).

export interface AwgTrailerRiskFields {
  s1?: number;
  s2?: number;
  s3?: number;
  s4?: number;
  h1?: string;
  h2?: string;
  h3?: string;
  h4?: string;
  randomTrailers?: boolean;
}

// Above this share of lost data packets TCP throughput visibly collapses.
export const AX_AWG_TRAILER_LOSS_THRESHOLD = 0.001;

function headerBounds(value: string | undefined): [number, number] | null {
  const m = /^\s*(\d+)\s*(?:-\s*(\d+)\s*)?$/.exec(value ?? '');
  if (!m) return null;
  const lo = Number(m[1]);
  const hi = m[2] === undefined ? lo : Number(m[2]);
  return hi >= lo ? [lo, hi] : null;
}

export function axAwgTrailerLoss(s: AwgTrailerRiskFields): number {
  if (!s.randomTrailers) return 0;
  const s4 = s.s4 ?? 0;
  let kept = 1;
  for (const [pad, h] of [
    [s.s1, s.h1],
    [s.s2, s.h2],
    [s.s3, s.h3],
  ] as const) {
    const bounds = headerBounds(h);
    if ((pad ?? 0) === s4 || !bounds) continue;
    kept *= 1 - (bounds[1] - bounds[0] + 1) / 2 ** 32;
  }
  return 1 - kept;
}

// Each range's low end: still distinct and non-overlapping, and the smallest change for clients.
export function axAwgSingleValueHeaders(
  s: AwgTrailerRiskFields,
): Pick<AwgTrailerRiskFields, 'h1' | 'h2' | 'h3' | 'h4'> {
  const narrow = (h: string | undefined) => {
    const bounds = headerBounds(h);
    return bounds ? String(bounds[0]) : h;
  };
  return { h1: narrow(s.h1), h2: narrow(s.h2), h3: narrow(s.h3), h4: narrow(s.h4) };
}

export function formatAwgTrailerLoss(loss: number): string {
  return (loss * 100).toFixed(1);
}
