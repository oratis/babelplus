import { createRoot } from 'react-dom/client';
import '../ui/base.css';
import { Onboarding } from './Onboarding.tsx';

const root = document.getElementById('root');
if (root) createRoot(root).render(<Onboarding />);
