import './styles.css';
import logo from './logo.svg';
import { Button, Card } from '../ui';
import { useCart } from '../hooks/useCart';

export default function Checkout() {
  const cart = useCart();
  return (
    <Card title="Checkout">
      <img src={logo} alt="logo" />
      <p>{cart.label}</p>
      <Button label="Pay" price={cart.total} onClick={() => cart.add({ sku: 'x', price: 1 })} />
    </Card>
  );
}
