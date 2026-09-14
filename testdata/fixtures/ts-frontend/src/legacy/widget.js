// Script legado carregado por <script>: usa formatMoney como global, sem import.
// Existem duas definições exportadas no repo, então a referência é ambígua.
function renderPrice(el, value) {
  el.textContent = formatMoney(value);
}

window.renderPrice = renderPrice;
