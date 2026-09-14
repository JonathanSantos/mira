import { roundMoney } from '../utils/money';

const COUPONS: Record<string, number> = { WELCOME10: 0.1, VIP20: 0.2 };

export function applyCoupon(total: number, coupon?: string): number {
  if (!coupon || !(coupon in COUPONS)) {
    return total;
  }
  return total * (1 - COUPONS[coupon]);
}

export function calculateDiscount(total: number, coupon?: string): number {
  return roundMoney(applyCoupon(total, coupon));
}
