import { useState } from 'react';
import { formatMoney } from '../utils/format';

export interface CartItem {
  sku: string;
  price: number;
}

export function useCart() {
  const [items, setItems] = useState<CartItem[]>([]);
  const total = items.reduce((sum, item) => sum + item.price, 0);

  function add(item: CartItem) {
    setItems([...items, item]);
  }

  return { items, add, total, label: formatMoney(total) };
}
