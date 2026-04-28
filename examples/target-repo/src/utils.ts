export function formatDate(date: Date, timezone: string = "UTC"): string {
  if (timezone === "UTC") {
    const y = date.getUTCFullYear();
    const m = String(date.getUTCMonth() + 1).padStart(2, "0");
    const d = String(date.getUTCDate()).padStart(2, "0");
    return `${y}-${m}-${d}`;
  }

  const parts = new Intl.DateTimeFormat("en", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(date);

  const year = parts.find((part) => part.type === "year")?.value;
  const month = parts.find((part) => part.type === "month")?.value;
  const day = parts.find((part) => part.type === "day")?.value;

  if (year === undefined || month === undefined || day === undefined) {
    throw new Error("failed to format date");
  }

  return `${year}-${month}-${day}`;
}

export function parseRange(input: string): number[] {
  const match = input.match(/^(-?\d+)-(-?\d+)$/);
  if (!match) {
    throw new Error(`invalid range: ${input}`);
  }
  const a = Number.parseInt(match[1], 10);
  const b = Number.parseInt(match[2], 10);
  const out: number[] = [];
  for (let i = a; i <= b; i++) out.push(i);
  return out;
}

export function formatNumber(n: number): string {
  const sign = n < 0 ? "-" : "";
  const [integerPart, fractionPart] = toPlainDecimalString(Math.abs(n)).split(".");
  const withCommas = integerPart.replace(/\B(?=(\d{3})+(?!\d))/g, ",");

  if (fractionPart === undefined) {
    return `${sign}${withCommas}`;
  }

  return `${sign}${withCommas}.${fractionPart}`;
}

function toPlainDecimalString(n: number): string {
  const str = n.toString();

  if (!str.includes("e")) {
    return str;
  }

  const match = /^(\d+)(?:\.(\d+))?e([+-]\d+)$/i.exec(str);
  if (match === null) {
    throw new Error(`failed to format number: ${str}`);
  }

  const whole = match[1];
  const fractional = match[2] ?? "";
  const exponent = Number.parseInt(match[3], 10);
  const digits = `${whole}${fractional}`;
  const decimalIndex = digits.length + exponent - fractional.length;

  if (decimalIndex <= 0) {
    return `0.${"0".repeat(-decimalIndex)}${digits}`;
  }

  if (decimalIndex >= digits.length) {
    return `${digits}${"0".repeat(decimalIndex - digits.length)}`;
  }

  return `${digits.slice(0, decimalIndex)}.${digits.slice(decimalIndex)}`;
}