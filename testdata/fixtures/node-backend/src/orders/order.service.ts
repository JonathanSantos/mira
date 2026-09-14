import { Injectable } from '@nestjs/common';
import { calculateDiscount } from '../pricing/discount';

export interface OrderItem {
  sku: string;
  price: number;
  quantity: number;
}

export interface Order {
  id: string;
  coupon?: string;
  items: OrderItem[];
}

@Injectable()
export class OrderService {
  private readonly orders = new Map<string, Order>();

  findOne(id: string): Order | undefined {
    return this.orders.get(id);
  }

  total(order: Order): number {
    const subtotal = order.items.reduce((sum, item) => sum + item.price * item.quantity, 0);
    return calculateDiscount(subtotal, order.coupon);
  }
}
