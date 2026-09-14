import './Button.css';
import { formatMoney } from '../utils/format';

export interface ButtonProps {
  label: string;
  price?: number;
  onClick?: () => void;
}

export default function Button({ label, price, onClick }: ButtonProps) {
  const suffix = price === undefined ? '' : ` (${formatMoney(price)})`;
  return (
    <button type="button" onClick={onClick}>
      {label}
      {suffix}
    </button>
  );
}
