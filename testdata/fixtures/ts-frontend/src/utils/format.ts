export const CURRENCY = 'USD';

export function formatMoney(value: number): string {
  return `${roundMoney(value).toFixed(2)} ${CURRENCY}`;
}

export const roundMoney = (value: number): number => Math.round(value * 100) / 100;
