const { calculateTax } = require('./tax');

// roundMoney não é importado: existe em utils/money.ts e em legacy/money.js,
// então a referência fica ambígua de propósito.
function buildReport(order) {
  const tax = calculateTax(order.total, 0.2);
  return { total: order.total, tax: roundMoney(tax) };
}

module.exports = { buildReport };
