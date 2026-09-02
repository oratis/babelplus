import { createRoot } from 'react-dom/client';
import '../ui/base.css';
import { Popup } from './Popup.tsx';

document.body.classList.add('popup');
const root = document.getElementById('root');
if (root) createRoot(root).render(<Popup />);
