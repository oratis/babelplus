import { createRoot } from 'react-dom/client';
import '../ui/base.css';
import { Options } from './Options.tsx';

const root = document.getElementById('root');
if (root) createRoot(root).render(<Options />);
