// Versão antiga mantida por compatibilidade: mesmo nome, outra assinatura.
export function formatMoney(value: number, symbol = '$'): string {
  return symbol + value.toFixed(2);
}
