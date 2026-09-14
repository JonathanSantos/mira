import { Controller, Get, Param } from '@nestjs/common';
import { OrderService } from './order.service';

@Controller('orders')
export class OrderController {
  constructor(private readonly orders: OrderService) {}

  @Get(':id/total')
  total(@Param('id') id: string): number {
    const order = this.orders.findOne(id);
    if (!order) {
      return 0;
    }
    return this.orders.total(order);
  }
}
