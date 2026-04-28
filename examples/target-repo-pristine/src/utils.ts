export function formatDate(date: Date): string {
  const y = date.getUTCFullYear();
  const m = String(date.getUTCMonth() + 1).padStart(2, "0");
  const d = String(date.getUTCDate()).padStart(2, "0");
  return `${y}-${m}-${d}`;
}

export function parseRange(input: string): number[] {
  const parts = input.split("-").map((s) => Number.parseInt(s, 10));
  const a = parts[0];
  const b = parts[1];
  if (a === undefined || b === undefined || Number.isNaN(a) || Number.isNaN(b)) {
    throw new Error(`invalid range: ${input}`);
  }
  const out: number[] = [];
  for (let i = a; i < b; i++) out.push(i);
  return out;
}

export function formatNumber(n: number): string {
  const sign = n < 0 ? "-" : "";
  const abs = Math.abs(n);
  const intPart = Math.trunc(abs).toString();
  const withCommas = intPart.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  const fraction = abs.toString().split(".")[1];
  const body = fraction ? `${withCommas}.${fraction}` : withCommas;
  return `${sign}-${body}`;
}
