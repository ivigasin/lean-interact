/** Ordering of Lean version strings (`v4.19.0`, `v4.9.0-rc1`, ...). */

const PRE_ORDER: Record<string, number> = { dev: 0, a: 1, alpha: 1, b: 2, beta: 2, c: 3, pre: 3, preview: 3, rc: 3 };

interface ParsedVersion {
  release: number[];
  pre: [number, number] | null;
  raw: string;
}

function parse(version: string): ParsedVersion {
  const raw = version.startsWith("v") ? version.slice(1) : version;
  const m = /^(\d+(?:\.\d+)*)(?:[-.]?([a-zA-Z]+)[-.]?(\d*))?/.exec(raw);
  if (!m) return { release: [], pre: null, raw };
  const release = m[1].split(".").map(Number);
  const tag = m[2]?.toLowerCase();
  const pre: [number, number] | null = tag !== undefined ? [PRE_ORDER[tag] ?? 3, Number(m[3] || 0)] : null;
  return { release, pre, raw };
}

/**
 * Compare two Lean versions with PEP 440-like semantics: numeric release components, and a
 * pre-release (`-rc1`) sorts before its final release.
 */
export function compareLeanVersions(a: string, b: string): number {
  const pa = parse(a);
  const pb = parse(b);
  const n = Math.max(pa.release.length, pb.release.length);
  for (let i = 0; i < n; i++) {
    const d = (pa.release[i] ?? 0) - (pb.release[i] ?? 0);
    if (d !== 0) return d;
  }
  if (pa.pre === null && pb.pre !== null) return 1;
  if (pa.pre !== null && pb.pre === null) return -1;
  if (pa.pre !== null && pb.pre !== null) {
    const d = pa.pre[0] - pb.pre[0] || pa.pre[1] - pb.pre[1];
    if (d !== 0) return d;
  }
  return pa.raw < pb.raw ? -1 : pa.raw > pb.raw ? 1 : 0;
}
