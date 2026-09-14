// Módulo CommonJS antigo, ainda usado pelos relatórios.
function calculateTax(amount, rate) {
  return amount * rate;
}

module.exports = { calculateTax };
