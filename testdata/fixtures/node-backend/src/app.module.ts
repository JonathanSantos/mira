import { Module } from '@nestjs/common';
import { OrderController, OrderService } from './orders';

@Module({
  controllers: [OrderController],
  providers: [OrderService],
})
export class AppModule {}
